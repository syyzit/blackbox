# Black Box

> A flight recorder for AI agents.

Agents now run on your own machine with broad permissions — reading files, calling
APIs, running tools. Black Box sits between the agent and its MCP servers, forwards
traffic **byte-for-byte**, and records every frame so you can answer the only question
that matters at 3am: *what did it actually do?*

Then it replays the run offline, diffs two runs to show where behavior changed, and
gates risky tools before they execute. Local-first, single binary, no cloud, no account.

## What works today

- **Transparent MCP proxy** over stdio and streamable HTTP, byte-exact, with recording
  on the side.
- **Normalized evidence**: method, tool, args, result, error, status, latency, size,
  token usage, cost, and the tool-definition hash each call ran under.
- **SQLite store** (pure Go, WAL, indexed) with per-session and per-tool aggregation.
- **Secret redaction** applied before persistence, audit and rendering, never to the
  live stream.
- **Tool-definition hashing** that flags a definition changed after approval (rug-pull).
- **MRTR-aware grouping**: a call that asks for input and retries is one logical call,
  not several unrelated ones, across the timeline, replay, and diff.
- **Deterministic replay**: answer a live agent from a recording, offline, including the
  server notifications it emitted.
- **SSE as evidence**: streamed frames are recorded as parsed JSON with their
  subscription id and attributed tool, not as opaque lines.
- **Run diff**: same task twice, see exactly which calls diverged.
- **Policy gate**: allow / deny / require-approval per server, tool, and argument regex,
  with an audit log. Approvals happen on `/dev/tty`, never on the MCP stream, so an agent
  cannot answer its own prompt.
- **Web UI**: session browser, scrubbable timeline that follows a live recording,
  frame inspector, run diff.
- **Redacted export**: one JSON incident bundle per session, safe to attach to a bug
  report.
- **Config wrapping**: `init` finds Claude Code, Cursor, VS Code, and OpenCode configs and
  routes selected servers through Black Box, with a backup.
- **One-command dev loop**: `dev` records a server, serves the UI, and follows the run.
- **Retention**: `prune` deletes old sessions or keeps the store under a size budget.
- **Forensics**: full-text `search` across every frame, a `report` that aggregates by
  day, tool, or server, and `policy test` to see what a candidate rule would have done
  to a recorded session.

## Install

```sh
./scripts/install.sh              # builds and installs to ~/.local/bin
# or
make build                        # bin/blackbox and demo binaries
./scripts/demo.sh                 # record -> replay -> diff -> gate, end to end
```

Requires Go 1.26+.

## Wire it into an agent

```sh
blackbox init                      # discover configs, change nothing
blackbox init -wrap all -dry-run   # show the rewrite
blackbox init -wrap all            # back up each config and write
```

It understands the `mcpServers` shape (Claude Code, Cursor, Claude Desktop), VS Code's
`servers`, and OpenCode's `mcp` with array commands. Remote servers are skipped, already
wrapped servers are left alone, and `-config path` targets a single file. Every write
leaves a `*.blackbox-backup-<timestamp>` next to the original.

If you prefer to edit by hand, the result is just a command rewrite:

```json
{
  "mcpServers": {
    "files": {
      "command": "blackbox",
      "args": ["run", "--", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/Users/me/notes"]
    }
  }
}
```

## Record

```sh
blackbox run -- npx -y @modelcontextprotocol/server-filesystem ~/notes

# if your server reports token usage, pass prices to get a real cost figure
blackbox run -price-in 3 -price-out 15 -- your-server
```

Put it in your agent's MCP config exactly where the server command was:

```json
{
  "mcpServers": {
    "files": {
      "command": "blackbox",
      "args": ["run", "--", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/Users/me/notes"]
    }
  }
}
```

Stdout stays a clean protocol stream; the session banner goes to stderr. For HTTP:

```sh
blackbox http -upstream https://example.com/mcp -addr 127.0.0.1:8787
```

## Inspect

```sh
blackbox sessions                       # newest first, with frame counts
blackbox show <id>                      # timeline: direction, status, latency
blackbox stats <id>                     # calls, errors, latency, bytes, cost
blackbox decisions <id>                 # policy audit log
blackbox export <id> -out incident.json # redacted incident bundle
blackbox ui -open                       # the web interface
blackbox dev -- ./my-server             # record + UI + follow, in one command
blackbox prune -older-than 30d          # retention
```

`dev` picks the next free port, opens the browser on the new session, and keeps the UI
up after the server exits so you can inspect what happened.

The UI follows a session while it is being recorded, keeps your selected frame pinned as
new ones arrive, and has `j`/`k` navigation, a frame inspector with raw / args / result /
error / policy tabs, rug-pull and redaction badges, a side-by-side run diff, and an
**Export** button for the redacted incident bundle.

## Replay and diff

```sh
blackbox replay <id> -- ./my-agent       # agent runs against recorded responses
blackbox replay <id>                     # just list the recorded exchanges
blackbox diff <run-a> <run-b>            # missing, extra, reordered, changed results
```

Replay is how you reproduce a bug report without touching the network. Diff is how you
find out that "the agent got worse" actually means it stopped calling `read_file` and
started calling `shell`.

Recordings store redacted payloads, so a replay session returns redacted values for any
secret that was redacted at record time.

## Policy gate

Copy [`docs/policy.example.json`](docs/policy.example.json) to `~/.blackbox/policy.json`
(or pass `-policy`). First matching rule wins; `default` applies otherwise.

```json
{
  "default": "allow",
  "rugPullBlock": true,
  "rules": [
    {"server": "*filesystem*", "tool": "write_*", "action": "require_approval"},
    {"tool": "*", "args": "\"path\"\\s*:\\s*\"/etc/", "action": "deny", "reason": "system path"},
    {"tool": "shell", "action": "deny"}
  ]
}
```

- `allow` forwards the call.
- `deny` blocks it with a JSON-RPC error and records the decision.
- `require_approval` asks on the controlling terminal; a timeout
  (`-approval-timeout`, default 1m) or a non-interactive session denies.
- When `rugPullBlock` is on, a tool whose definition hash changed since the previous run
  is denied until you intentionally re-approve it.

Every decision is written to the audit log and surfaced in the UI.

## Ask questions across sessions

```sh
blackbox search "ETIMEDOUT" --since 7d
blackbox search write_file --status tool_error
blackbox search needle --tool echo --limit 20
```

Search runs on SQLite FTS5 over method, tool, args, result, and error, so a tool name, a
path, an error string, or a JSON value all work. The same search box is in the UI sidebar;
clicking a hit opens that session at that frame.

```sh
blackbox report --since 7d
blackbox report --since 30d --by tool
blackbox report --since 0 --by server
```

```
DAY         CALLS  ERRORS  BLOCKED  AVG    P95    MAX    BYTES  COST
2026-10-31  412    7       3        210ms  840ms  1.2s   12.4MB $0.310
TOTAL       412    7       3                             12.4MB $0.310
```

Test a policy before you ship it:

```sh
blackbox policy test 20261031-154220 --policy candidate.json
```

```
calls    2
result   allow 1 · deny 1 · require approval 0

DECISION  TOOL  RECORDED    ARGS
deny      fail  tool_error  {"name":"fail","arguments":{}}
```

Add `--fail-on-deny` to make it a CI gate.

## Retention

```sh
blackbox prune -dry-run -older-than 30d
blackbox prune -older-than 30d -max-size 500MB
blackbox prune -max-size 2GB -vacuum=false
```

`-older-than` accepts `30d`, `2w`, `12h`, or any Go duration. `-max-size` accepts `500MB`,
`2GB`, or bytes and deletes the oldest sessions until the store fits. Deletes cascade to
events, tool definitions, and policy decisions; `VACUUM` (on by default) reclaims the
file. Run it when no recorder is active.

## How it works

```
agent  ──stdin──▶  blackbox  ──▶  MCP server
       ◀─stdout──            ◀──
                        │
                        ├──▶ policy gate
                        └──▶ SQLite evidence
```

The proxy reads newline-delimited JSON-RPC frames, evaluates policy, forwards the frame
unchanged, and parses a copy for recording. A recorder failure never breaks the stream —
observation is always subordinate to the thing being observed.

| Package | Responsibility |
| --- | --- |
| `internal/proxy` | stdio and HTTP pumps, normalization, policy enforcement |
| `internal/policy` | rule matching, decisions, audit records |
| `internal/replay` | exchange engine, replay server, behavioral diff |
| `internal/store` | SQLite schema, sessions, events, stats, ledger, decisions |
| `internal/redact` | credential patterns applied before persist and render |
| `internal/event` | the recorded frame type and sink contract |
| `internal/jsonrpc` | permissive JSON-RPC envelope, never rejects unknown frames |
| `internal/ui` | embedded web UI and JSON API |
| `internal/mcptest` | fake MCP server and client harness |

Design notes for the stateless `2026-07-28` protocol, including MRTR call grouping and the
three MCP-specific attack patterns, live in [`docs/protocol-notes.md`](docs/protocol-notes.md).

## Development

```sh
make build   # bin/blackbox, bin/fakeserver, bin/demoagent
./scripts/install.sh "$HOME/.local/bin"
make test    # go test ./...
make vet     # go vet ./...
make demo    # the full tour against the bundled server
# dogfooding the recorder on its own development: docs/dogfood.md
./scripts/demo-real.sh   # the same tour through the real filesystem MCP server
```

CI runs `go vet` and `go test -race` on every push.

## Limitations

- MCP does not define billing. Cost is derived from token usage only when a server
  reports it, using `-price-in` / `-price-out` (USD per million tokens). Most servers
  report nothing, so cost stays zero. Latency and bytes are always recorded. The bundled
  fake server reports tokens so the demo can show a non-zero figure.
- Replay matches calls by method, tool, and arguments, and groups MRTR rounds by the
  recorded `requestState`. A nondeterministic server that returns different payloads for
  identical calls will show up as `result_changed`.
- Search indexes method, tool, args, result, and error. It does not index the raw frame
  separately, since that duplicates the fields above.
- The UI is read-only: approvals happen in the terminal that launched the recorder.
- An approval prompt blocks the proxied stream while it waits, bounded by
  `-approval-timeout`. A fully asynchronous approval queue is future work.
- Tested against the protocol spec and the bundled fake server; real-world servers vary.

## License

MIT
