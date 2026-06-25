#!/usr/bin/env bash
set -euo pipefail

NODE="${1:-http://localhost:8081}"
DURATION="${2:-5m}"
RATE="${3:-25000}"
CONCURRENCY="${4:-200}"

echo "=== RaftLite Benchmark ==="
echo "Target: $NODE"
echo "Duration: $DURATION"
echo "Target RPS: $RATE"
echo "Concurrency: $CONCURRENCY"
echo ""

# ponytail: requires ghz or wrk installed
if command -v ghz &>/dev/null; then
  ghz --insecure \
    --proto api/raft.proto \
    --call raftlite.Loopback/AddBlock \
    -d '{"ip":"10.0.0.1","reason":"benchmark","ttl_seconds":60}' \
    --rps "$RATE" \
    --concurrency "$CONCURRENCY" \
    --duration "$DURATION" \
    "$NODE"
elif command -v wrk &>/dev/null; then
  wrk -t"$CONCURRENCY" -c"$CONCURRENCY" -d"$DURATION" \
    -s <(echo 'wrk.method = "POST"; wrk.body = "{\"ip\":\"10.0.0.1\"}"') \
    "$NODE/block"
else
  echo "ERROR: install ghz or wrk"
  echo "  ghz: https://github.com/bojand/ghz"
  echo "  wrk: https://github.com/wg/wrk"
  exit 1
fi

echo ""
echo "=== Benchmark complete ==="
