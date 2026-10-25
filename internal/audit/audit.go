// Package audit writes security-relevant administrative events as JSON
// lines (FR-031, SEC-008). Records capture the actor, operation, target,
// and the TRUE outcome — an uncertain or rejected write is never recorded
// as committed. Secrets are never logged.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Entry is one audited administrative operation.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id"`
	Actor     string    `json:"actor"`
	Operation string    `json:"operation"`
	Target    string    `json:"target,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Outcome   string    `json:"outcome"` // committed | rejected | uncertain
	RaftIndex uint64    `json:"raft_index,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Logger appends audit entries. Safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	enc *json.Encoder
	f   *os.File // non-nil when we own the file
}

// New creates a logger writing to w.
func New(w io.Writer) *Logger {
	return &Logger{enc: json.NewEncoder(w)}
}

// OpenFile appends to the JSON-lines file at path, creating it if needed.
// The file is opened in append mode and unbuffered (fsync policy is left to
// the filesystem; audit is tamper-evidence, not a durability guarantee).
func OpenFile(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &Logger{enc: json.NewEncoder(f), f: f}, nil
}

// DefaultPath returns the audit log path from RAFTLITE_AUDIT_LOG, or
// empty (audit disabled) when unset.
func DefaultPath() string { return os.Getenv("RAFTLITE_AUDIT_LOG") }

// Record writes one entry. A nil receiver is a valid no-op so callers need
// no nil checks.
func (l *Logger) Record(e Entry) {
	if l == nil || l.enc == nil {
		return // disabled logger (e.g. no RAFTLITE_AUDIT_LOG configured)
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.enc.Encode(e)
}

// Close closes the underlying file, if any.
func (l *Logger) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
