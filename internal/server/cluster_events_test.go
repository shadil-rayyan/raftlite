package server

// Raft-conscious event log: endpoint shape on a real 3-node cluster, plus
// in-network (container) forwarding for follower-hosted fleet state.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
)

type clusterEventsBody struct {
	NodeID   string `json:"node_id"`
	Role     string `json:"role"`
	LeaderID string `json:"leader_id"`
	Term     uint64 `json:"term"`
	Events   []struct {
		ID        uint64    `json:"id"`
		Timestamp time.Time `json:"timestamp"`
		NodeID    string    `json:"node_id"`
		Type      string    `json:"type"`
		Term      uint64    `json:"term"`
		Detail    string    `json:"detail"`
	} `json:"events"`
}

func getClusterEvents(t *testing.T, client *http.Client, base string) clusterEventsBody {
	t.Helper()
	resp, err := client.Get(base + "/v1/cluster/events?limit=50")
	if err != nil {
		t.Fatalf("GET /v1/cluster/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body clusterEventsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

// TestClusterEventsEndpoint drives the 3-node in-process HTTP cluster and
// asserts each node serves its own Raft event view: the leader records
// leader_elected, followers record leader_observed, and the envelope
// carries node/role/term for the dashboard header.
func TestClusterEventsEndpoint(t *testing.T) {
	_, urls, clients := newThreeNodeHTTPCluster(t, "")
	deadline := time.Now().Add(5 * time.Second)

	leaderSeen, followersSeen := false, 0
	for time.Now().Before(deadline) {
		leaderSeen, followersSeen = false, 0
		for i := range urls {
			b := getClusterEvents(t, clients[i], urls[i])
			if b.NodeID == "" || b.Role == "" {
				t.Fatalf("node %d envelope missing node_id/role: %+v", i, b)
			}
			for _, e := range b.Events {
				if e.NodeID != b.NodeID {
					t.Fatalf("node %d returned an event from %s", i, e.NodeID)
				}
				if e.Timestamp.IsZero() || e.Type == "" {
					t.Fatalf("event missing timestamp/type: %+v", e)
				}
			}
			switch b.Role {
			case "leader":
				leaderSeen = hasEventType(b, "leader_elected")
			case "follower":
				if hasEventType(b, "leader_observed") {
					followersSeen++
				}
			}
		}
		if leaderSeen && followersSeen == 2 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("event log incomplete: leader_elected=%v followers_with_leader_observed=%d",
		leaderSeen, followersSeen)
}

func hasEventType(b clusterEventsBody, typ string) bool {
	for _, e := range b.Events {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// TestFleetForwardsViaInternalAddrs: when a follower's browser-facing peer
// URLs are unreachable from inside the process (the container case —
// localhost of a *different* container), the in-network admin URLs take
// over for server-side forwarding and the fleet request still lands on the
// leader.
func TestFleetForwardsViaInternalAddrs(t *testing.T) {
	leaderNode := &fakeNode{id: "n2", state: consensus.Leader, leader: "n2"}
	leaderAPI, _ := newFleetAPI(t, leaderNode, "", true)
	leaderSrv := httptest.NewServer(leaderAPI.Handler())
	defer leaderSrv.Close()

	followerNode := &fakeNode{id: "n1", state: consensus.Follower, leader: "n2"}
	followerAPI, _ := newFleetAPI(t, followerNode, "", true)
	// Browser-facing URL is a localhost address no container can reach...
	followerAPI.SetPeerAdminAddrs(map[string]string{"n2": "http://127.0.0.1:1"})
	followerH := followerAPI.Handler()

	// ...so forwarding fails until the in-network URL is configured.
	rec := postJSON(t, followerH, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 20})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("without internal addrs forwarding must fail with 502, got %d %s", rec.Code, rec.Body.String())
	}

	followerAPI.SetInternalPeerAdminAddrs(map[string]string{"n2": leaderSrv.URL})
	rec = postJSON(t, followerH, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 20})
	if rec.Code != http.StatusCreated {
		t.Fatalf("internal-addrs forwarded create: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, getJSON(t, followerH, "/v1/fleet/gateways", ""))
	if body["summary"].(map[string]any)["total"] != float64(1) {
		t.Fatalf("forwarded read saw wrong state: %v", body)
	}
}
