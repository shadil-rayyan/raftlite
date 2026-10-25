package consensus

import (
	"encoding/json"
	"fmt"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// Membership uses single-server changes (Raft dissertation §4.1): exactly
// one voter is added or removed per committed CONFIG entry. A new node is
// expected to catch up via snapshot/log before it is counted toward quorum
// by virtue of being in the config; removing a node shrinks the quorum only
// after the removal entry commits under the old quorum.
//
// This replaces the v0.1 joint-consensus implementation, which committed
// configuration entries without a quorum — a safety violation.

// configCommand is the serialized CONFIG payload.
type configCommand struct {
	Peers []string `json:"peers"`
}

// ProposeAddVoter adds one voting member through the normal commit path.
func (n *Node) ProposeAddVoter(id string) (uint64, error) {
	return n.proposeMembershipChange(func(peers []string) ([]string, error) {
		for _, p := range peers {
			if p == id {
				return nil, fmt.Errorf("voter %q already a member", id)
			}
		}
		return append(append([]string(nil), peers...), id), nil
	})
}

// ProposeRemoveVoter removes one voting member through the normal commit
// path. Removing the last voter is rejected.
func (n *Node) ProposeRemoveVoter(id string) (uint64, error) {
	return n.proposeMembershipChange(func(peers []string) ([]string, error) {
		out := make([]string, 0, len(peers))
		found := false
		for _, p := range peers {
			if p == id {
				found = true
				continue
			}
			out = append(out, p)
		}
		if !found {
			return nil, fmt.Errorf("voter %q is not a member", id)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("refusing to remove the last voter")
		}
		return out, nil
	})
}

func (n *Node) proposeMembershipChange(transform func([]string) ([]string, error)) (uint64, error) {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return 0, ErrNotLeader
	}
	newPeers, err := transform(n.peers)
	if err != nil {
		n.mu.Unlock()
		return 0, err
	}
	payload, err := json.Marshal(configCommand{Peers: newPeers})
	if err != nil {
		n.mu.Unlock()
		return 0, err
	}
	entry := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_CONFIG,
		Data:  payload,
	}
	n.log = append(n.log, entry)
	if err := n.storage.Append(transport.LogEntry{
		Index: entry.Index, Term: entry.Term,
		Type: byte(entry.Type), Data: entry.Data,
	}); err != nil {
		n.log = n.log[:len(n.log)-1]
		n.mu.Unlock()
		return 0, err
	}
	n.matchIndex[n.id] = entry.Index
	n.broadcastAppendEntriesLocked()
	n.updateCommitIndexLocked()
	committed := n.commitIndex >= entry.Index
	if committed {
		n.persistCommitLocked()
	}
	n.mu.Unlock()
	if !committed {
		return entry.Index, ErrNoQuorum
	}
	return entry.Index, nil
}

// applyConfigEntry applies a committed CONFIG entry to membership. Called
// from applyCommitted under n.mu, so membership changes apply in log order
// exactly once per node.
func (n *Node) applyConfigEntry(data []byte) {
	var cmd configCommand
	if err := json.Unmarshal(data, &cmd); err != nil || len(cmd.Peers) == 0 {
		return // malformed config: ignore deterministically on all nodes
	}
	n.peers = cmd.Peers
	// Initialize replication bookkeeping for any new member.
	last := n.log[len(n.log)-1].Index
	for _, p := range n.peers {
		if _, ok := n.nextIndex[p]; !ok {
			n.nextIndex[p] = last + 1
		}
		if _, ok := n.matchIndex[p]; !ok {
			n.matchIndex[p] = 0
		}
	}
}

// CurrentPeers returns the active voting configuration.
func (n *Node) CurrentPeers() []string { return n.Peers() }
