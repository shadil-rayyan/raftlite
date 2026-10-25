package consensus

import (
	"context"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// broadcastAppendEntriesLocked builds per-peer AppendEntries requests while
// the caller holds n.mu, then sends them after releasing it. Sending under
// the lock would serialize RPCs behind every state read.
func (n *Node) broadcastAppendEntriesLocked() {
	if n.state != Leader || n.transport == nil {
		return
	}
	term := n.currentTerm
	leaderID := n.id
	commit := n.commitIndex
	type job struct {
		peer string
		req  *raftpb.AppendEntriesRequest
	}
	jobs := make([]job, 0, len(n.peers))
	snapIndex := n.snapshotIndex

	for _, peer := range n.peers {
		if peer == n.id {
			continue
		}
		next := n.nextIndex[peer]

		// Follower is behind the compaction point: ship a snapshot instead.
		if next <= snapIndex && snapIndex > 0 {
			data, _, _, err := n.snapshotPayloadLocked()
			if err != nil || len(data) == 0 {
				continue
			}
			req := &raftpb.InstallSnapshotRequest{
				Term:              term,
				LeaderId:          []byte(leaderID),
				LastIncludedIndex: snapIndex,
				LastIncludedTerm:  n.snapshotTerm,
				Data:              data,
			}
			tr := n.transport
			go func(peer string, req *raftpb.InstallSnapshotRequest) {
				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				defer cancel()
				resp, err := tr.SendInstallSnapshot(ctx, peer, req)
				if err != nil || resp == nil {
					return
				}
				n.handleInstallSnapshotResponse(peer, req.LastIncludedIndex, resp)
			}(peer, req)
			continue
		}

		prevLogIndex := next - 1
		if lastIdx := n.log[len(n.log)-1].Index; prevLogIndex > lastIdx {
			prevLogIndex = lastIdx
		}
		prevEntry, ok := n.logAt(prevLogIndex)
		if !ok {
			// Should not happen with correct nextIndex; fall back to base.
			prevEntry = &n.log[0]
			prevLogIndex = prevEntry.Index
		}

		var entries []*raftpb.LogEntry
		for _, e := range n.log {
			if e.Index >= next && e.Index > n.snapshotIndex {
				entries = append(entries, &raftpb.LogEntry{
					Index: e.Index, Term: e.Term, Type: e.Type, Data: e.Data,
				})
			}
		}

		req := &raftpb.AppendEntriesRequest{
			Term:         term,
			LeaderId:     []byte(leaderID),
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevEntry.Term,
			Entries:      entries,
			LeaderCommit: commit,
		}
		jobs = append(jobs, job{peer: peer, req: req})
	}

	if len(jobs) == 0 {
		return
	}
	tr := n.transport
	go func() {
		for _, j := range jobs {
			go func(peer string, req *raftpb.AppendEntriesRequest) {
				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				defer cancel()
				resp, err := tr.SendAppendEntries(ctx, peer, req)
				if err != nil || resp == nil {
					return
				}
				sentUpTo := req.PrevLogIndex + uint64(len(req.Entries))
				n.HandleAppendEntriesResponse(peer, sentUpTo, resp)
			}(j.peer, j.req)
		}
	}()
}

// broadcastAppendEntries is the unlocked entry point.
func (n *Node) broadcastAppendEntries() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.broadcastAppendEntriesLocked()
}

// startHeartbeatLoop launches at most one heartbeat loop per node. Repeated
// elections must not stack duplicate senders (a step-down that is observed
// late by the running loop would otherwise race a fresh spawn).
func (n *Node) startHeartbeatLoop() {
	n.mu.Lock()
	if n.heartbeatRunning {
		n.mu.Unlock()
		return
	}
	n.heartbeatRunning = true
	n.mu.Unlock()
	go func() {
		defer func() {
			n.mu.Lock()
			n.heartbeatRunning = false
			n.mu.Unlock()
		}()
		n.heartbeatLoop()
	}()
}

func (n *Node) heartbeatLoop() {
	for {
		n.mu.Lock()
		if n.state != Leader {
			n.mu.Unlock()
			return
		}
		n.mu.Unlock()

		n.broadcastAppendEntries()

		select {
		case <-n.clock.After(time.Duration(n.heartbeatTimeoutMs) * time.Millisecond):
		case <-n.shutdownCh:
			return
		}
	}
}

// logAt returns the entry with the given index, if present in the in-memory
// log window (entries below snapshotIndex are compacted away).
func (n *Node) logAt(index uint64) (*LogEntry, bool) {
	base := n.log[0].Index
	if index < base || index > n.log[len(n.log)-1].Index {
		return nil, false
	}
	return &n.log[index-base], true
}

// HandleAppendEntries is the follower side. Returns success=false with
// conflict hints when the log consistency check fails, enabling the leader's
// fast backtracking.
func (n *Node) HandleAppendEntries(req *raftpb.AppendEntriesRequest) *raftpb.AppendEntriesResponse {
	n.mu.Lock()
	applied := false
	defer func() {
		n.mu.Unlock()
		if applied {
			// Followers compact too; without this only leaders would ever
			// snapshot and follower logs would grow without bound.
			n.CheckSnapshot()
		}
	}()

	resp := &raftpb.AppendEntriesResponse{Term: n.currentTerm}

	if req.Term < n.currentTerm {
		resp.Success = false
		return resp
	}

	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.state = Follower
		n.votedFor = ""
		_ = n.persistHardStateLocked()
	}

	// Valid leader contact: learn leadership and reset the election timer.
	n.leaderID = string(req.LeaderId)
	if n.state == Candidate {
		n.state = Follower
	}
	n.resetElectionTimer()

	// Consistency check against the leader's prevLogIndex/prevLogTerm.
	if req.PrevLogIndex < n.snapshotIndex {
		// Leader is sending entries we already compacted into a snapshot;
		// ask it to advance.
		resp.Success = false
		resp.ConflictIndex = n.snapshotIndex + 1
		return resp
	}

	if req.PrevLogIndex > n.log[len(n.log)-1].Index {
		resp.Success = false
		resp.ConflictIndex = n.log[len(n.log)-1].Index + 1
		return resp
	}

	prev, ok := n.logAt(req.PrevLogIndex)
	if !ok || prev.Term != req.PrevLogTerm {
		resp.Success = false
		if ok {
			resp.ConflictTerm = prev.Term
			// First index of the conflicting term, for fast backtracking.
			for i := req.PrevLogIndex; i > n.snapshotIndex; i-- {
				e, ok := n.logAt(i - 1)
				if !ok || e.Term != resp.ConflictTerm {
					break
				}
				resp.ConflictIndex = i - 1
			}
			if resp.ConflictIndex == 0 {
				resp.ConflictIndex = req.PrevLogIndex
			}
		} else {
			resp.ConflictIndex = n.log[len(n.log)-1].Index + 1
		}
		return resp
	}

	// Append entries, truncating any conflicting uncommitted suffix.
	for i, entry := range req.Entries {
		idx := req.PrevLogIndex + 1 + uint64(i)
		existing, exists := n.logAt(idx)
		if exists {
			if existing.Term == entry.Term {
				continue // already have it
			}
			// Conflict: truncate local suffix, then append.
			base := n.log[0].Index
			n.log = n.log[:idx-base]
			_ = n.storage.Truncate(idx - 1)
			// fall through to append below
		}
		n.log = append(n.log, LogEntry{
			Index: entry.Index, Term: entry.Term,
			Type: entry.Type, Data: entry.Data,
		})
		_ = n.storage.Append(transport.LogEntry{
			Index: entry.Index, Term: entry.Term,
			Type: byte(entry.Type), Data: entry.Data,
		})
	}

	if req.LeaderCommit > n.commitIndex {
		lastNew := req.PrevLogIndex + uint64(len(req.Entries))
		if lastNew > n.log[len(n.log)-1].Index {
			lastNew = n.log[len(n.log)-1].Index
		}
		n.commitIndex = min(req.LeaderCommit, lastNew)
		n.persistCommitLocked()
		n.applyCommitted()
		applied = true
	}

	resp.Success = true
	resp.Term = n.currentTerm
	return resp
}

// HandleAppendEntriesResponse processes a follower reply on the leader.
// `sentUpTo` is the last index the leader shipped in the request — using it
// (instead of the leader's own last index) keeps matchIndex accurate when
// multiple appends are in flight.
func (n *Node) HandleAppendEntriesResponse(peer string, sentUpTo uint64, resp *raftpb.AppendEntriesResponse) {
	n.mu.Lock()
	advanced := false
	defer func() {
		n.mu.Unlock()
		if advanced {
			n.notifyCommitWaiters()
			n.CheckSnapshot()
		}
	}()

	if n.state != Leader {
		return
	}

	if resp.Term > n.currentTerm {
		n.currentTerm = resp.Term
		n.state = Follower
		n.votedFor = ""
		_ = n.persistHardStateLocked()
		n.resetElectionTimer()
		n.reportStateLocked()
		return
	}

	if resp.Success {
		if sentUpTo > n.matchIndex[peer] {
			n.matchIndex[peer] = sentUpTo
		}
		n.nextIndex[peer] = n.matchIndex[peer] + 1
		before := n.commitIndex
		n.updateCommitIndexLocked()
		n.persistCommitLocked()
		advanced = n.commitIndex > before
		return
	}

	// Failure: back off nextIndex using the follower's conflict hints.
	if resp.ConflictTerm > 0 {
		lastIdxInTerm := uint64(0)
		for i := n.log[len(n.log)-1].Index; i > n.snapshotIndex; i-- {
			if e, ok := n.logAt(i); ok && e.Term == resp.ConflictTerm {
				lastIdxInTerm = i
				break
			}
		}
		if lastIdxInTerm > 0 {
			n.nextIndex[peer] = lastIdxInTerm + 1
		} else {
			n.nextIndex[peer] = maxOf(resp.ConflictIndex, 1)
		}
	} else {
		n.nextIndex[peer] = maxOf(resp.ConflictIndex, 1)
	}
	// Retry immediately with the corrected nextIndex.
	n.broadcastAppendEntriesLocked()
}

// handleInstallSnapshotResponse advances follower progress after a snapshot.
func (n *Node) handleInstallSnapshotResponse(peer string, snapIndex uint64, resp *raftpb.InstallSnapshotResponse) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return
	}
	if resp.Term > n.currentTerm {
		n.currentTerm = resp.Term
		n.state = Follower
		n.votedFor = ""
		_ = n.persistHardStateLocked()
		return
	}
	if snapIndex > n.matchIndex[peer] {
		n.matchIndex[peer] = snapIndex
	}
	n.nextIndex[peer] = snapIndex + 1
	n.updateCommitIndexLocked()
}

// applyCommitted applies committed entries to the FSM in order. Caller holds
// n.mu. CONFIG entries update membership (see membership.go).
func (n *Node) applyCommitted() {
	for n.lastApplied < n.commitIndex {
		n.lastApplied++
		entry, ok := n.logAt(n.lastApplied)
		if !ok {
			return
		}
		switch entry.Type {
		case raftpb.LogEntry_COMMAND:
			n.fsm.Apply(entry.Data, entry.Index)
		case raftpb.LogEntry_CONFIG:
			n.applyConfigEntry(entry.Data)
		}
	}
}

// updateCommitIndexLocked advances commitIndex by the Raft rule: an entry is
// committed when stored on a majority AND from the leader's current term
// (the Figure-8 rule — counting old-term entries alone can commit a stale
// tail). Caller holds n.mu.
func (n *Node) updateCommitIndexLocked() {
	if n.state != Leader {
		return
	}
	last := n.log[len(n.log)-1].Index
	for i := last; i > n.commitIndex; i-- {
		e, ok := n.logAt(i)
		if !ok || e.Term != n.currentTerm {
			continue
		}
		count := 0
		for _, peer := range n.peers {
			if n.matchIndex[peer] >= i {
				count++
			}
		}
		if count >= majorityOf(len(n.peers)) {
			prev := n.commitIndex
			n.commitIndex = i
			n.applyCommitted()
			if n.metrics != nil {
				n.metrics.IncCommits()
				for idx := prev + 1; idx <= i; idx++ {
					if t, ok := n.proposeTimes[idx]; ok {
						n.metrics.ObserveCommitLatency(time.Since(t).Seconds())
						delete(n.proposeTimes, idx)
					}
				}
			}
			n.reportStateLocked()
			return
		}
	}
}

// persistCommitLocked opportunistically records commitIndex in hard state.
// Failures are non-fatal: commitIndex is a recovery hint, not a safety
// requirement (unlike term/vote).
func (n *Node) persistCommitLocked() {
	_ = n.persistHardStateLocked()
}

func maxOf(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
