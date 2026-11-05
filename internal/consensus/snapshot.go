package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
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
// The ordering is a crash-safe transaction (FR-024):
//
//  1. Serialize FSM state — failure aborts, nothing changes.
//  2. Persist the snapshot file atomically (temp+fsync+rename) — failure
//     aborts: compacting the WAL without a durable snapshot would lose
//     the committed prefix on restart.
//  3. Compact the in-memory log to the base entry at the snapshot index.
//  4. Drop the durable prefix atomically (WAL.CompactPrefix) — failure is
//     safe: the full WAL is a superset that restore() accepts (it skips
//     entries <= snapshotIndex); only reclamation is delayed, and it is
//     reported through storage health.
//
// Crash windows: after (2) the durable state is old full log + new
// snapshot (safe); mid-(4) CompactPrefix publishes atomically (old full
// log or new compacted log — both safe). There is no instant where the
// snapshot is missing while the prefix is gone.
//
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
			// Never compact past an unpersisted snapshot: the prefix must
			// stay in the WAL so a restart can still rebuild it.
			n.noteStorageFailureLocked("snapshot_write", err)
			return
		}
		n.noteStorageSuccessLocked()
	}

	// Compact the in-memory log: keep a single base entry at the snapshot
	// position.
	base := LogEntry{Index: idx, Term: term.Term}
	oldBase := n.log[0].Index
	n.log = append([]LogEntry{base}, n.log[idx-oldBase+1:]...)
	n.snapshotIndex = idx
	n.snapshotTerm = term.Term
	n.recordEvent(term.Term, EvSnapshotTaken, "index="+itoa(idx))

	// Reclaim the durable prefix atomically. Failure is non-fatal (see
	// step 4 above) but must not be silent.
	if err := n.storage.CompactPrefix(idx); err != nil {
		n.noteStorageFailureLocked("wal_compact", err)
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
		n.recordEvent(req.Term, EvTermBumped, "higher term seen in install snapshot; stepped down")
		_ = n.persistHardStateLocked()
		n.failPendingReadsLocked(ErrNotLeader)
	}

	if leader := string(req.LeaderId); leader != "" && leader != n.leaderID {
		n.leaderID = leader
		n.recordEvent(req.Term, EvLeaderObserved, "leader is "+leader+" (via snapshot)")
	}
	n.resetElectionTimer()

	var env snapshotEnvelope
	if err := json.Unmarshal(req.Data, &env); err != nil {
		return resp // malformed: ignore, leader will retry or use log
	}
	if env.LastIndex <= n.snapshotIndex {
		return resp // stale snapshot
	}

	// Durability first, in crash-safe order (FR-024). Every failure below
	// returns WITHOUT acknowledging and WITHOUT mutating memory, so a
	// retried install always starts from a consistent state:
	//
	//  1. Persist the snapshot file (atomic temp+fsync+rename).
	//  2. Drop the durable log past the snapshot point — Truncate keeps
	//     entries <= toIndex, so this removes the divergent tail while a
	//     follower that is merely behind is a no-op (its tail is already
	//     below the snapshot index).
	//  3. Restore the FSM (all-or-nothing) and adopt the new indexes.
	//
	// Crash between any steps leaves old log + new snapshot, or dropped
	// tail + new snapshot — both safe, because restore() prefers the
	// durable snapshot and skips the compacted prefix.
	if dir := snapshotDir(); dir != "" {
		if err := writeSnapshotFile(dir, sanitizeID(n.id), env); err != nil {
			n.noteStorageFailureLocked("install_snapshot_write", err)
			return resp // not applied, not acknowledged; leader re-sends
		}
	}
	if err := n.storage.Truncate(env.LastIndex); err != nil {
		// The divergent tail could not be dropped: adopting the snapshot
		// in memory now would make this node acknowledge entries whose
		// durable copies contradict them.
		n.noteStorageFailureLocked("install_snapshot_truncate", err)
		return resp // old log intact (or tail still present); leader re-sends
	}
	n.noteStorageSuccessLocked()

	// Durable state is in place: adopt it in memory. Engine.Restore is
	// all-or-nothing (unmarshal, then assign), so a failure here leaves
	// the old state and can be retried.
	if err := n.fsm.Restore(env.Data); err != nil {
		n.noteStorageFailureLocked("install_snapshot_restore", err)
		return resp
	}
	n.snapshotIndex = env.LastIndex
	n.snapshotTerm = env.LastTerm
	n.log = []LogEntry{{Index: env.LastIndex, Term: env.LastTerm}}
	n.commitIndex = env.LastIndex
	n.lastApplied = env.LastIndex

	// Refresh the hard-state commit hint. Best-effort: the hint only
	// accelerates recovery, correctness does not depend on it.
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
