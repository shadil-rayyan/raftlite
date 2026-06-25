package server

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/shadil/raftlite/api/go/loopbackpb"
	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/consensus"
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
	resp := &raftpb.InstallSnapshotResponse{Term: s.node.CurrentTerm()}
	_ = req
	return resp, nil
}

type LoopbackServer struct {
	loopbackpb.UnimplementedLoopbackServer
	node *consensus.Node
}

func NewLoopbackServer(node *consensus.Node) *LoopbackServer {
	return &LoopbackServer{node: node}
}

func (s *LoopbackServer) AddBlock(_ context.Context, req *raftpb.AddBlockRequest) (*raftpb.AddBlockResponse, error) {
	data := []byte("add:" + req.Ip)
	idx, err := s.node.Propose(data)
	if err != nil {
		return nil, err
	}
	return &raftpb.AddBlockResponse{Acked: true, RaftIndex: idx}, nil
}

func StartGRPCServer(addr string, node *consensus.Node) (*grpc.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}

	srv := grpc.NewServer()
	raftpb.RegisterRaftServiceServer(srv, NewRaftServer(node))
	loopbackpb.RegisterLoopbackServer(srv, NewLoopbackServer(node))
	reflection.Register(srv)

	go srv.Serve(lis)

	return srv, lis, nil
}
