# Raft Consensus Internals

## Overview

RaftLite implements the full Raft consensus protocol from scratch in Go. No `hashicorp/raft` wrapper — every line is hand-written to enable Deterministic Simulation Testing (DST) and full Joint Consensus support.

## Node States

```
Follower ──(election timeout)──► Candidate ──(majority votes)──► Leader
    ▲                                                            │
    └──────────────────(higher term discovered)──────────────────┘
```

- **Follower**: Default state. Responds to RPCs from leaders/candidates.
- **Candidate**: Starts election, votes for self, requests votes from peers.
- **Leader**: Receives client requests, replicates log, manages commitment.

## Election Protocol

```go
func (n *Node) startElection() {
    n.currentTerm++          // Monotonically increasing term number
    n.state = Candidate
    n.votedFor = n.id        // Vote for self
    // Send RequestVote to all peers
    // If majority → becomeLeader()
    // If higher term discovered → revert to Follower
    // If timeout → start new election
}
```

Key details:
- Election timeout is randomized (150ms base + random jitter 0-150ms)
- A server grants one vote per term (`votedFor` check)
- Voter checks candidate's log is at least as up-to-date as its own (last log term first, then last log index)
- Pre-vote extension: candidate checks it can win before bumping term (reduces disruptive elections)

## Log Replication

```
Leader                    Follower
  │                          │
  │── AppendEntries ──────►  │
  │   (prevLogIndex,         │
  │    prevLogTerm,          │   Check consistency:
  │    entries[],            │   - log[prevLogIndex].term == prevLogTerm?
  │    leaderCommit)         │   - If not, reject with conflict info
  │                          │
  │◄── AppendEntriesResp ──  │
  │    (success,             │
  │     conflictIndex,       │
  │     conflictTerm)        │
```

When a follower rejects an AppendEntries:
1. Set `nextIndex[peer]` to the first index of the conflicting term (fast rollback)
2. Or to `conflictIndex` if conflicting term not found in leader's log

Commit rule: an entry is committed when the leader has replicated it to a majority of the cluster AND the entry is from the leader's current term (previous terms cannot be committed indirectly).

## Joint Consensus (Dynamic Membership)

Standard Raft changes membership one node at a time, which can lead to split-brain. RaftLite implements the full Joint Consensus protocol from the Raft dissertation:

```
C_OLD ──► C_OLD,NEW ──► C_NEW
         (transitional)
```

- **Phase 1 (C_OLD,NEW)**: Leader proposes a joint configuration entry. A quorum requires a majority of BOTH the old AND new configurations simultaneously.
- **Phase 2 (C_NEW)**: Once the joint entry is committed, leader proposes the final C_NEW configuration. Normal quorum rules apply.

This guarantees safety during membership changes: no two disjoint majorities can exist during the transition.

## Flat-File Write-Ahead Log (WAL)

Size-based compaction (64MB threshold) — each log entry is:
```
┌─────────────────────────────────────────────┐
│ Index (8) │ Term (8) │ Type (1) │ Len (4) │  ← 21-byte header
├─────────────────────────────────────────────┤
│ Data (variable)                             │
├─────────────────────────────────────────────┤
│ CRC32 (4)                                   │  ← integrity check
└─────────────────────────────────────────────┘
```

- `O_SYNC` write for durability
- CRC32 checksum on every entry for corruption detection
- Truncation on conflict resolution
- Snapshot: JSON-serialized FSM state at 64MB WAL threshold

## Lease Reads

(Phase 2 feature — added after correctness is proven)
- Leader assumes it's still the leader for a lease duration after the last successful heartbeat
- Can serve reads from local state without a round-trip to followers
- Requires clock synchronization bounds
