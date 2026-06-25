# Deterministic Simulation Testing (DST)

## Why DST Matters

Distributed systems bugs are "Heisenbugs" — they occur due to precise interleavings of network delays, clock drifts, and disk stalls that are nearly impossible to reproduce on real hardware. Traditional testing (spin up containers, send traffic, hope it works) misses these.

FoundationDB and TigerBeetle use DST to prove correctness. RaftLite applies the same approach.

## How It Works

The key insight: abstract all sources of nondeterminism behind interfaces, then drive them with a pseudorandom seed.

```go
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
}

type Storage interface {
    Append(entry LogEntry) error
    Scan(fromIndex uint64) ([]LogEntry, error)
    Truncate(toIndex uint64) error
    Sync() error
}
```

The real implementations use `time.Now()` and `os.File`. The mock implementations are driven by a `rand.Rand` seeded with a configurable seed.

## What the Harness Injects

Given a single `uint64` seed, `SimulationHarness` deterministically injects:

| Failure | Mechanism | Configurable |
|---------|-----------|-------------|
| Clock drift | `MockClock.SetDrift()` | ± range (ms) |
| Packet loss | `MockNetwork.SetDropRate()` | Drop rate (0.0-1.0) |
| Network partition | `MockNetwork.SetPartition()` | Per-node on/off |
| Disk stall | `MockStorage.SetStallOn()` | Which call # to stall |
| Disk failure | `MockStorage.SetFailOn()` | Which call # to fail |
| Node crash | `SimulationConfig.NodeCrashEvery` | Every N steps |
| Out-of-order delivery | `MockNetwork` queue reordering | Enabled/disabled |

## Reproducibility

```go
func TestSpecificSeed(t *testing.T) {
    cfg := SimulationConfig{
        Seed:     48291,
        Nodes:    3,
        MaxSteps: 10000,
    }
    h := NewSimulation(cfg)
    // Run cluster
    // If bug found at step 4,821 → same seed reproduces it identically
}
```

Passing `--seed=48291` on the command line recreates the exact interleaving of 10,000+ concurrent events. This is the FoundationDB standard.

## Architecture for Testability

```
┌─────────────────────────────────────────────┐
│            Raft Consensus Node              │
│  (uses Clock, Storage, Network interfaces)  │
└─────────────────────────────────────────────┘
         │                    ▲
         │ uses              │ implements
         ▼                    │
┌─────────────────────────────────────────────┐
│          Transport Layer Interfaces         │
│  Clock │ Network │ Storage                  │
└────┬───────────────────────────┬────────────┘
     │                           │
     ▼                           ▼
RealClock                  MockClock
RealNetwork                MockNetwork
RealStorage (WAL)          MockStorage
```

This interface-based design was established in Phase 1, before any Raft code was written. Every consensus feature was developed against mocked I/O.
