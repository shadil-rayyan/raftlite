package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
)

const (
	walMagic       = 0x524C5445 // "RLTE"
	walHeaderSize  = 24
	walEntryHeader = 21
	walFrameSize   = 64 * 1024
)

var ErrCorruptWAL = errors.New("corrupt WAL entry")

type WAL struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	lastIdx  uint64
	lastTerm uint64
}

func OpenWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}
	w := &WAL{f: f, path: path}
	if err := w.readHeader(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *WAL) readHeader() error {
	stat, err := w.f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return w.writeHeader(0, 0)
	}
	buf := make([]byte, walHeaderSize)
	if _, err := w.f.ReadAt(buf, 0); err != nil {
		return err
	}
	if magic := binary.LittleEndian.Uint32(buf[0:4]); magic != walMagic {
		return ErrCorruptWAL
	}
	w.lastIdx = binary.LittleEndian.Uint64(buf[8:16])
	w.lastTerm = binary.LittleEndian.Uint64(buf[16:24])
	return nil
}

func (w *WAL) writeHeader(lastIdx, lastTerm uint64) error {
	buf := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint32(buf[0:4], walMagic)
	binary.LittleEndian.PutUint64(buf[8:16], lastIdx)
	binary.LittleEndian.PutUint64(buf[16:24], lastTerm)
	if _, err := w.f.WriteAt(buf, 0); err != nil {
		return err
	}
	return nil
}

func (w *WAL) Append(entry LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendRaw(entry)
}

func (w *WAL) Scan(fromIndex uint64) ([]LogEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	stat, err := w.f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() <= walHeaderSize {
		return nil, nil
	}
	body := stat.Size() - walHeaderSize
	data := make([]byte, body)
	if _, err := w.f.ReadAt(data, walHeaderSize); err != nil {
		return nil, err
	}
	var entries []LogEntry
	pos := int64(0)
	for pos < body {
		if pos+walEntryHeader > body {
			break
		}
		idx := binary.LittleEndian.Uint64(data[pos : pos+8])
		term := binary.LittleEndian.Uint64(data[pos+8 : pos+16])
		etyp := data[pos+16]
		dlen := binary.LittleEndian.Uint32(data[pos+17 : pos+21])
		pos += walEntryHeader
		if int64(dlen) > body-pos {
			return nil, ErrCorruptWAL
		}
		if idx >= fromIndex {
			entry := LogEntry{Index: idx, Term: term, Type: etyp, Data: make([]byte, dlen)}
			copy(entry.Data, data[pos:pos+int64(dlen)])
			got := crc32IEEE(entry.Data)
			want := binary.LittleEndian.Uint32(data[pos+int64(dlen) : pos+int64(dlen)+4])
			if got != want {
				return nil, ErrCorruptWAL
			}
			entries = append(entries, entry)
		}
		pos += int64(dlen) + 4
	}
	return entries, nil
}

func (w *WAL) LastIndex() (uint64, error) { return w.lastIdx, nil }

func (w *WAL) LastTerm() (uint64, error) { return w.lastTerm, nil }

func (w *WAL) Truncate(toIndex uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries, err := w.scanAll()
	if err != nil {
		return err
	}
	var keep []LogEntry
	for _, e := range entries {
		if e.Index <= toIndex {
			keep = append(keep, e)
		}
	}
	return w.rewrite(keep)
}

func (w *WAL) Sync() error { return w.f.Sync() }

func (w *WAL) Close() error { return w.f.Close() }

func (w *WAL) scanAll() ([]LogEntry, error) {
	stat, err := w.f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() <= walHeaderSize {
		return nil, nil
	}
	body := stat.Size() - walHeaderSize
	data := make([]byte, body)
	if _, err := w.f.ReadAt(data, walHeaderSize); err != nil {
		return nil, err
	}
	return w.parseEntries(data)
}

func (w *WAL) parseEntries(data []byte) ([]LogEntry, error) {
	var entries []LogEntry
	pos := int64(0)
	body := int64(len(data))
	for pos+walEntryHeader <= body {
		idx := binary.LittleEndian.Uint64(data[pos : pos+8])
		term := binary.LittleEndian.Uint64(data[pos+8 : pos+16])
		etyp := data[pos+16]
		dlen := binary.LittleEndian.Uint32(data[pos+17 : pos+21])
		pos += walEntryHeader
		if int64(dlen) > body-pos-4 {
			return nil, ErrCorruptWAL
		}
		entry := LogEntry{Index: idx, Term: term, Type: etyp, Data: make([]byte, dlen)}
		copy(entry.Data, data[pos:pos+int64(dlen)])
		pos += int64(dlen)
		got := crc32IEEE(entry.Data)
		want := binary.LittleEndian.Uint32(data[pos : pos+4])
		if got != want {
			return nil, ErrCorruptWAL
		}
		pos += 4
		entries = append(entries, entry)
	}
	return entries, nil
}

func (w *WAL) rewrite(entries []LogEntry) error {
	if err := w.f.Truncate(walHeaderSize); err != nil {
		return err
	}
	_, err := w.f.Seek(walHeaderSize, 0)
	if err != nil {
		return err
	}
	w.lastIdx = 0
	w.lastTerm = 0
	for _, e := range entries {
		if err := w.appendRawUnsafe(e); err != nil {
			return err
		}
	}
	return w.writeHeader(w.lastIdx, w.lastTerm)
}

func (w *WAL) appendRaw(entry LogEntry) error {
	if err := w.appendRawUnsafe(entry); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	return w.writeHeader(w.lastIdx, w.lastTerm)
}

func (w *WAL) appendRawUnsafe(entry LogEntry) error {
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
	binary.LittleEndian.PutUint32(frame[walEntryHeader+len(data):walEntryHeader+len(data)+4], crc32IEEE(data))
	if _, err := w.f.Seek(0, 2); err != nil {
		return err
	}
	if _, err := w.f.Write(frame); err != nil {
		return err
	}
	w.lastIdx = entry.Index
	w.lastTerm = entry.Term
	return nil
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
