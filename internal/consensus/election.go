package consensus

import (
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
)

func (n *Node) startElection() {
	n.currentTerm++
	n.state = Candidate
	n.votedFor = n.id
	lastIdx := n.log[len(n.log)-1].Index
	lastTerm := n.log[len(n.log)-1].Term

	votes := 1

	for _, peer := range n.peers {
		if peer == n.id {
			continue
		}
		req := raftpb.RequestVoteRequest{
			Term:         n.currentTerm,
			CandidateId:  []byte(n.id),
			LastLogIndex: lastIdx,
			LastLogTerm:  lastTerm,
		}
		_ = req
		// ponytail: Phase 3 gRPC sends this; DST tests call HandleVoteRequest directly
	}

	deadline := n.clock.After(time.Duration(n.electionTimeoutMs) * time.Millisecond)
	_ = deadline
	_ = votes

	n.state = Follower
}

func (n *Node) becomeLeader() {
	n.state = Leader
	n.leaderID = n.id
	for _, peer := range n.peers {
		n.nextIndex[peer] = n.log[len(n.log)-1].Index + 1
		n.matchIndex[peer] = 0
	}
	n.matchIndex[n.id] = n.log[len(n.log)-1].Index

	go n.heartbeatLoop()
	n.broadcastAppendEntries()
}

func (n *Node) heartbeatLoop() {
	for {
		n.mu.Lock()
		if n.state != Leader {
			n.mu.Unlock()
			return
		}
		n.broadcastAppendEntries()
		n.mu.Unlock()

		select {
		case <-n.clock.After(time.Duration(n.heartbeatTimeoutMs) * time.Millisecond):
		case <-n.shutdownCh:
			return
		}
	}
}

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
	}

	if n.votedFor == "" || n.votedFor == string(req.CandidateId) {
		lastIdx := n.log[len(n.log)-1].Index
		lastTerm := n.log[len(n.log)-1].Term
		if req.LastLogTerm > lastTerm ||
			(req.LastLogTerm == lastTerm && req.LastLogIndex >= lastIdx) {
			n.votedFor = string(req.CandidateId)
			resp.VoteGranted = true
			resp.Term = n.currentTerm
			n.resetElectionTimer()
		}
	}

	return resp
}
