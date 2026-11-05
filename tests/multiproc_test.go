package raftlite

// Multi-process crash/recovery test for Milestone 1: three REAL raftlite
// processes on localhost, a hard-killed leader (TerminateProcess-grade
// crash), re-election on the survivors, post-failover writes, and a
// restart of the killed node that must rejoin from durable state and
// catch up. This is the in-repo form of the live smoke script; it is
// gated on INTEGRATION=1 because it builds and runs real binaries.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMultiProcessClusterFailover(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run")
	}

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "raftlite")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/raftlite")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build raftlite: %v\n%s", err, out)
	}

	ids := []string{"node1", "node2", "node3"}
	httpPort := map[string]int{}
	raftAddr := map[string]string{}
	adminURL := map[string]string{}
	for _, id := range ids {
		httpPort[id] = freePort(t)
		raftAddr[id] = fmt.Sprintf("127.0.0.1:%d", freePort(t))
		adminURL[id] = fmt.Sprintf("http://127.0.0.1:%d", httpPort[id])
	}

	var peerAddrs, adminPeers, adminInternal []string
	for _, id := range ids {
		peerAddrs = append(peerAddrs, id+"="+raftAddr[id])
		adminPeers = append(adminPeers, id+"="+adminURL[id])
		adminInternal = append(adminInternal, id+"="+adminURL[id])
	}

	cmds := map[string]*exec.Cmd{}
	logs := map[string]string{}
	var logFiles []*os.File
	start := func(id string) *exec.Cmd {
		nodeDir := filepath.Join(binDir, id)
		if err := os.MkdirAll(nodeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"NODE_ID="+id,
			"RAFT_ADDR="+raftAddr[id],
			"RAFTLITE_ADDR=127.0.0.1:"+strconv.Itoa(httpPort[id]),
			"PEERS="+strings.Join(ids, ","),
			"PEER_ADDRS="+strings.Join(peerAddrs, ","),
			"ADMIN_PEER_ADDRS="+strings.Join(adminPeers, ","),
			"ADMIN_INTERNAL_PEER_ADDRS="+strings.Join(adminInternal, ","),
			"WAL_PATH="+filepath.Join(nodeDir, id+".wal"),
			"SNAPSHOT_DIR="+filepath.Join(nodeDir, "snapshots"),
		)
		logPath := filepath.Join(binDir, id+".log")
		logs[id] = logPath
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		logFiles = append(logFiles, logFile)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		return cmd
	}
	stop := func(cmd *exec.Cmd) {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill() // hard kill: crash semantics
			_, _ = cmd.Process.Wait()
		}
	}
	for _, id := range ids {
		cmds[id] = start(id)
	}
	t.Cleanup(func() {
		for _, c := range cmds {
			stop(c)
		}
		// Close log handles before the TempDir cleanup tries to remove
		// them (Windows refuses to delete open files).
		for _, f := range logFiles {
			f.Close()
		}
	})

	base := func(id string) string { return fmt.Sprintf("http://127.0.0.1:%d", httpPort[id]) }
	bases := func(ids ...string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, base(id))
		}
		return out
	}

	// 1. Elect a leader across three real processes.
	leaderID := waitLeaderOn(t, bases(ids...), "", 20*time.Second)
	t.Logf("leader elected: %s", leaderID)

	// 2. Write through a FOLLOWER (exercises leader forwarding).
	var followerID string
	for _, id := range ids {
		if id != leaderID {
			followerID = id
			break
		}
	}
	if err := postBlock(t, base(followerID), "203.0.113.10", "multiproc"); err != nil {
		t.Fatalf("block via follower %s: %v (log: %s)", followerID, err, logs[followerID])
	}

	// 3. The committed block is visible on every node.
	waitBlockedOn(t, bases(ids...), "203.0.113.10", 10*time.Second)

	// 4. Hard-kill the leader; survivors must elect a new one.
	stop(cmds[leaderID])
	delete(cmds, leaderID)
	t.Logf("killed leader %s", leaderID)

	var survivors []string
	for _, id := range ids {
		if id != leaderID {
			survivors = append(survivors, id)
		}
	}
	newLeaderID := waitLeaderOn(t, bases(survivors...), leaderID, 20*time.Second)
	t.Logf("re-elected: %s -> %s", leaderID, newLeaderID)

	// 5. Writes still work on the surviving cluster.
	if err := postBlock(t, base(survivors[0]), "203.0.113.11", "post-failover"); err != nil {
		t.Fatalf("post-failover block via %s: %v (log: %s)", survivors[0], err, logs[survivors[0]])
	}
	waitBlockedOn(t, bases(survivors...), "203.0.113.11", 10*time.Second)

	// 6. Restart the killed node on its original ports and data dirs: it
	// must rejoin from durable state and catch up on the entry it never
	// saw before the crash.
	cmds[leaderID] = start(leaderID)
	waitBlockedOn(t, bases(ids...), "203.0.113.11", 20*time.Second)
	t.Logf("%s rejoined and caught up", leaderID)
}

// waitLeaderOn polls every base until one reports a leader id; if exclude
// is non-empty, the reported leader must differ from it.
func waitLeaderOn(t *testing.T, bases []string, exclude string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, b := range bases {
			leaderID, err := getLeader(b)
			if err != nil {
				continue
			}
			if leaderID != "" && leaderID != exclude {
				return leaderID
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no leader within %v (bases: %v)", timeout, bases)
	return ""
}

// waitBlockedOn polls every base until the IP reports blocked=true.
func waitBlockedOn(t *testing.T, bases []string, ip string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pending := 0
		for _, b := range bases {
			blocked, err := getBlocked(b, ip)
			if err != nil || !blocked {
				pending++
			}
		}
		if pending == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, b := range bases {
		blocked, err := getBlocked(b, ip)
		t.Logf("%s blocked[%s]=%v err=%v", b, ip, blocked, err)
	}
	t.Fatalf("%s not visible on every node within %v", ip, timeout)
}

func getLeader(base string) (string, error) {
	resp, err := http.Get(base + "/leader")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		LeaderID string `json:"leader_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.LeaderID, nil
}

func getBlocked(base, ip string) (bool, error) {
	resp, err := http.Get(fmt.Sprintf("%s/v1/blocked/%s", base, ip))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Blocked bool `json:"blocked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Blocked, nil
}

func postBlock(t *testing.T, base, ip, reason string) error {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"ip": ip, "reason": reason, "duration": "1h"})
	resp, err := http.Post(base+"/v1/policies/ip-blocks", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("status %d: %v", resp.StatusCode, out)
	}
	if out["policy_id"] == nil || out["policy_id"] == "" {
		return fmt.Errorf("no policy_id in response: %v", out)
	}
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
