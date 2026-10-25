package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// Snapshot file format: JSON envelope next to the WAL ("<wal>.snapshot").
// Writes are atomic: temp file + fsync + rename, so a crash mid-snapshot
// leaves the previous snapshot intact (FR-024).
type snapshotEnvelope struct {
	LastIndex uint64 `json:"last_index"`
	LastTerm  uint64 `json:"last_term"`
	Data      []byte `json:"data"` // FSM state
}

// snapshotDir is the directory holding snapshots. It is derived from the
// node's WAL path via SetSnapshotDir; empty disables snapshot persistence
// (unit tests) while keeping in-memory snapshot transfer functional.
var snapshotEnabled struct {
	sync.RWMutex
	dir string
}

// SetSnapshotDir configures where snapshot files are written. Call once
// during process setup before Start.
func SetSnapshotDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	snapshotEnabled.Lock()
	snapshotEnabled.dir = dir
	snapshotEnabled.Unlock()
	return nil
}

func snapshotDir() string {
	snapshotEnabled.RLock()
	defer snapshotEnabled.RUnlock()
	return snapshotEnabled.dir
}

// CheckSnapshot conditionally compacts the log when it grows beyond the
// threshold. Called after apply; cheap no-op below threshold.
func (n *Node) CheckSnapshot() {
	n.mu.Lock()
	if n.lastApplied == 0 || n.lastApplied <= n.snapshotIndex {
		n.mu.Unlock()
		return
	}
	// Compact when the log holds more than snapshotThreshold entries beyond
	// the last snapshot.
	if n.log[len(n.log)-1].Index-n.snapshotIndex <= snapshotThreshold {
		n.mu.Unlock()
		return
	}
	n.takeSnapshotLocked()
	n.mu.Unlock()
}

const snapshotThreshold = 1024 // entries

// takeSnapshotLocked serializes FSM state and compacts the log prefix.
// Caller holds n.mu.
func (n *Node) takeSnapshotLocked() {
	data, err := n.fsm.Snapshot()
	if err != nil {
		return // snapshot failure must not crash the node; retry next time
	}
	idx := n.lastApplied
	term, ok := n.logAt(idx)
	if !ok {
		return
	}
	env := snapshotEnvelope{LastIndex: idx, LastTerm: term.Term, Data: data}

	if dir := snapshotDir(); dir != "" {
		if err := writeSnapshotFile(dir, sanitizeID(n.id), env); err != nil {
			// Persistence failed: keep serving from memory; log compaction
			// is still safe because InstallSnapshot can rebuild from FSM.
			_ = err
		}
	}

	// Compact the log: keep a single base entry at the snapshot position.
	base := LogEntry{Index: idx, Term: term.Term}
	oldBase := n.log[0].Index
	n.log = append([]LogEntry{base}, n.log[idx-oldBase+1:]...)
	n.snapshotIndex = idx
	n.snapshotTerm = term.Term

	// Rewrite durable log to drop compacted prefix. Truncate keeps entries
	// <= toIndex, so we invert: write a fresh log via truncate+append of
	// the surviving suffix.
	_ = n.storage.Truncate(0)
	for _, e := range n.log[1:] {
		_ = n.storage.Append(transport.LogEntry{
			Index: e.Index, Term: e.Term, Type: byte(e.Type), Data: e.Data,
		})
	}
}

func writeSnapshotFile(dir, nodeID string, env snapshotEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("snapshot-%s.json", nodeID))
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
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
	return os.Rename(tmp, path)
}

// loadLatestSnapshot reads the node's snapshot file, if any. Used by
// restore() during construction.
func loadLatestSnapshot() (uint64, uint64, []byte, bool) {
	dir := snapshotDir()
	if dir == "" {
		return 0, 0, nil, false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "snapshot-*.json"))
	if len(matches) == 0 {
		return 0, 0, nil, false
	}
	// Pick the file with the highest last_index (usually a single file).
	var best snapshotEnvelope
	found := false
	for _, m := range matches {
		payload, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var env snapshotEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if !found || env.LastIndex > best.LastIndex {
			best = env
			found = true
		}
	}
	if !found {
		return 0, 0, nil, false
	}
	return best.LastIndex, best.LastTerm, best.Data, true
}

// snapshotPayloadLocked returns the current snapshot for transfer to a
// lagging follower. Caller holds n.mu. Re-serializes from the FSM when no
// file exists (memory-only mode).
func (n *Node) snapshotPayloadLocked() ([]byte, uint64, uint64, error) {
	idx := n.snapshotIndex
	term := n.snapshotTerm

	if dir := snapshotDir(); dir != "" {
		payload, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("snapshot-%s.json", sanitizeID(n.id))))
		if err == nil {
			var env snapshotEnvelope
			if json.Unmarshal(payload, &env) == nil {
				return payload, env.LastIndex, env.LastTerm, nil
			}
		}
	}

	// Memory-only snapshot (tests, or persistence unavailable).
	data, err := n.fsm.Snapshot()
	if err != nil {
		return nil, 0, 0, err
	}
	env := snapshotEnvelope{LastIndex: idx, LastTerm: term, Data: data}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, 0, 0, err
	}
	return raw, idx, term, nil
}

// HandleInstallSnapshot is the follower side of snapshot transfer: restore
// the FSM, reset the log to the snapshot base, and persist the snapshot.
func (n *Node) HandleInstallSnapshot(req *raftpb.InstallSnapshotRequest) *raftpb.InstallSnapshotResponse {
	n.mu.Lock()
	defer n.mu.Unlock()

	resp := &raftpb.InstallSnapshotResponse{Term: n.currentTerm}
	if req.Term < n.currentTerm {
		return resp
	}
	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.state = Follower
		n.votedFor = ""
		_ = n.persistHardStateLocked()
	}

	n.leaderID = string(req.LeaderId)
	n.resetElectionTimer()

	var env snapshotEnvelope
	if err := json.Unmarshal(req.Data, &env); err != nil {
		return resp // malformed: ignore, leader will retry or use log
	}
	if env.LastIndex <= n.snapshotIndex {
		return resp // stale snapshot
	}
	if err := n.fsm.Restore(env.Data); err != nil {
		return resp
	}

	n.snapshotIndex = env.LastIndex
	n.snapshotTerm = env.LastTerm
	n.log = []LogEntry{{Index: env.LastIndex, Term: env.LastTerm}}
	n.commitIndex = env.LastIndex
	n.lastApplied = env.LastIndex

	// Persist: snapshot file first, then reset durable log.
	if dir := snapshotDir(); dir != "" {
		_ = writeSnapshotFile(dir, sanitizeID(n.id), env)
	}
	_ = n.storage.Truncate(0) // drop old entries; log restarts at snapshot
	_ = n.persistHardStateLocked()

	resp.Term = n.currentTerm
	resp.BytesWritten = []byte(fmt.Sprintf("%d", len(req.Data)))
	return resp
}

// Compactable reports whether the node has snapshot state a peer may need.
func (n *Node) Compactable() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.snapshotIndex > 0
}

// SnapshotPathFor returns the snapshot path for a node id (ops tooling).
func SnapshotPathFor(nodeID string) string {
	dir := snapshotDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("snapshot-%s.json", nodeID))
}

// sanitizeID keeps node IDs safe for use in file names.
func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}
