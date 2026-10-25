// Package observability implements RaftLite's Prometheus metrics (FR-030)
// in the standard text exposition format, with no third-party dependency —
// the metrics are emitted by the process itself, so the exposition is
// generated from live state rather than hardcoded.
//
// Swap-in note: exporting with prometheus/client_golang is a mechanical
// change (same names, labels, types); the hand-rolled version keeps the
// binary dependency-free until a registry-based integration is wanted.
package observability

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// Registry holds the metric set for one process.
type Registry struct {
	// counters
	electionsTotal   atomic.Uint64
	commitsTotal     atomic.Uint64
	proposalRejected atomic.Uint64
	proposalUncertain atomic.Uint64
	rpcErrors        sync.Map // key: direction/type -> *atomic.Uint64

	// gauges
	isLeader     atomic.Int64 // 0/1
	term         atomic.Uint64
	commitIndex  atomic.Uint64
	appliedIndex atomic.Uint64
	policies     atomic.Int64
	walSizeBytes atomic.Int64
	snapshotIndex atomic.Uint64

	// latency histogram for propose->commit, in seconds
	commitLatency histogram

	mu     sync.Mutex
	labels map[string]string // static labels, e.g. node_id
}

const (
	// Buckets cover the realistic consensus range: sub-millisecond local
	// commits up to multi-second quorum stalls. Upper bound +Inf implicit.
	bucketCount = 11
)

var bucketBounds = [bucketCount]float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
}

type histogram struct {
	mu     sync.Mutex
	counts [bucketCount]uint64
	sum    float64
	total  uint64
}

func (h *histogram) observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.total++
	for i, b := range bucketBounds {
		if v <= b {
			h.counts[i]++
		}
	}
}

// NewRegistry creates an empty metric registry.
func NewRegistry(nodeID string) *Registry {
	r := &Registry{labels: map[string]string{"node_id": nodeID}}
	return r
}

// ---- updates from the consensus layer / API ----

func (r *Registry) IncElections()              { r.electionsTotal.Add(1) }
func (r *Registry) IncCommits()                { r.commitsTotal.Add(1) }
func (r *Registry) IncProposalRejected()       { r.proposalRejected.Add(1) }
func (r *Registry) IncProposalUncertain()      { r.proposalUncertain.Add(1) }
func (r *Registry) SetLeader(v bool) {
	if v {
		r.isLeader.Store(1)
	} else {
		r.isLeader.Store(0)
	}
}
func (r *Registry) SetTerm(t uint64)             { r.term.Store(t) }
func (r *Registry) SetCommitIndex(i uint64)      { r.commitIndex.Store(i) }
func (r *Registry) SetAppliedIndex(i uint64)     { r.appliedIndex.Store(i) }
func (r *Registry) SetPolicies(n int64)          { r.policies.Store(n) }
func (r *Registry) SetWALSizeBytes(n int64)      { r.walSizeBytes.Store(n) }
func (r *Registry) SetSnapshotIndex(i uint64)    { r.snapshotIndex.Store(i) }
func (r *Registry) ObserveCommitLatency(sec float64) { r.commitLatency.observe(sec) }

// IncRPCError counts an RPC failure by direction+reason.
func (r *Registry) IncRPCError(direction, reason string) {
	key := direction + ":" + reason
	actual, _ := r.rpcErrors.LoadOrStore(key, &atomic.Uint64{})
	actual.(*atomic.Uint64).Add(1)
}

// ---- exposition ----

func labelsString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := "{"
	for i, k := range keys {
		if i > 0 {
			s += ","
		}
		s += k + `="` + m[k] + `"`
	}
	return s + "}"
}

func (r *Registry) emitGauge(w io.Writer, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	fmt.Fprintf(w, "%s%s %d\n", name, labelsString(r.labels), v)
}

func (r *Registry) emitCounter(w io.Writer, name, help string, v uint64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	fmt.Fprintf(w, "%s%s %d\n", name, labelsString(r.labels), v)
}

// WriteMetrics renders the full exposition payload.
func (r *Registry) WriteMetrics(w io.Writer) {
	r.emitCounter(w, "raftlite_elections_total",
		"Total leader elections started by this node", r.electionsTotal.Load())
	r.emitCounter(w, "raftlite_commits_total",
		"Total entries committed while this node was leader", r.commitsTotal.Load())
	r.emitCounter(w, "raftlite_proposals_rejected_total",
		"Total proposals rejected before entering the log", r.proposalRejected.Load())
	r.emitCounter(w, "raftlite_proposals_uncertain_total",
		"Total proposals whose commit outcome was not confirmed", r.proposalUncertain.Load())
	r.emitGauge(w, "raftlite_is_leader",
		"1 when this node is the current leader", r.isLeader.Load())
	r.emitGauge(w, "raftlite_term",
		"Current Raft term as seen by this node", int64(r.term.Load()))
	r.emitGauge(w, "raftlite_commit_index",
		"Commit index of the Raft log", int64(r.commitIndex.Load()))
	r.emitGauge(w, "raftlite_applied_index",
		"Last applied index in the state machine", int64(r.appliedIndex.Load()))
	r.emitGauge(w, "raftlite_policies",
		"Number of active policies in the state machine", r.policies.Load())
	r.emitGauge(w, "raftlite_wal_size_bytes",
		"On-disk size of the write-ahead log", r.walSizeBytes.Load())
	r.emitGauge(w, "raftlite_snapshot_index",
		"Last index included in the persisted snapshot", int64(r.snapshotIndex.Load()))

	// RPC errors: counter vec.
	hasRpcErr := false
	r.rpcErrors.Range(func(k, v any) bool { hasRpcErr = true; return false })
	if hasRpcErr {
		fmt.Fprintf(w, "# HELP raftlite_rpc_errors_total Consensus RPC failures by direction and reason\n")
		fmt.Fprintf(w, "# TYPE raftlite_rpc_errors_total counter\n")
		type row struct {
			key string
			v   uint64
		}
		var rows []row
		r.rpcErrors.Range(func(k, v any) bool {
			rows = append(rows, row{key: k.(string), v: v.(*atomic.Uint64).Load()})
			return true
		})
		sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
		for _, row := range rows {
			l := map[string]string{"node_id": r.labels["node_id"]}
			if idx := indexByte(row.key, ':'); idx >= 0 {
				l["direction"] = row.key[:idx]
				l["reason"] = row.key[idx+1:]
			}
			fmt.Fprintf(w, "raftlite_rpc_errors_total%s %d\n", labelsString(l), row.v)
		}
	}

	// Commit latency histogram.
	h := &r.commitLatency
	h.mu.Lock()
	fmt.Fprintf(w, "# HELP raftlite_commit_latency_seconds Propose to commit latency when this node is leader\n")
	fmt.Fprintf(w, "# TYPE raftlite_commit_latency_seconds histogram\n")
	// cumulative buckets: counts are already cumulative
	for i, b := range bucketBounds {
		fmt.Fprintf(w, "raftlite_commit_latency_seconds_bucket%s %d\n",
			labelsString(withLe(r.labels, formatFloat(b))), h.counts[i])
	}
	fmt.Fprintf(w, "raftlite_commit_latency_seconds_bucket%s %d\n",
		labelsString(withLe(r.labels, "+Inf")), h.total)
	fmt.Fprintf(w, "raftlite_commit_latency_seconds_sum%s %s\n",
		labelsString(r.labels), formatFloat(h.sum))
	fmt.Fprintf(w, "raftlite_commit_latency_seconds_count%s %d\n",
		labelsString(r.labels), h.total)
	h.mu.Unlock()
}

func withLe(base map[string]string, le string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["le"] = le
	return out
}

func formatFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
