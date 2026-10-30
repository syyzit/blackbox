#!/usr/bin/env bash
# End-to-end tour: record, inspect, replay, diff, and gate.
set -euo pipefail
cd "$(dirname "$0")/.."

HOME_DIR="${BLACKBOX_HOME:-$(mktemp -d)/blackbox}"
export BLACKBOX_HOME="$HOME_DIR"
mkdir -p "$HOME_DIR"

newest() { ./bin/blackbox sessions | awk 'NR==2 {print $1}'; }

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

step "1. record a run"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello from the agent"}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add","arguments":{"a":2,"b":40}}}' \
  | ./bin/blackbox run -price-in 3 -price-out 15 -- ./bin/fakeserver >/dev/null
A="$(newest)"
./bin/blackbox stats "$A"

step "2. replay it offline against the recording"
./bin/blackbox replay "$A" -- ./bin/demoagent

step "3. record a different run, then diff the two"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":2,"b":40}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fail","arguments":{}}}' \
  | ./bin/blackbox run -- ./bin/fakeserver >/dev/null
B="$(newest)"
./bin/blackbox diff "$A" "$B" || true

step "4. policy gate: deny a tool and audit the decision"
cat >"$HOME_DIR/policy.json" <<'JSON'
{
  "default": "allow",
  "rules": [
    {"tool": "fail", "action": "deny", "reason": "demo: destructive tool"}
  ]
}
JSON
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fail","arguments":{}}}' \
  | ./bin/blackbox run -- ./bin/fakeserver 2>/dev/null | tail -1
C="$(newest)"
./bin/blackbox decisions "$C"

step "5. inspect in the browser"
echo "blackbox ui -open    # sessions, scrubber, frame detail, run diff"
