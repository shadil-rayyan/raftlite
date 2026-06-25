package consensus

import (
	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

func (n *Node) broadcastAppendEntries() {
	lastIdx := n.log[len(n.log)-1].Index

	for _, peer := range n.peers {
		if peer == n.id {
			continue
		}
		prevLogIndex := n.nextIndex[peer] - 1
		if prevLogIndex >= uint64(len(n.log)) {
			prevLogIndex = uint64(len(n.log) - 1)
		}
		prevLogTerm := n.log[prevLogIndex].Term

		var entries []*raftpb.LogEntry
		for i := n.nextIndex[peer]; i <= lastIdx; i++ {
			e := n.log[i]
			entries = append(entries, &raftpb.LogEntry{
				Index: e.Index, Term: e.Term,
				Type: e.Type, Data: e.Data,
			})
		}

		req := &raftpb.AppendEntriesRequest{
			Term:         n.currentTerm,
			LeaderId:     []byte(n.id),
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevLogTerm,
			Entries:      entries,
			LeaderCommit: n.commitIndex,
		}
		// ponytail: Phase 3 gRPC sends; for now just store
		_ = req
	}
}

func (n *Node) HandleAppendEntries(req *raftpb.AppendEntriesRequest) *raftpb.AppendEntriesResponse {
	n.mu.Lock()
	defer n.mu.Unlock()

	resp := &raftpb.AppendEntriesResponse{Term: n.currentTerm}

	if req.Term < n.currentTerm {
		resp.Success = false
		return resp
	}

	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.state = Follower
		n.votedFor = ""
	}

	n.leaderID = string(req.LeaderId)
	n.resetElectionTimer()

	if req.PrevLogIndex >= uint64(len(n.log)) {
		resp.Success = false
		resp.ConflictIndex = uint64(len(n.log) - 1)
		resp.ConflictTerm = n.log[len(n.log)-1].Term
		return resp
	}

	if n.log[req.PrevLogIndex].Term != req.PrevLogTerm {
		resp.Success = false
		resp.ConflictTerm = n.log[req.PrevLogIndex].Term
		for i := req.PrevLogIndex; i > 0; i-- {
			if n.log[i].Term == resp.ConflictTerm {
				resp.ConflictIndex = i
				break
			}
		}
		return resp
	}

	for i, entry := range req.Entries {
		idx := req.PrevLogIndex + 1 + uint64(i)
		if idx >= uint64(len(n.log)) {
			n.log = append(n.log, LogEntry{
				Index: entry.Index, Term: entry.Term,
				Type: entry.Type, Data: entry.Data,
			})
			n.storage.Append(transport.LogEntry{
				Index: entry.Index, Term: entry.Term,
				Type: byte(entry.Type), Data: entry.Data,
			})
		} else if n.log[idx].Term != entry.Term {
			n.log = n.log[:idx]
			n.storage.Truncate(idx - 1)
			n.log = append(n.log, LogEntry{
				Index: entry.Index, Term: entry.Term,
				Type: entry.Type, Data: entry.Data,
			})
			n.storage.Append(transport.LogEntry{
				Index: entry.Index, Term: entry.Term,
				Type: byte(entry.Type), Data: entry.Data,
			})
		}
	}

	if req.LeaderCommit > n.commitIndex {
		n.commitIndex = min(req.LeaderCommit, uint64(len(n.log)-1))
		n.applyCommitted()
	}

	resp.Success = true
	resp.Term = n.currentTerm
	return resp
}

func (n *Node) applyCommitted() {
	for n.lastApplied < n.commitIndex {
		n.lastApplied++
		entry := n.log[n.lastApplied]
		if entry.Type == raftpb.LogEntry_COMMAND {
			n.fsm.Apply(entry.Data)
		}
	}
}

func (n *Node) HandleAppendEntriesResponse(peer string, resp *raftpb.AppendEntriesResponse) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.state != Leader {
		return
	}

	if resp.Term > n.currentTerm {
		n.currentTerm = resp.Term
		n.state = Follower
		n.votedFor = ""
		n.resetElectionTimer()
		return
	}

	if resp.Success {
		lastIdx := n.log[len(n.log)-1].Index
		n.matchIndex[peer] = lastIdx
		n.nextIndex[peer] = lastIdx + 1
		n.updateCommitIndex()
	} else {
		if resp.ConflictTerm > 0 {
			lastIdxInTerm := uint64(0)
			for i := len(n.log) - 1; i > 0; i-- {
				if n.log[i].Term == resp.ConflictTerm {
					lastIdxInTerm = n.log[i].Index
					break
				}
			}
			if lastIdxInTerm > 0 {
				n.nextIndex[peer] = lastIdxInTerm
			} else {
				n.nextIndex[peer] = resp.ConflictIndex
			}
		} else {
			n.nextIndex[peer] = resp.ConflictIndex + 1
		}
	}
}

func (n *Node) updateCommitIndex() {
	for i := n.commitIndex + 1; i < uint64(len(n.log)); i++ {
		if n.log[i].Term != n.currentTerm {
			continue
		}
		count := 0
		for _, peer := range n.peers {
			if n.matchIndex[peer] >= i {
				count++
			}
		}
		if count > len(n.peers)/2 {
			n.commitIndex = i
			n.applyCommitted()
		}
	}
}
