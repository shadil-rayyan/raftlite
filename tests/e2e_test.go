package raftlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/transport"
)

func TestE2ESingleNode(t *testing.T) {
	fsm := consensus.NewFSM()
	clock := transport.NewMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	storage := transport.NewMockStorage()
	node := consensus.NewNode("node1", []string{"node1"}, clock, storage, fsm)
	node.Start()
	defer node.Stop()

	if node.State() != consensus.Leader {
		t.Fatal("single node should be leader")
	}

	_, err := node.Propose([]byte("add:10.0.0.1"))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if !fsm.IsBlocked("10.0.0.1") {
		t.Fatal("expected 10.0.0.1 to be blocked")
	}
}

func TestE2EBlockRequest(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run")
	}

	base := "http://localhost:8081"
	client := &http.Client{Timeout: 5 * time.Second}

	body, _ := json.Marshal(map[string]string{"ip": "192.168.1.100"})
	resp, err := client.Post(base+"/block", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /block: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	t.Logf("block result: %v", result)

	resp2, err := client.Get(fmt.Sprintf("%s/blocked/192.168.1.100", base))
	if err != nil {
		t.Fatalf("GET /blocked: %v", err)
	}
	defer resp2.Body.Close()

	var check map[string]any
	json.NewDecoder(resp2.Body).Decode(&check)
	t.Logf("blocked check: %v", check)
}
