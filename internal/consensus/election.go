package consensus

import (
	"context"
	"sync"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// startElection campaigns with the current log state. Called with n.mu held
// by electionTimerLoop — releases before RPCs, re-acquires to count votes.
func (n *Node) startElection() {
	n.mu.Lock()
	if n.state == Leader {
		n.mu.Unlock()
		return
	}
	n.currentTerm++
	n.state = Candidate
	n.votedFor = n.id
	n.leaderID = ""
	term := n.currentTerm
	lastIdx := n.log[len(n.log)-1].Index
	lastTerm := n.log[len(n.log)-1].Term
	peers := append([]string(nil), n.peers...)
	tr := n.transport

	// §5.4.2: persist term + self-vote BEFORE sending any request. A crash
	// here must not allow a second vote in this term.
	if err := n.persistHardStateLocked(); err != nil {
		// Cannot campaign safely without durable vote; step back and retry
		// on the next timeout.
		n.state = Follower
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()

	if tr == nil {
		return // no transport attached (unit tests): stay candidate, retry later
	}

	// Single-voter fast path (e.g. cluster of one after removals).
	if majorityOf(len(peers)) <= 1 {
		n.winElection(term)
		return
	}

	votes := make(chan bool, len(peers))
	var inflight sync.WaitGroup
	for _, peer := range peers {
		if peer == n.id {
			continue
		}
		inflight.Add(1)
		go func(peer string) {
			defer inflight.Done()
			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			defer cancel()
			resp, err := tr.SendRequestVote(ctx, peer, &raftpb.RequestVoteRequest{
				Term:         term,
				CandidateId:  []byte(n.id),
				LastLogIndex: lastIdx,
				LastLogTerm:  lastTerm,
			})
			if err != nil || resp == nil {
				votes <- false
				return
			}
			n.handleVoteResponse(resp)
			votes <- resp.VoteGranted
		}(peer)
	}

	// Count votes as they arrive; stop at a majority.
	granted := 1
	needed := majorityOf(len(peers))
	deadline := time.After(time.Duration(2*n.electionTimeoutMs) * time.Millisecond)
	for i := 0; i < len(peers)-1; i++ {
		select {
		case v := <-votes:
			if v {
				granted++
				if granted >= needed {
					n.winElection(term)
					return
				}
			}
		case <-deadline:
			return // timed out; retry on next election timeout
		case <-n.shutdownCh:
			return
		}
	}
	if granted >= needed {
		n.winElection(term)
	}
}

func majorityOf(n int) int { return n/2 + 1 }

// winElection transitions to leader if still a candidate in `term`.
func (n *Node) winElection(term uint64) {
	n.mu.Lock()
	if n.state != Candidate || n.currentTerm != term {
		n.mu.Unlock()
		return
	}
	n.state = Leader
	n.leaderID = n.id
	last := n.log[len(n.log)-1].Index
	for _, p := range n.peers {
		if p == n.id {
			continue
		}
		n.nextIndex[p] = last + 1
		n.matchIndex[p] = 0
	}
	// Append a no-op entry in the new term (standard Raft leader behavior):
	// entries from previous terms can only commit indirectly, once a
	// current-term entry above them commits. Without this, a fresh leader
	// may never advance commitIndex over an old committed prefix.
	noop := LogEntry{
		Index: last + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_COMMAND,
		Data:  nil, // FSM ignores empty data
	}
	n.log = append(n.log, noop)
	if err := n.storage.Append(transport.LogEntry{
		Index: noop.Index, Term: noop.Term,
		Type: byte(noop.Type), Data: noop.Data,
	}); err != nil {
		// Could not persist the no-op: roll back and stay candidate; the
		// next election timeout retries. Never lead with an unpersisted log.
		n.log = n.log[:len(n.log)-1]
		n.state = Candidate
		n.mu.Unlock()
		return
	}
	n.matchIndex[n.id] = noop.Index
	n.nextIndex[n.id] = noop.Index + 1
	// Single-voter quorum commits the no-op (and any prior-term prefix)
	// immediately; multi-node waits for follower acks.
	n.updateCommitIndexLocked()
	n.persistCommitLocked()
	if n.metrics != nil {
		n.metrics.IncElections()
	}
	n.reportStateLocked()
	committed := n.commitIndex >= noop.Index
	n.mu.Unlock()
	if committed {
		n.notifyCommitWaiters()
	}

	n.startHeartbeatLoop()
	// Replicate immediately so followers learn of the new leader and any
	// uncommitted tail from previous terms gets resolved.
	n.broadcastAppendEntries()}

// handleVoteResponse processes a vote reply, stepping down on a higher term.
func (n *Node) handleVoteResponse(resp *raftpb.RequestVoteResponse) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if resp.Term > n.currentTerm {
		n.currentTerm = resp.Term
		n.state = Follower
		n.votedFor = ""
		_ = n.persistHardStateLocked()
		n.resetElectionTimer()
		n.reportStateLocked()
	}
}

// HandleVoteRequest is the receiver side of RequestVote. The vote is
// persisted (fsync) before the response is built — a granted vote that is
// not durable is a safety violation.
func (n *Node) HandleVoteRequest(req *raftpb.RequestVoteRequest) *raftpb.RequestVoteResponse {
	n.mu.Lock()
	defer n.mu.Unlock()

	resp := &raftpb.RequestVoteResponse{Term: n.currentTerm}

	if req.Term < n.currentTerm {
		resp.VoteGranted = false
		return resp
	}

	if req.Term > n.currentTerm {
		n.currentTerm = req.Term
		n.state = Follower
		n.votedFor = ""
		// Raft §5.1: a term bump is persisted even when no vote is granted,
		// so this node can never act on the older term after a restart.
		_ = n.persistHardStateLocked()
	}

	candidate := string(req.CandidateId)
	if n.votedFor == "" || n.votedFor == candidate {
		lastIdx := n.log[len(n.log)-1].Index
		lastTerm := n.log[len(n.log)-1].Term
		upToDate := req.LastLogTerm > lastTerm ||
			(req.LastLogTerm == lastTerm && req.LastLogIndex >= lastIdx)
		if upToDate {
			prevVote := n.votedFor
			n.votedFor = candidate
			if err := n.persistHardStateLocked(); err != nil {
				// Durability failure: refuse the vote rather than risk a
				// double vote after restart.
				n.votedFor = prevVote
				resp.VoteGranted = false
				return resp
			}
			resp.VoteGranted = true
			resp.Term = n.currentTerm
			n.resetElectionTimer()
		}
	}

	n.reportStateLocked()
	resp.Term = n.currentTerm // always the responder's current term (§5.1)
	return resp
}
