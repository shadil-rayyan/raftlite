package consensus

import (
	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

type JointConfig struct {
	OldPeers []string
	NewPeers []string
	Phase    int
}

func (n *Node) ProposeJointConfig(newPeers []string) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return 0, errNotLeader
	}

	oldPeers := n.peers

	jointPeers := mergePeers(oldPeers, newPeers)
	entry1 := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_CONFIG,
		Data:  []byte("joint:" + joinConfigStrings(jointPeers)),
	}
	n.log = append(n.log, entry1)
	n.storage.Append(transport.LogEntry{
		Index: entry1.Index, Term: entry1.Term,
		Type: byte(entry1.Type), Data: entry1.Data,
	})
	n.broadcastAppendEntries()

	n.commitIndex = entry1.Index
	n.applyCommitted()
	n.peers = jointPeers

	entry2 := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_CONFIG,
		Data:  []byte(joinConfigStrings(newPeers)),
	}
	n.log = append(n.log, entry2)
	n.storage.Append(transport.LogEntry{
		Index: entry2.Index, Term: entry2.Term,
		Type: byte(entry2.Type), Data: entry2.Data,
	})
	n.broadcastAppendEntries()

	n.commitIndex = entry2.Index
	n.applyCommitted()
	n.peers = newPeers

	for _, peer := range n.peers {
		if n.nextIndex[peer] == 0 {
			n.nextIndex[peer] = n.log[len(n.log)-1].Index + 1
		}
	}

	return entry2.Index, nil
}

func (n *Node) IsInJointConfig() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.peers) > len(dedupePeers(n.peers))
}

func mergePeers(a, b []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range a {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range b {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func dedupePeers(peers []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range peers {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
