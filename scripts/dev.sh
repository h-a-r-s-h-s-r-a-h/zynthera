#!/usr/bin/env bash
# Runs the API, the agent and the web app together, each with a log prefix.
# Ctrl+C stops all three. Go code is rebuilt on each start, not on each save.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "Building API and agent..."
go build -o bin/zynthera-api ./cmd/api
go build -o bin/zynthera-agent ./cmd/agent

# The web app's dev server needs to know where the API is.
api_url="$(grep -E '^API_INTERNAL_URL=' .env | tail -n 1 | cut -d= -f2- || true)"
export API_INTERNAL_URL="${api_url:-http://127.0.0.1:8080}"

# Job control gives every background job its own process group, so one signal
# to the group stops the service together with its log-prefix pipe.
set -m

pids=()
start() {
  local name="$1"
  shift
  ("$@" 2>&1 | sed -u "s/^/[$name] /") &
  pids+=("$!")
}

cleanup() {
  trap - EXIT INT TERM
  for pid in "${pids[@]}"; do
    kill -TERM -- "-$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

start "api  " ./bin/zynthera-api serve
start "agent" ./bin/zynthera-agent
start "web  " npm --prefix web run dev

echo "Zynthera is starting: http://localhost:3000  (Ctrl+C to stop)"
# Stop everything as soon as any one of them exits.
wait -n || true
