#!/usr/bin/env bash
# Record a call that needs approval, approve it from the CLI, and show the audit.
set -euo pipefail
cd "$(dirname "$0")/.."

HOME_DIR="${BLACKBOX_HOME:-$(mktemp -d)/blackbox-approve}"
export BLACKBOX_HOME="$HOME_DIR"
mkdir -p "$HOME_DIR"

POLICY="$HOME_DIR/policy.json"
cat >"$POLICY" <<'JSON'
{
  "default": "allow",
  "mode": "enforce",
  "rules": [
    {"tool": "echo", "action": "require_approval", "reason": "demo approval"}
  ]
}
JSON

echo "== start a headless session that will block on approval"
( printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"approved by cli"}}}'; sleep 20 ) \
  | ./bin/blackbox run -policy "$POLICY" -- ./bin/fakeserver >"$HOME_DIR/out.jsonl" 2>"$HOME_DIR/err.log" &
RUN_PID=$!

for _ in $(seq 1 80); do
  ID="$(./bin/blackbox approvals 2>/dev/null | awk 'NR==2 {print $1}')"
  [ -n "$ID" ] && break
  sleep 0.25
done
if [ -z "${ID:-}" ]; then
  echo "no pending approval appeared" >&2
  kill "$RUN_PID" 2>/dev/null || true
  exit 1
fi

echo
echo "== pending"
./bin/blackbox approvals

echo
echo "== approve $ID"
./bin/blackbox approve "$ID"

sleep 1
echo
echo "== the call proceeded"
grep -o '"text":"approved by cli"' "$HOME_DIR/out.jsonl" || tail -5 "$HOME_DIR/err.log"

echo
echo "== audit"
SESSION="$(./bin/blackbox sessions | awk 'NR==2 {print $1}')"
./bin/blackbox decisions "$SESSION"

kill "$RUN_PID" 2>/dev/null || true
wait "$RUN_PID" 2>/dev/null || true
