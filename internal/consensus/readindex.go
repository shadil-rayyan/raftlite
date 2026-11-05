package consensus

// Linearizable reads via ReadIndex (Milestone 1, P0-D).
//
// A leader serves a linearizable read by recording its commitIndex
// (readIndex), confirming with a quorum that it is STILL the leader at its
// current term via a heartbeat round sent AFTER the recording, and then
// reading local state once lastApplied >= readIndex.
//
// Safety sketch. Term-t acks received after the capture prove that a
// majority had not observed any term > t at ack time. If a different
// leader had committed entries at term > t at ANY point (before or after
// the capture), that commit needed a majority that had accepted the higher
// term; terms never decrease, so those nodes could never ack at exactly
// term t afterwards. The two majorities intersect, so a confirmed round
// rules out competing commits entirely — the recorded readIndex covers
// every entry committed before the read was invoked. Entries committed by
// a new leader after the confirmation overlap the read window, so serving
// without re-checking leadership afterwards is still linearizable.
//
// Acks must be correlated to the post-capture round: an in-flight ack to a
// PRE-capture heartbeat could come from a node that (after sending it)
// accepted a higher-term leader's commits. Send goroutines therefore
// snapshot readGen at spawn (under n.mu) and report it with the response;
// only responses carrying the round's generation count.

import (
	"context"
	"time"
)

// readRound is one in-flight leadership-confirmation round.
type readRound struct {
	gen    uint64
	term   uint64
	acks   map[string]bool
	result chan error // buffered(1): nil = quorum confirmed
}

// ReadIndex returns a read barrier: once it returns without error on a
// leader, the caller may serve a linearizable read of any state at or
// below the returned index from its own FSM. Callers that don't need the
// barrier index may ignore it.
//
// Errors: ErrNotLeader (not leader, or leadership changed mid-round),
// context deadline/cancellation (quorum could not be confirmed in time).
func (n *Node) ReadIndex(ctx context.Context) (uint64, error) {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return 0, ErrNotLeader
	}
	readIndex := n.commitIndex
	if majorityOf(len(n.peers)) <= 1 {
		// Single-voter cluster: we are the quorum.
		n.mu.Unlock()
		return readIndex, nil
	}
	n.readGen++
	gen := n.readGen
	round := &readRound{
		gen:    gen,
		term:   n.currentTerm,
		acks:   map[string]bool{n.id: true}, // self-vote
		result: make(chan error, 1),
	}
	n.readRounds[gen] = round
	// Heartbeat round sent AFTER the capture; the send goroutines carry
	// this generation so only their responses can confirm the round.
	n.broadcastAppendEntriesLocked()
	n.mu.Unlock()

	select {
	case err := <-round.result:
		if err != nil {
			return 0, err
		}
	case <-ctx.Done():
		n.abandonRound(gen, round)
		return 0, ctx.Err()
	case <-n.shutdownCh:
		n.abandonRound(gen, round)
		return 0, ErrNoQuorum
	}

	// Quorum confirmed. commitIndex is monotonic and the leader applies
	// committed entries in the same critical section that advances it, so
	// lastApplied normally already covers readIndex; poll briefly in case
	// apply is momentarily behind (bounded by ctx).
	for {
		n.mu.Lock()
		applied := n.lastApplied
		stillValid := n.state == Leader && n.currentTerm == round.term
		n.mu.Unlock()
		if applied >= readIndex {
			return readIndex, nil
		}
		if !stillValid {
			// The proof above stays valid for entries committed so far,
			// but fail fast rather than spin: the caller can retry and
			// reach the new leader.
			return 0, ErrNotLeader
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// abandonRound drops a timed-out round from the map (no-op if it already
// completed or was failed by a step-down).
func (n *Node) abandonRound(gen uint64, round *readRound) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r, ok := n.readRounds[gen]; ok && r == round {
		delete(n.readRounds, gen)
	}
}

// noteReadAckLocked accounts a peer response toward the confirmation
// round `gen`. Responses with a different generation came from requests
// sent before (or after) this round's capture and must not count.
// Caller holds n.mu.
func (n *Node) noteReadAckLocked(gen uint64, peer string, respTerm uint64) {
	if gen == 0 {
		return // test/manual callers: not part of a round
	}
	round, ok := n.readRounds[gen]
	if !ok {
		return // round already completed or abandoned
	}
	if n.state != Leader || n.currentTerm != round.term || respTerm != round.term {
		// The round can no longer be confirmed at its original term.
		n.completeRoundLocked(round, ErrNotLeader)
		return
	}
	round.acks[peer] = true
	if len(round.acks) >= majorityOf(len(n.peers)) {
		n.completeRoundLocked(round, nil)
	}
}

// completeRoundLocked removes the round and reports the outcome to its
// waiter (first writer wins; the channel is buffered). Caller holds n.mu.
func (n *Node) completeRoundLocked(round *readRound, err error) {
	delete(n.readRounds, round.gen)
	select {
	case round.result <- err:
	default:
	}
}

// failPendingReadsLocked aborts every in-flight confirmation round. Called
// on each path that steps this node down or bumps its term: those rounds
// can never be confirmed at their original term, and their waiters should
// learn immediately instead of timing out. Caller holds n.mu.
func (n *Node) failPendingReadsLocked(err error) {
	for gen, round := range n.readRounds {
		delete(n.readRounds, gen)
		select {
		case round.result <- err:
		default:
		}
	}
}
