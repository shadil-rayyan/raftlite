package consensus

import (
	"context"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
)

// Transport is the outbound RPC surface a Node needs to talk to peers.
// The gRPC implementation lives in internal/server; tests provide in-memory
// implementations (or nil, which exercises single-node paths only).
type Transport interface {
	SendRequestVote(ctx context.Context, to string, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error)
	SendAppendEntries(ctx context.Context, to string, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error)
	SendInstallSnapshot(ctx context.Context, to string, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error)
}

// rpcTimeout bounds every outbound consensus RPC so a hung peer cannot stall
// the leader's replication or a candidate's election.
const rpcTimeout = 2 * time.Second
