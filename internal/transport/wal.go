package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Segmented write-ahead log.
//
// Layout: the base path is segment 0; rotated segments are "<path>.000001",
// "<path>.000002", ... Each segment starts with the legacy 24-byte header and
// contains length-prefixed CRC-protected records, keeping the v0.1 on-disk
// record format so existing data and tests remain valid.
//
// Truncation rewrites only the boundary segment and removes later segments —
// no whole-log rewrite. Hard state (term/vote/commit) lives in a separate
// sidecar file "<path>.hs", written atomically (temp + fsync + rename).

const (
	walMagic       = 0x524C5445 // "RLTE"
	walHeaderSize  = 24
	walEntryHeader = 21
	walSegmentSize = 8 << 20 // rotate after 8 MiB

	hsMagic   = 0x524C4853 // "RLHS"
	hsVersion = 1
)

var ErrCorruptWAL = errors.New("corrupt WAL entry")

type segment struct {
	index int    // 0 for base path, 1.. for rotated
	path  string
}

type WAL struct {
	mu       sync.Mutex
	basePath string
	segs     []segment
	f        *os.File // active (last) segment
	lastIdx  uint64
	lastTerm uint64
	hsPath   string
}

// OpenWAL opens or creates the segmented WAL rooted at path.
// A torn tail record (crash mid-append) is discarded on open; a CRC mismatch
// mid-stream is surfaced by Scan as ErrCorruptWAL.
func OpenWAL(path string) (*WAL, error) {
	w := &WAL{basePath: path, hsPath: path + ".hs"}
	if err := w.openSegments(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *WAL) openSegments() error {
	paths := []segment{{index: 0, path: w.basePath}}
	// rotated segments: base.000001, base.000002, ...
	matches, _ := filepath.Glob(w.basePath + ".[0-9][0-9][0-9][0-9][0-9][0-9]")
	for _, m := range matches {
		suffix := strings.TrimPrefix(m, w.basePath+".")
		var idx int
		if _, err := fmt.Sscanf(suffix, "%d", &idx); err != nil {
			continue
		}
		paths = append(paths, segment{index: idx, path: m})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].index < paths[j].index })
	w.segs = paths

	// initialize/validate each segment header; adopt lastIdx/lastTerm from
	// the final segment that has a body.
	for i, seg := range w.segs {
		f, err := os.OpenFile(seg.path, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("open wal segment: %w", err)
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if stat.Size() == 0 {
			if err := writeWALHeader(f, 0, 0); err != nil {
				f.Close()
				return err
			}
		} else if err := validateWALHeader(f); err != nil {
			f.Close()
			return err
		}
		if i == len(w.segs)-1 {
			w.f = f
		} else {
			f.Close()
		}
	}
	// adopt durable log position from the last segment
	if idx, term, err := w.readTailHeader(); err == nil {
		w.lastIdx, w.lastTerm = idx, term
	}
	// Discard any torn tail (FR-022): a crash mid-append leaves a partial
	// record, and appending after it would make every later record
	// unreadable — scan stops at the first invalid record. Truncating to
	// the last complete record on open keeps the log append-safe.
	return w.truncateTornTail()
}

// truncateTornTail removes bytes past the last complete record in the final
// segment and realigns lastIdx/lastTerm with the readable tail. The header
// alone is not authoritative: a crash between the record fsync and the header
// rewrite leaves the header one entry behind.
func (w *WAL) truncateTornTail() error {
	entries, positions, err := w.scanAll()
	if err == ErrCorruptWAL {
		// Corruption is surfaced by Scan, not swallowed during open;
		// do not attempt to truncate a corrupt (but complete) record.
		return nil
	}
	if err != nil {
		return err
	}
	lastSeg := len(w.segs) - 1

	dropBody := func() error {
		stat, serr := os.Stat(w.segs[lastSeg].path)
		if serr != nil || stat.Size() <= walHeaderSize {
			return nil
		}
		if err := os.Truncate(w.segs[lastSeg].path, walHeaderSize); err != nil {
			return err
		}
		w.lastIdx, w.lastTerm = 0, 0
		return writeWALHeader(w.f, 0, 0)
	}

	// Find the last complete record in the active (final) segment.
	end := int64(-1)
	last := -1
	for i, p := range positions {
		if p.seg == lastSeg {
			end = p.off + p.size
			last = i
		}
	}
	if last < 0 {
		// No complete record in the tail segment: drop its whole body.
		return dropBody()
	}
	w.lastIdx, w.lastTerm = entries[last].Index, entries[last].Term

	stat, err := w.f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() > end {
		if err := os.Truncate(w.segs[lastSeg].path, end); err != nil {
			return err
		}
		// The header may reference a record we just discarded.
		return writeWALHeader(w.f, w.lastIdx, w.lastTerm)
	}
	return nil
}

func validateWALHeader(f *os.File) error {
	buf := make([]byte, walHeaderSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(buf[0:4]) != walMagic {
		return ErrCorruptWAL
	}
	return nil
}

func writeWALHeader(f *os.File, lastIdx, lastTerm uint64) error {
	buf := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint32(buf[0:4], walMagic)
	binary.LittleEndian.PutUint64(buf[8:16], lastIdx)
	binary.LittleEndian.PutUint64(buf[16:24], lastTerm)
	_, err := f.WriteAt(buf, 0)
	return err
}

// readTailHeader returns lastIdx/lastTerm recorded in the last segment.
func (w *WAL) readTailHeader() (uint64, uint64, error) {
	buf := make([]byte, walHeaderSize)
	if _, err := w.f.ReadAt(buf, 0); err != nil {
		return 0, 0, err
	}
	return binary.LittleEndian.Uint64(buf[8:16]), binary.LittleEndian.Uint64(buf[16:24]), nil
}

func (w *WAL) segmentPath(i int) string {
	if i == 0 {
		return w.basePath
	}
	return fmt.Sprintf("%s.%06d", w.basePath, i)
}

// Append durably appends one entry and advances the recorded position.
func (w *WAL) Append(entry LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendRaw(entry)
}

func (w *WAL) appendRaw(entry LogEntry) error {
	if err := w.rotateIfNeeded(len(entry.Data) + walEntryHeader + 4); err != nil {
		return err
	}
	frame := encodeRecord(entry)
	if _, err := w.f.Seek(0, 2); err != nil {
		return err
	}
	if _, err := w.f.Write(frame); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	w.lastIdx, w.lastTerm = entry.Index, entry.Term
	return writeWALHeader(w.f, w.lastIdx, w.lastTerm)
}

func encodeRecord(entry LogEntry) []byte {
	data := entry.Data
	if data == nil {
		data = []byte{}
	}
	frame := make([]byte, walEntryHeader+len(data)+4)
	binary.LittleEndian.PutUint64(frame[0:8], entry.Index)
	binary.LittleEndian.PutUint64(frame[8:16], entry.Term)
	frame[16] = entry.Type
	binary.LittleEndian.PutUint32(frame[17:21], uint32(len(data)))
	copy(frame[walEntryHeader:], data)
	binary.LittleEndian.PutUint32(frame[walEntryHeader+len(data):], crc32IEEE(data))
	return frame
}

// rotateIfNeeded opens a new segment when the active one would overflow.
func (w *WAL) rotateIfNeeded(recordSize int) error {
	stat, err := w.f.Stat()
	if err != nil {
		return err
	}
	if stat.Size()+int64(recordSize) <= walSegmentSize {
		return nil
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	next := len(w.segs)
	p := w.segmentPath(next)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	if err := writeWALHeader(f, w.lastIdx, w.lastTerm); err != nil {
		f.Close()
		return err
	}
	w.segs = append(w.segs, segment{index: next, path: p})
	w.f = f
	return nil
}

// recordPos locates a record's byte offset within a segment file.
type recordPos struct {
	seg  int
	off  int64 // offset of record start
	size int64 // total record size on disk
}

// scanAll returns every valid record with its location, or ErrCorruptWAL on a
// CRC mismatch. An incomplete trailing record (torn write) ends the scan —
// that is the crash-recovery case and is not an error.
func (w *WAL) scanAll() ([]LogEntry, []recordPos, error) {
	var entries []LogEntry
	var positions []recordPos
	for si, seg := range w.segs {
		f, err := os.Open(seg.path)
		if err != nil {
			return nil, nil, err
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		body := stat.Size() - walHeaderSize
		if body <= 0 {
			f.Close()
			continue
		}
		data := make([]byte, body)
		if _, err := f.ReadAt(data, walHeaderSize); err != nil {
			f.Close()
			return nil, nil, err
		}
		f.Close()

		pos := int64(0)
		for pos+walEntryHeader <= body {
			idx := binary.LittleEndian.Uint64(data[pos : pos+8])
			term := binary.LittleEndian.Uint64(data[pos+8 : pos+16])
			etyp := data[pos+16]
			dlen := int64(binary.LittleEndian.Uint32(data[pos+17 : pos+21]))
			recStart := pos
			pos += walEntryHeader
			if dlen < 0 || pos+dlen+4 > body {
				break // torn tail: stop cleanly
			}
			entryData := make([]byte, dlen)
			copy(entryData, data[pos:pos+dlen])
			want := binary.LittleEndian.Uint32(data[pos+dlen : pos+dlen+4])
			if crc32IEEE(entryData) != want {
				return nil, nil, ErrCorruptWAL
			}
			entries = append(entries, LogEntry{Index: idx, Term: term, Type: etyp, Data: entryData})
			positions = append(positions, recordPos{
				seg:  si,
				off:  walHeaderSize + recStart,
				size: walEntryHeader + dlen + 4,
			})
			pos += dlen + 4
		}
	}
	return entries, positions, nil
}

// Scan returns entries with Index >= fromIndex.
func (w *WAL) Scan(fromIndex uint64) ([]LogEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries, _, err := w.scanAll()
	if err != nil {
		return nil, err
	}
	if fromIndex < 1 {
		fromIndex = 1
	}
	var out []LogEntry
	for _, e := range entries {
		if e.Index >= fromIndex {
			out = append(out, e)
		}
	}
	return out, nil
}

func (w *WAL) LastIndex() (uint64, error) { return w.lastIdx, nil }

func (w *WAL) LastTerm() (uint64, error) { return w.lastTerm, nil }

// Size returns the total on-disk bytes across all segments, for metrics
// and snapshot-trigger decisions.
func (w *WAL) Size() (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var total int64
	for _, seg := range w.segs {
		if stat, err := os.Stat(seg.path); err == nil {
			total += stat.Size()
		}
	}
	return total, nil
}

// Truncate removes every entry with Index > toIndex by truncating the
// boundary segment and deleting later segments — O(segment), not O(log).
func (w *WAL) Truncate(toIndex uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, positions, err := w.scanAll()
	if err == ErrCorruptWAL {
		// Corruption is surfaced by Scan, not swallowed during open;
		// do not attempt to truncate a corrupt (but complete) record.
		return nil
	}
	if err != nil {
		return err
	}
	// find first kept record and first discarded record
	keepCount := 0
	for _, e := range entries {
		if e.Index <= toIndex {
			keepCount++
		}
	}
	var boundary recordPos
	if keepCount < len(positions) {
		boundary = positions[keepCount]
	} else {
		// nothing to discard
		return nil
	}

	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil

	// truncate the boundary segment at the first discarded record
	segPath := w.segs[boundary.seg].path
	if err := os.Truncate(segPath, boundary.off); err != nil {
		return err
	}
	// drop every segment after the boundary
	for i := boundary.seg + 1; i < len(w.segs); i++ {
		os.Remove(w.segs[i].path)
	}
	w.segs = w.segs[:boundary.seg+1]

	// refresh header position from surviving records
	if keepCount > 0 {
		last := entries[keepCount-1]
		w.lastIdx, w.lastTerm = last.Index, last.Term
	} else {
		w.lastIdx, w.lastTerm = 0, 0
	}

	f, err := os.OpenFile(segPath, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	w.f = f
	return writeWALHeader(f, w.lastIdx, w.lastTerm)
}

func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Sync()
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ---- hard state (term / votedFor / commit index) ----

// LoadHardState reads the sidecar hard-state file. A missing file yields the
// zero value (fresh node); a corrupt file is an error — operators must not
// silently restart a voting node that lost its durable term.
func (w *WAL) LoadHardState() (HardState, error) {
	data, err := os.ReadFile(w.hsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return HardState{}, nil
		}
		return HardState{}, err
	}
	if len(data) < 32 {
		return HardState{}, ErrCorruptWAL
	}
	if binary.LittleEndian.Uint32(data[0:4]) != hsMagic ||
		data[4] != hsVersion {
		return HardState{}, ErrCorruptWAL
	}
	crc := binary.LittleEndian.Uint32(data[28:32])
	if crc32IEEE(data[:28]) != crc {
		return HardState{}, ErrCorruptWAL
	}
	hs := HardState{
		Term:        binary.LittleEndian.Uint64(data[8:16]),
		CommitIndex: binary.LittleEndian.Uint64(data[16:24]),
	}
	vlen := int(binary.LittleEndian.Uint32(data[24:28]))
	if 32+vlen != len(data) {
		return HardState{}, ErrCorruptWAL
	}
	hs.VotedFor = string(data[32 : 32+vlen])
	return hs, nil
}

// SaveHardState durably records term/vote/commit via temp file + fsync +
// atomic rename. Must be called before responding to a vote request.
func (w *WAL) SaveHardState(hs HardState) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	vb := []byte(hs.VotedFor)
	buf := make([]byte, 32+len(vb))
	binary.LittleEndian.PutUint32(buf[0:4], hsMagic)
	buf[4] = hsVersion
	binary.LittleEndian.PutUint64(buf[8:16], hs.Term)
	binary.LittleEndian.PutUint64(buf[16:24], hs.CommitIndex)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(len(vb)))
	copy(buf[32:], vb)
	binary.LittleEndian.PutUint32(buf[28:32], crc32IEEE(buf[:28]))

	tmp := w.hsPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, w.hsPath)
}

func crc32IEEE(data []byte) uint32 {
	var crc uint32 = 0xFFFFFFFF
	for _, b := range data {
		crc ^= uint32(b)
		for i := 0; i < 8; i++ {
			if crc&1 > 0 {
				crc = (crc >> 1) ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFFFFFF
}
