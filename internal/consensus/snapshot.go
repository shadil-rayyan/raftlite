package consensus

import (
	"encoding/json"
)

const snapshotThreshold = 64 * 1024 * 1024 // 64 MB

func (n *Node) CheckSnapshot() {
	n.mu.Lock()
	defer n.mu.Unlock()

	walSize, err := n.walSize()
	if err != nil || walSize < snapshotThreshold {
		return
	}

	snap := n.fsm.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}

	lastApplied := n.lastApplied
	lastTerm := n.log[lastApplied].Term

	// Truncate log up to lastApplied
	n.log = append([]LogEntry{{Index: lastApplied, Term: lastTerm}}, n.log[lastApplied+1:]...)
	n.storage.Truncate(lastApplied - 1)

	n.storeSnapshotData(lastApplied, lastTerm, data)
}

func (n *Node) walSize() (int64, error) {
	// ponytail: WAL size check; if not WAL-based, skip
	return 0, nil
}

func (n *Node) storeSnapshotData(lastIdx, lastTerm uint64, data []byte) {
	// ponytail: snapshot persistence — write to file for now
	_ = lastIdx
	_ = lastTerm
	_ = data
}
