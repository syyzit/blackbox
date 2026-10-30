#!/usr/bin/env bash
# Same tour, but through a real third-party MCP server (the reference filesystem one).
set -euo pipefail
cd "$(dirname "$0")/.."

command -v npx >/dev/null || { echo "npx is required for the real-server demo"; exit 1; }

HOME_DIR="${BLACKBOX_HOME:-$(mktemp -d)/blackbox-real}"
export BLACKBOX_HOME="$HOME_DIR"
mkdir -p "$HOME_DIR"

ROOT="$(mktemp -d)"
echo "hello from a real file" >"$ROOT/note.txt"
STEPS="$ROOT/steps.json"
cat >"$STEPS" <<JSON
[
  {"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1"}}},
  {"method":"notifications/initialized"},
  {"method":"tools/list","params":{}},
  {"method":"tools/call","params":{"name":"list_directory","arguments":{"path":"$ROOT"}}}
]
JSON

echo "== record against @modelcontextprotocol/server-filesystem"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"list_directory\",\"arguments\":{\"path\":\"$ROOT\"}}}" \
  | ./bin/blackbox run -- npx -y @modelcontextprotocol/server-filesystem "$ROOT" >/dev/null

ID="$(./bin/blackbox sessions | awk 'NR==2 {print $1}')"
./bin/blackbox stats "$ID"

echo
echo "== replay it offline"
./bin/blackbox replay "$ID" -- ./bin/demoagent -steps "$STEPS"

echo
echo "== timeline excerpt"
./bin/blackbox show "$ID" | head -14
