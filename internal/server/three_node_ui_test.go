package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shadil/raftlite/internal/audit"
	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/policy"
)

func newThreeNodeHTTPCluster(t *testing.T, token string) (*consensus.InProcessCluster, []string, []*http.Client) {
	t.Helper()
	t.Setenv("RAFTLITE_DEV_UI", "1")

	c := consensus.NewInProcessCluster(t, 3)
	urls := make([]string, 3)
	servers := make([]*httptest.Server, 3)
	apis := make([]*API, 3)
	for i := range c.IDs {
		node := c.Nodes[c.IDs[i]]
		apis[i] = NewAPI(node, c.FSMs[c.IDs[i]], audit.New(io.Discard), token, nil)
		servers[i] = httptest.NewServer(apis[i].Handler())
		urls[i] = servers[i].URL
	}
	// Wire peer admin addrs now that all base URLs exist.
	for i := range c.IDs {
		peers := make(map[string]string)
		for j, other := range c.IDs {
			if j != i {
				peers[other] = urls[j]
			}
		}
		apis[i].SetPeerAdminAddrs(peers)
	}
	t.Cleanup(func() {
		for _, s := range servers {
			s.Close()
		}
	})
	clients := make([]*http.Client, 3)
	for i := range clients {
		clients[i] = servers[i].Client()
	}
	return c, urls, clients
}

func waitDiagnosticsHealthy(t *testing.T, urls []string, clients []*http.Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		healthy := 0
		for i, url := range urls {
			resp, err := clients[i].Get(url + "/v1/diagnostics")
			if err == nil && resp.StatusCode == http.StatusOK {
				var d struct {
					ClusterSize int  `json:"cluster_size"`
					Quorum      int  `json:"quorum"`
					QuorumOK    bool `json:"quorum_ok"`
					MajorityLog bool `json:"majority_has_log"`
				}
				if json.NewDecoder(resp.Body).Decode(&d) == nil && d.ClusterSize == 3 && d.Quorum == 2 && d.QuorumOK && d.MajorityLog {
					healthy++
				}
				resp.Body.Close()
			}
		}
		if healthy == len(urls) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cluster diagnostics never became healthy")
}

func TestThreeNodeUIAndDiagnostics(t *testing.T) {
	c, urls, clients := newThreeNodeHTTPCluster(t, "")
	leader := c.WaitLeader(5 * time.Second)

	// ---- every node serves the UI and contains expected markers ----
	for i, url := range urls {
		resp, err := clients[i].Get(url + "/ui")
		if err != nil {
			t.Fatalf("node %d GET /ui: %v", i, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("node %d status %d", i, resp.StatusCode)
		}
		for _, marker := range []string{"RaftLite Console", "const DEV_UI = true;", "Diagnostics", "id=\"token\""} {
			if !bytes.Contains(body, []byte(marker)) {
				t.Fatalf("node %d ui missing %q", i, marker)
			}
		}
	}

	// ---- diagnostics reports a healthy quorum on every node ----
	waitDiagnosticsHealthy(t, urls, clients)
	// ---- commit a policy through the leader and verify replication ----
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, _ := policy.NewID()
	page := "10.0.0.5"
	cmd := policy.Command{
		Op:     policy.OpCreate,
		ID:     id,
		Type:   policy.TypeIPBlock,
		Target: page,
		Reason: "ui test",
	}
	payload, _ := json.Marshal(cmd)
	idx, err := leader.ProposeAndWait(ctx, payload)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	c.WaitApplied(idx, 5*time.Second)

	// ---- every node reports the block ----
	for i, url := range urls {
		resp, err := clients[i].Get(url + "/v1/blocked/10.0.0.5")
		if err != nil {
			t.Fatalf("node %d blocked: %v", i, err)
		}
		defer resp.Body.Close()
		var b map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
			t.Fatalf("node %d decode blocked: %v", i, err)
		}
		if b["blocked"] != true {
			t.Fatalf("node %d not blocked: %+v", i, b)
		}
	}
}
