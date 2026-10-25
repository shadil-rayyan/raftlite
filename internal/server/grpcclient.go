package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
)

// GRPCTransport implements consensus.Transport over gRPC with a per-peer
// connection pool, reconnect on demand, and per-RPC deadlines (the deadline
// arrives via the context from the consensus layer).
type GRPCTransport struct {
	mu     sync.Mutex
	conns  map[string]*grpc.ClientConn
	opts   []grpc.DialOption
	addrs  map[string]string // peer ID -> "host:port"
}

// TLSConfig holds inter-node transport security settings (SEC-003/004).
// When CAFile is set, peer certificates are verified against it. When
// CertFile/KeyFile are set, this node presents a client certificate
// (mutual TLS). An empty TLSConfig yields insecure transport, which must be
// an explicit choice for local development only.
type TLSConfig struct {
	CAFile   string
	CertFile string
	KeyFile  string
}

// LoadServerCredentials builds gRPC server credentials from a TLSConfig.
// Returns nil when TLS is not configured (plaintext, dev only).
func LoadServerCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if cfg.CAFile == "" && cfg.CertFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in CA file")
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		tlsCfg.RootCAs = pool
	}
	return credentials.NewTLS(tlsCfg), nil
}

// NewGRPCTransport creates a transport. addrs maps peer ID to "host:port".
func NewGRPCTransport(addrs map[string]string, tlsCfg *TLSConfig) (*GRPCTransport, error) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))

	if tlsCfg != nil && (tlsCfg.CAFile != "" || tlsCfg.CertFile != "") {
		creds, err := LoadServerCredentials(*tlsCfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		// Explicit insecure mode for development clusters.
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	t := &GRPCTransport{
		conns: make(map[string]*grpc.ClientConn),
		opts:  opts,
		addrs: addrs,
	}
	return t, nil
}

// conn returns (and lazily dials) the connection for a peer.
func (t *GRPCTransport) conn(peer string) (*grpc.ClientConn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.conns[peer]; ok && c.GetState().String() != "Shutdown" {
		return c, nil
	}
	addr, ok := t.addrs[peer]
	if !ok {
		return nil, fmt.Errorf("unknown peer %q", peer)
	}
	c, err := grpc.NewClient(addr, t.opts...)
	if err != nil {
		return nil, fmt.Errorf("dial peer %q: %w", peer, err)
	}
	t.conns[peer] = c
	return c, nil
}

// Close tears down all peer connections.
func (t *GRPCTransport) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.conns {
		c.Close()
	}
	t.conns = make(map[string]*grpc.ClientConn)
}

func (t *GRPCTransport) SendRequestVote(ctx context.Context, to string, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	c, err := t.conn(to)
	if err != nil {
		return nil, err
	}
	ctx, cancel := deadline(ctx)
	defer cancel()
	return raftpb.NewRaftServiceClient(c).RequestVote(ctx, req)
}

func (t *GRPCTransport) SendAppendEntries(ctx context.Context, to string, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	c, err := t.conn(to)
	if err != nil {
		return nil, err
	}
	ctx, cancel := deadline(ctx)
	defer cancel()
	return raftpb.NewRaftServiceClient(c).AppendEntries(ctx, req)
}

func (t *GRPCTransport) SendInstallSnapshot(ctx context.Context, to string, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	c, err := t.conn(to)
	if err != nil {
		return nil, err
	}
	ctx, cancel := deadline(ctx)
	defer cancel()
	return raftpb.NewRaftServiceClient(c).InstallSnapshot(ctx, req)
}

// deadline caps any RPC that arrives without one.
func deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 2*time.Second)
}
