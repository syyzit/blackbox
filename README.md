# Black Box

> A flight recorder for AI agents.

Agents now run on your own machine with broad permissions — reading files, calling
APIs, running tools. Black Box sits between the agent and its MCP servers, forwards
traffic **byte-for-byte**, and records every frame so you can answer the only question
that matters at 3am: *what did it actually do?*

Local-first, single binary, no cloud, no account.

## Status

Phase 1 of the build, plus the SQLite store. Working today:

- transparent MCP **stdio** proxy, byte-exact, with recording on the side
- **streamable HTTP** reverse proxy with `Mcp-Method` / `Mcp-Name` header routing
- normalized events: args, result, error, status, latency, size, token usage, cost
- secret redaction applied to the recording, never to the live stream
- tool-definition hashing with cross-session **rug-pull** flagging
- **SQLite** session store (pure Go, WAL) with per-session aggregation
- `blackbox run`, `http`, `sessions`, `show`, `stats`
- fake MCP server + client harness for integration tests

Coming next: web UI, deterministic replay, run diff, and the policy gate. See
[Roadmap](#roadmap).

## Quick start

```sh
make build
./scripts/demo.sh
```

Or record a real server:

```sh
blackbox run -- npx -y @modelcontextprotocol/server-filesystem ~/notes
blackbox sessions
blackbox show <session-id>
```

The session banner is written to **stderr**; stdout stays a clean MCP stream, so Black
Box can be dropped in as a server command without the agent noticing.

## How it works

```
agent  ──stdin──▶  blackbox  ──▶  MCP server
       ◀─stdout──            ◀──
                        │
                        └──▶ event log (per session)
```

The proxy reads newline-delimited JSON-RPC frames, forwards them unchanged, and parses a
copy for indexing. A recorder failure never breaks the stream — observation is always
subordinate to the thing being observed.

| Package | Responsibility |
| --- | --- |
| `internal/proxy` | stdio pump, byte-exact forwarding, event normalization |
| `internal/event` | the recorded frame type and sink contract |
| `internal/jsonrpc` | permissive JSON-RPC envelope, never rejects unknown frames |
| `internal/store` | session lifecycle and the append-only event log |
| `internal/mcptest` | fake MCP server and client harness |
| `cmd/blackbox` | CLI |

Design notes for the stateless `2026-07-28` protocol, including MRTR grouping and the
three MCP-specific attack patterns we plan to catch, live in
[`docs/protocol-notes.md`](docs/protocol-notes.md).

## Roadmap

- [x] Transparent stdio proxy with byte-exact passthrough
- [x] Per-session event log and CLI timeline
- [x] SQLite event store with latency and cost aggregation
- [x] Streamable HTTP transport with `Mcp-Method` / `Mcp-Name` header routing
- [x] Secret redaction on persist and render
- [x] Rug-pull detection via tool-definition hashing
- [ ] Web UI: session list, scrubbable timeline, frame inspector
- [ ] Deterministic replay against recorded responses
- [ ] Run diff: what changed between two executions of the same task
- [ ] Policy gate: allow / deny / require-approval per tool
- [ ] Single-binary packaging with the UI embedded

## Development

```sh
make build   # bin/blackbox and bin/fakeserver
make test    # go test ./...
make vet     # go vet ./...
make demo    # scripted record -> list -> show
```

Requires Go 1.25+.

## License

MIT
