#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DATASET="${DATASET:-policies-100.json}"
RATE="${RATE:-10}"
PORT="${PORT:-8095}"
BASE="http://127.0.0.1:${PORT}"
OUT="${OUT:-$ROOT/results/all-runs.json}"

mkdir -p "$ROOT/bin" "$ROOT/work" "$ROOT/results/raw"

# Ensure datasets exist.
if [ ! -f "$ROOT/datasets/$DATASET" ]; then
    echo "generating datasets..."
    go run ./cmd/gen
fi

# Stop any previous measure instance recorded by this script.
if [ -f "$ROOT/work/measure.pid" ]; then
    old="$(cat "$ROOT/work/measure.pid" 2>/dev/null || true)"
    if [ -n "${old:-}" ] && kill -0 "$old" 2>/dev/null; then
        kill "$old" 2>/dev/null || true
        wait "$old" 2>/dev/null || true
    fi
    rm -f "$ROOT/work/measure.pid"
fi

# Start measurement server.
echo "starting measurement server on :$PORT ..."
./bin/measure >"$ROOT/work/measure.log" 2>&1 &
MEASURE_PID=$!
echo "$MEASURE_PID" >"$ROOT/work/measure.pid"

cleanup() {
    curl -s -X POST "$BASE/api/env/stop" >/dev/null 2>&1 || true
    if kill -0 "$MEASURE_PID" 2>/dev/null; then
        kill "$MEASURE_PID" 2>/dev/null || true
        wait "$MEASURE_PID" 2>/dev/null || true
    fi
    rm -f "$ROOT/work/measure.pid"
}
trap cleanup EXIT

# Wait for server health.
for i in $(seq 1 60); do
    if curl -sf "$BASE/api/status" >/dev/null 2>&1; then
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "measurement server did not start; see $ROOT/work/measure.log"
        exit 1
    fi
    sleep 1
done

post_json() {
    curl -s -X POST -H 'Content-Type: application/json' -d "$1" "$2"
}

wait_run() {
    local run_id="$1"
    local deadline=$((SECONDS + 600))
    sleep 1
    while [ "$SECONDS" -lt "$deadline" ]; do
        code="$(curl -s -o /tmp/opencode/run-$run_id.json -w '%{http_code}' "$BASE/api/runs/$run_id" 2>/dev/null || true)"
        if [ "$code" = "200" ]; then
            cat "/tmp/opencode/run-$run_id.json"
            return 0
        fi
        active="$(curl -s "$BASE/api/status" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('run_active',False))" 2>/dev/null || echo False)"
        if [ "$active" = "False" ] && [ "$code" != "200" ]; then
            echo "run $run_id failed or produced no result" >&2
            return 1
        fi
        sleep 2
    done
    echo "run $run_id timed out" >&2
    return 1
}

run_experiment() {
    local system="$1"
    local exp="$2"
    local rate="$RATE"
    if [ "$exp" = "http_performance" ]; then
        rate="${HTTP_RATE:-200}"
    fi
    if [ "$exp" = "resource_usage" ]; then
        rate="${RESOURCE_RATE:-$RATE}"
    fi
    echo ""
    echo "=== $system / $exp (rate=$rate) ==="
    resp="$(post_json "{\"experiment\":\"$exp\",\"dataset\":\"$DATASET\",\"rate\":$rate}" "$BASE/api/runs")"
    run_id="$(python3 -c "import sys,json; print(json.load(sys.stdin)['run_id'])" <<<"$resp")"
    echo "run_id=$run_id"
    result="$(wait_run "$run_id")"
    python3 -c "import sys,json; d=json.load(sys.stdin); print('done:', d.get('experiment'), 'rows=', len(d.get('stats',{})))" <<<"$result"
}

# Traditional: all experiments except partition (not applicable).
echo "starting traditional environment..."
post_json '{"system":"traditional"}' "$BASE/api/env/start" >/dev/null
for exp in propagation consistency server_failure gateway_recovery http_performance resource_usage; do
    run_experiment traditional "$exp"
done
post_json '' "$BASE/api/env/stop" >/dev/null 2>&1 || curl -s -X POST "$BASE/api/env/stop" >/dev/null

# RaftLite: all experiments.
echo "starting raftlite environment..."
post_json '{"system":"raftlite"}' "$BASE/api/env/start" >/dev/null
for exp in propagation consistency server_failure partition gateway_recovery http_performance resource_usage; do
    run_experiment raftlite "$exp"
done
curl -s -X POST "$BASE/api/env/stop" >/dev/null

# Save all run summaries from this session into one file.
curl -s "$BASE/api/runs" >"$OUT"
echo ""
echo "wrote $OUT"

# Aggregate raw CSVs into summary.csv.
python3 scripts/analyze.py

echo ""
echo "all runs complete; results: $OUT and $ROOT/results/summary.csv"
