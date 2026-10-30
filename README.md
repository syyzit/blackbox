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
  token usage, and cost per call.
- **SQLite store** (pure Go, WAL, indexed) with per-session and per-tool aggregation.
- **Secret redaction** applied before persistence and rendering, never to the live stream.
- **Tool-definition hashing** that flags a definition changed after approval (rug-pull).
- **Deterministic replay**: answer a live agent from a recording, offline.
- **Run diff**: same task twice, see exactly which calls diverged.
- **Policy gate**: allow / deny / require-approval per server, tool, and argument regex,
  with an audit log. Approvals happen on `/dev/tty`, never on the MCP stream, so an agent
  cannot answer its own prompt.
- **Web UI**: session browser, scrubbable timeline, frame inspector, run diff.

## Install

```sh
./scripts/install.sh              # builds and installs to ~/.local/bin
# or
make build                        # bin/blackbox and demo binaries
./scripts/demo.sh                 # record -> replay -> diff -> gate, end to end
```

Requires Go 1.26+.

## Record

```sh
blackbox run -- npx -y @modelcontextprotocol/server-filesystem ~/notes
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
blackbox ui -open                       # the web interface
```

The UI has a scrubbable timeline, `j`/`k` navigation, a frame inspector with raw / args /
result / error / policy tabs, rug-pull and redaction badges, and a side-by-side run diff.

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
- `require_approval` asks on the controlling terminal; a timeout or a non-interactive
  session denies.
- When `rugPullBlock` is on, a tool whose definition hash changed since the previous run
  is denied until you intentionally re-approve it.

Every decision is written to the audit log and surfaced in the UI.

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
make test    # go test ./...
make vet     # go vet ./...
make demo    # the full tour
```

CI runs `go vet` and `go test` on every push.

## Limitations

- MCP does not define billing. Cost is derived from token usage only when a server
  reports it, and is zero otherwise. Latency and bytes are always recorded.
- Replay matches calls by method, tool, and arguments. A nondeterministic server that
  returns different payloads for identical calls will show up as `result_changed`.
- The UI is read-only: approvals happen in the terminal that launched the recorder.
- Tested against the protocol spec and the bundled fake server; real-world servers vary.

## License

MIT
