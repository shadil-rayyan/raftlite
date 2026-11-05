# Measurement explanation

## What a propagation measurement is

For one policy submitted once, observed by one gateway:

```
t_submit  = wall clock on the measurement server, taken immediately before it POSTs
            the policy to the backend
t_applied = wall clock on the gateway, taken at the instant it applies the policy
            into its local cache (first time it has seen that policy)
latency   = t_applied − t_submit
```

All processes run on the same host, so one clock serves both timestamps — no clock-sync
correction is involved.

The measurement therefore covers, end to end:

1. backend write (SQLite transaction commit **or** RaftLite consensus commit),
2. the gateway's next poll picking the change up (its poll interval — 500 ms by default),
3. the gateway's cache rebuild.

Because step 2 is a periodic poll, the latency distribution is bounded below by ~0 and above by
roughly the poll interval, with the backend's own cost stacked on top. That is why the observed
medians sit near half the poll interval and why the two systems differ by a smaller amount on
top of that shared term. **If you want to isolate the backend's contribution, lower the poll
interval** (`-poll`, or the `poll` field in the measurement server config) — the comparison stays
fair because both systems use the same interval.

## How receipts work

The gateway is the only component that knows when a policy actually took effect locally. So it
reports:

```json
{"gateway_id":"G3","policy_id":"P00073-r1","ip":"100.1.0.73","revision":42,"applied_at_ns":1793648977732666600}
```

- emitted **once per policy per gateway**, the first time that gateway applies it,
- POSTed to the measurement server's `/receipts` endpoint (retried briefly on failure),
- matched by **IP** (not policy ID) because RaftLite assigns its own policy IDs.

The measurement server keeps a per-run registry of in-flight policies keyed by IP. When a
receipt arrives for a gateway that has not yet reported that IP, a row is recorded. When every
gateway has reported, the policy is "fully propagated".

## Statistics

Computed over all recorded latencies in milliseconds, **without discarding any sample** (slow
results are data):

- **mean** — arithmetic mean
- **median** — 50th percentile (nearest-rank on the sorted sample)
- **min / max**
- **stddev** — sample standard deviation (n−1)
- **P95 / P99** — nearest-rank percentiles

Reported per gateway (`G1`…`G5`) and overall (`all`).

## CSV formats

### Propagation — `results/raw/<system>-propagation-run<N>-<timestamp>.csv`

```
run_id,policy_id,ip,gateway_id,submit_at_ns,applied_at_ns,latency_ms
1,P00073-r1,100.1.0.73,G1,1793648977424218500,1793648977718226900,294.008
```

One row per (policy × gateway). `applied_at_ns` is the gateway's own clock; `latency_ms` is
derived from it and `submit_at_ns`.

### Consistency — `results/raw/<system>-consistency-run<N>-<timestamp>.csv`

```
run_id,gateway_id,correct,missing,incorrect,catchup_ms
4,G1,100,0,0,207.560
```

One row per gateway. `correct` = expected policies present with the right action,
`missing` = expected policies absent, `incorrect` = present with a different action.

## Run-scoped identities

Every run rewrites the dataset: policy IDs get an `-r<run>` suffix and IPs are remapped into
`100.<run%256>.<idx/256>.<idx%256>` (valid exact IPv4). Both the submitted set and the
expected set use the rewritten values, so correctness comparisons are unaffected.

Why this matters: gateways only report a receipt the first time they apply a given policy.
Without run scoping, a second run of the same dataset would re-submit already-known policies,
no receipts would fire, and the run would silently measure nothing.

## What is *not* measured

- In-memory gateway enforcement latency (the 429/200 data path) — that is experiment 5's job.
- Resource usage (CPU/memory/disk) — experiment 6's job.
- Behaviour under node failure or partition — experiments 3/4/7.

No latency or throughput *targets* are asserted anywhere in this project. Numbers in the README
come from single-machine smoke runs and are labelled as such.
