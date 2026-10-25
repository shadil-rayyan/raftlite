package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	"github.com/shadil/raftlite/api/go/loopbackpb"
	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/policy"
)

type RaftServer struct {
	raftpb.UnimplementedRaftServiceServer
	node *consensus.Node
}

func NewRaftServer(node *consensus.Node) *RaftServer {
	return &RaftServer{node: node}
}

func (s *RaftServer) AppendEntries(_ context.Context, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	return s.node.HandleAppendEntries(req), nil
}

func (s *RaftServer) RequestVote(_ context.Context, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	return s.node.HandleVoteRequest(req), nil
}

func (s *RaftServer) InstallSnapshot(_ context.Context, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	return s.node.HandleInstallSnapshot(req), nil
}

type LoopbackServer struct {
	loopbackpb.UnimplementedLoopbackServer
	node *consensus.Node
}

func NewLoopbackServer(node *consensus.Node) *LoopbackServer {
	return &LoopbackServer{node: node}
}

// AddBlock proposes a structured block policy through the authoritative
// commit path. The anomaly detector may only propose — never mutate state
// directly (FR-033).
func (s *LoopbackServer) AddBlock(ctx context.Context, req *raftpb.AddBlockRequest) (*raftpb.AddBlockResponse, error) {
	id, err := policy.NewID()
	if err != nil {
		return nil, err
	}
	cmd := policy.Command{
		Op:        policy.OpCreate,
		ID:        id,
		Type:      policy.TypeIPBlock,
		Target:    req.Ip,
		Reason:    req.Reason,
		CreatedBy: "ai-engine",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if req.TtlSeconds > 0 {
		t := time.Now().Add(time.Duration(req.TtlSeconds) * time.Second).UTC().Truncate(time.Second)
		cmd.ExpiresAt = &t
	}
	payload, _ := json.Marshal(cmd)
	idx, err := s.node.ProposeAndWait(ctx, payload)
	if err != nil {
		return nil, err
	}
	return &raftpb.AddBlockResponse{Acked: true, RaftIndex: idx}, nil
}

// StartGRPCServer serves Raft RPCs and the AI loopback on one listener.
// creds may be nil (plaintext, development only — SEC-003/004).
func StartGRPCServer(addr string, node *consensus.Node, creds credentials.TransportCredentials) (*grpc.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}

	var srv *grpc.Server
	if creds != nil {
		srv = grpc.NewServer(grpc.Creds(creds))
	} else {
		srv = grpc.NewServer()
	}
	raftpb.RegisterRaftServiceServer(srv, NewRaftServer(node))
	loopbackpb.RegisterLoopbackServer(srv, NewLoopbackServer(node))
	reflection.Register(srv)

	go srv.Serve(lis)

	return srv, lis, nil
}
