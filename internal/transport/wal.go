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

	hsMagic   = 0x524C4853 // "RLHS"
	hsVersion = 1
)

// walSegmentSize is the rotation threshold. A var so tests can force
// rotation without writing 8 MiB.
var walSegmentSize int64 = 8 << 20 // rotate after 8 MiB

var ErrCorruptWAL = errors.New("corrupt WAL entry")

type segment struct {
	index int    // 0 for base path, 1.. for rotated
	path  string
}

// walFile is the file surface the WAL needs. *os.File implements it; tests
// substitute a failing file to exercise the rollback paths (write/sync
// errors) that a healthy disk can never produce.
type walFile interface {
	Seek(offset int64, whence int) (int64, error)
	Write(p []byte) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Sync() error
	Truncate(size int64) error
	Stat() (os.FileInfo, error)
	Close() error
}

type WAL struct {
	mu       sync.Mutex
	basePath string
	segs     []segment
	f        walFile // active (last) segment
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

	// Prune stale rotated segments left behind by an interrupted prefix
	// compaction (CompactPrefix): the compaction atomically replaces the
	// base segment with the surviving suffix, THEN removes the old rotated
	// segments. A crash between those steps leaves rotated segments that
	// predate the compaction — scanning them would resurrect duplicate
	// indexes beside the compacted base. The base's shape is the
	// signature: a never-compacted base always starts at record index 1
	// and is never empty while rotated segments exist. The pruned prefix
	// is covered by the durable snapshot the compaction was taken for.
	if w.baseIsCompacted() {
		for i := len(w.segs) - 1; i >= 1; i-- {
			_ = os.Remove(w.segs[i].path)
		}
		w.segs = w.segs[:1]
	}

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

func writeWALHeader(f walFile, lastIdx, lastTerm uint64) error {
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

// baseIsCompacted reports whether the base segment starts mid-log (or is
// empty) while rotated segments exist — the signature of an interrupted
// prefix compaction. See the prune in openSegments.
func (w *WAL) baseIsCompacted() bool {
	if len(w.segs) <= 1 {
		return false // nothing to prune
	}
	f, err := os.Open(w.basePath)
	if err != nil {
		return false // missing base: keep rotated segments (they hold the log)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	if stat.Size() <= walHeaderSize {
		// Empty base body with rotated segments present never happens in
		// normal operation (rotation preserves the base body), so this is
		// a compaction whose suffix was empty.
		return true
	}
	var hdr [walEntryHeader]byte
	if _, err := f.ReadAt(hdr[:], walHeaderSize); err != nil {
		return false
	}
	return binary.LittleEndian.Uint64(hdr[0:8]) > 1
}

// Append durably appends one entry and advances the recorded position.
func (w *WAL) Append(entry LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendRaw(entry)
}

func (w *WAL) appendRaw(entry LogEntry) error {
	if w.f == nil {
		// A previous failure left no active handle (failed truncate or
		// reopen); self-heal before writing.
		if err := w.reopenActiveLocked(); err != nil {
			return err
		}
	}
	if err := w.rotateIfNeeded(len(entry.Data) + walEntryHeader + 4); err != nil {
		return err
	}
	frame := encodeRecord(entry)
	// Capture the append point so a failed write can be rolled back to it.
	start, err := w.f.Seek(0, 2) // 2 = io.SeekEnd
	if err != nil {
		return err
	}
	if _, err := w.f.Write(frame); err != nil {
		// Roll back any partially-written bytes. A failed record left in
		// the middle of the segment would be buried by the next successful
		// append, and the scan would stop there on restart — silently
		// discarding every record written after it (FR-022 applied to
		// runtime failures, not just crashes).
		_ = w.f.Truncate(start)
		_, _ = w.f.Seek(0, 2)
		return err
	}
	if err := w.f.Sync(); err != nil {
		// Bytes may exist but are not durable. Roll back so the file agrees
		// with the in-memory log that is rejecting this append — the node
		// must never think an entry is missing from disk while the disk
		// holds a copy it did not acknowledge.
		_ = w.f.Truncate(start)
		_, _ = w.f.Seek(0, 2)
		return err
	}
	w.lastIdx, w.lastTerm = entry.Index, entry.Term
	// The segment header is advisory: open re-derives the true tail by
	// scanning the body, and a crash between record-fsync and header
	// rewrite already leaves it one entry behind (see truncateTornTail).
	// The record above IS durable, so a header-write failure must not
	// turn a successful append into a reported one; the next append
	// rewrites the header anyway.
	_ = writeWALHeader(w.f, w.lastIdx, w.lastTerm)
	return nil
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
	// O_TRUNC: if a stale file at this index survived an interrupted
	// compaction prune, it must not keep its old body behind the new
	// header (indexes would not be contiguous).
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
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
		// Corruption must not masquerade as a successful truncate: the
		// caller would then diverge its in-memory log from a disk we never
		// touched. Surface it so the node flags storage unhealthy and the
		// leader gets a transient (hint-less) NACK instead of silence.
		return err
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
	if keepCount >= len(positions) {
		return nil // nothing to discard
	}
	boundary := positions[keepCount]

	segPath := w.segs[boundary.seg].path

	// Close the active handle first: it may be one of the segments about to
	// be removed, and Windows refuses to delete open files.
	old := w.f
	w.f = nil
	if err := old.Close(); err != nil {
		// Handle unusable — reopen from disk so the node keeps running
		// while still reporting the failure.
		w.reopenActiveLocked()
		return err
	}

	// Drop every segment AFTER the boundary first. Each step only removes
	// records, so a crash at any point leaves a contiguous log (either the
	// old one or a prefix of it); the reverse order could leave a hole
	// between the truncated boundary segment and the not-yet-removed ones.
	// Removal failures are retried by the stale-segment prune at open.
	for i := boundary.seg + 1; i < len(w.segs); i++ {
		_ = os.Remove(w.segs[i].path)
	}
	w.segs = w.segs[:boundary.seg+1]

	// Now cut the boundary segment at the first discarded record.
	if err := os.Truncate(segPath, boundary.off); err != nil {
		// The file was not modified; restore the append handle so the node
		// keeps operating and reports this failure through the normal
		// storage-health path instead of panicking on the next append.
		w.reopenActiveLocked()
		return err
	}

	// refresh header position from surviving records
	if keepCount > 0 {
		last := entries[keepCount-1]
		w.lastIdx, w.lastTerm = last.Index, last.Term
	} else {
		w.lastIdx, w.lastTerm = 0, 0
	}

	f, err := os.OpenFile(segPath, os.O_RDWR, 0644)
	if err != nil {
		// w.f stays nil; the next append reopens it (or reports the
		// failure through the storage-health path).
		return err
	}
	w.f = f
	if err := writeWALHeader(f, w.lastIdx, w.lastTerm); err != nil {
		return err
	}
	// Make the truncation durable before reporting success: a crash that
	// resurrects the discarded suffix would otherwise reappear beside
	// records appended after this truncate.
	return f.Sync()
}

// reopenActiveLocked re-establishes the append handle after a failure left
// w.f nil. Caller holds w.mu. Best-effort: returns the open error when the
// file cannot be reopened (the next append reports it).
func (w *WAL) reopenActiveLocked() error {
	if w.f != nil {
		return nil
	}
	if len(w.segs) == 0 {
		return errors.New("wal: no segment to reopen")
	}
	f, err := os.OpenFile(w.segs[len(w.segs)-1].path, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	w.f = f
	return nil
}

// CompactPrefix drops every record with Index <= fromIndex, keeping the
// surviving suffix durable at every instant. It backs snapshot log
// compaction, replacing the old truncate-to-empty + re-append sequence
// that had a crash window in which neither the snapshot nor the suffix
// existed on disk.
//
// Protocol: write the suffix to a temp file (fsync), atomically rename it
// over the base segment, then remove the now-stale rotated segments. A
// crash at any point leaves either the OLD full log — a safe superset;
// restore skips the compacted prefix because the snapshot is already
// durable — or the NEW base plus stale rotated segments, which the next
// open prunes (see baseIsCompacted).
func (w *WAL) CompactPrefix(fromIndex uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, _, err := w.scanAll()
	if err != nil {
		return err // includes ErrCorruptWAL: never compact past corruption
	}
	var suffix []LogEntry
	var tailIdx, tailTerm uint64
	for _, e := range entries {
		if e.Index <= fromIndex {
			tailIdx, tailTerm = e.Index, e.Term // last dropped record
			continue
		}
		suffix = append(suffix, e)
	}
	if len(suffix) == len(entries) {
		return nil // nothing to drop (also covers an empty log)
	}

	// Build the replacement base: header + surviving suffix records.
	tmp := w.basePath + ".compact.tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		f.Close()
		_ = os.Remove(tmp)
		return cause
	}
	if err := writeWALHeader(f, tailIdx, tailTerm); err != nil {
		return fail(err)
	}
	// WriteAt-style header write leaves the offset at 0; records must land
	// after it.
	if _, err := f.Seek(0, 2); err != nil {
		return fail(err)
	}
	for _, e := range suffix {
		if _, err := f.Write(encodeRecord(e)); err != nil {
			return fail(err) // temp removed; the old log is untouched
		}
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// Close the active handle before publishing/removing: it may be a
	// rotated segment that is about to be deleted (Windows refuses to
	// delete open files).
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			w.f = nil
			w.reopenActiveLocked()
			_ = os.Remove(tmp)
			return err
		}
	}
	w.f = nil

	// Atomic publish: after this point the durable log is the suffix (or,
	// if the crash came earlier, still the old full log — both safe).
	if err := os.Rename(tmp, w.basePath); err != nil {
		w.reopenActiveLocked()
		_ = os.Remove(tmp)
		return err
	}

	// Drop the stale rotated segments; failures are pruned at next open.
	for i := 1; i < len(w.segs); i++ {
		_ = os.Remove(w.segs[i].path)
	}
	w.segs = w.segs[:1]
	w.lastIdx, w.lastTerm = tailIdx, tailTerm

	f2, err := os.OpenFile(w.basePath, os.O_RDWR, 0644)
	if err != nil {
		// w.f stays nil; the next append self-heals via reopenActiveLocked.
		return err
	}
	w.f = f2
	return nil
}

func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.reopenActiveLocked(); err != nil {
			return err
		}
	}
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
