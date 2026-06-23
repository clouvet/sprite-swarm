#!/usr/bin/env bash
# M2 acceptance: build, start the session service, drive one chat turn end-to-end
# (create session -> WebSocket -> send -> assert token streaming -> result), then
# tear down. Requires an authenticated `claude` CLI. Brain stays disabled.
set -euo pipefail

cd "$(dirname "$0")/.."

ADDR="${ADDR:-:8089}"
BASE="http://localhost:${ADDR#:}"
WORKDIR="${SPRITE_AGENT_WORKDIR:-$HOME}"
LOG="$(mktemp)"

echo "==> building"
go build -o /tmp/sa-smoke-server ./cmd/sprite-agent
go build -o /tmp/sa-smoke-client ./cmd/smoke

echo "==> starting server on ${ADDR} (workdir=${WORKDIR})"
SPRITE_AGENT_ADDR="$ADDR" SPRITE_AGENT_WORKDIR="$WORKDIR" SPRITE_AGENT_PERMISSION_MODE="${PERMISSION_MODE:-plan}" \
  /tmp/sa-smoke-server >"$LOG" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; echo "--- server log ---"; cat "$LOG"' EXIT
sleep 2

echo "==> running smoke client"
/tmp/sa-smoke-client -addr "$BASE" -prompt "Reply with exactly: SMOKE_OK and nothing else." -timeout 140s

echo "==> OK"
