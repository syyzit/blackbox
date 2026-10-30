#!/usr/bin/env bash
# Records a scripted MCP session, then lists and prints it back.
set -euo pipefail
cd "$(dirname "$0")/.."

HOME_DIR="${BLACKBOX_HOME:-$(mktemp -d)/blackbox}"
export BLACKBOX_HOME="$HOME_DIR"

echo "== recording =="
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello from blackbox"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail","arguments":{}}}' \
  | ./bin/blackbox run -- ./bin/fakeserver

echo
echo "== sessions (in $HOME_DIR) =="
./bin/blackbox sessions

ID="$(./bin/blackbox sessions | awk 'NR==2 {print $1}')"
echo
echo "== timeline =="
./bin/blackbox show "$ID"
