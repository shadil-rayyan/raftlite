#!/usr/bin/env bash
set -euo pipefail

CLUSTER="${1:-node1:8081 node2:8082 node3:8083}"
LEADER_ENDPOINT=""

echo "=== RaftLite Chaos Test ==="
echo ""

# Find current leader
for node in $CLUSTER; do
  host="${node%:*}"
  port="${node#*:}"
  leader=$(curl -sf "http://$host:$port/leader" | grep -o '"leader_id":"[^"]*"' | cut -d'"' -f4 || true)
  if [ -n "$leader" ]; then
    LEADER_ENDPOINT="$host:$port"
    echo "Current leader: $leader at $LEADER_ENDPOINT"
    break
  fi
done

if [ -z "$LEADER_ENDPOINT" ]; then
  echo "ERROR: could not find leader"
  exit 1
fi

echo ""
echo "Killing leader container: $LEADER_ENDPOINT"
LEADER_CONTAINER=$(docker ps --format '{{.Names}}' | grep "$(echo $LEADER_ENDPOINT | cut -d: -f1)" || true)
if [ -n "$LEADER_CONTAINER" ]; then
  docker kill "$LEADER_CONTAINER" &>/dev/null
  echo "Killed: $LEADER_CONTAINER"
else
  echo "WARN: could not find container for $LEADER_ENDPOINT, trying docker-compose kill"
  docker-compose kill "$(echo $LEADER_ENDPOINT | cut -d: -f1)" 2>/dev/null || true
fi

echo ""
echo "Waiting for new leader election..."
sleep 5

# Verify new leader is elected
for node in $CLUSTER; do
  host="${node%:*}"
  port="${node#*:}"
  NEW_LEADER=$(curl -sf "http://$host:$port/leader" | grep -o '"leader_id":"[^"]*"' | cut -d'"' -f4 || true)
  if [ -n "$NEW_LEADER" ]; then
    echo "New leader elected: $NEW_LEADER"
    break
  fi
done

echo ""
echo "=== Chaos test complete ==="
