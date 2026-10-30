# MCP protocol notes (target: 2026-07-28)

Working notes for the proxy layer. Source of truth:
<https://modelcontextprotocol.io/specification/2026-07-28>

## Transport framing

- **stdio**: one JSON-RPC message per line, newline-delimited. No embedded newlines.
  The proxy must not rewrite bytes; it reads a line, forwards it verbatim, and parses a
  copy for recording.
- **HTTP**: streamable HTTP. The 2026-07-28 revision removed the initialize handshake and
  sessions. Every request is self-describing, so any request can land on any instance
  behind a round-robin load balancer.

## Statelessness (the big change)

There is no session and no handshake. Consequences for Black Box:

1. We cannot treat a stdio process as a conversation. The session boundary is *ours*
   (process lifetime + explicit run id), not the protocol's.
2. Capabilities and protocol version travel in `_meta` on **every** request:
   - `io.modelcontextprotocol/protocolVersion` (required)
   - `io.modelcontextprotocol/clientCapabilities` (required)
   - `io.modelcontextprotocol/clientInfo` (optional, self-reported)
   - `io.modelcontextprotocol/logLevel` (optional)
3. Optional up-front discovery via `server/discover` returns supported versions and
   capabilities. Clients may skip it.
4. Because identity is self-reported, `clientInfo`/`serverInfo` are for display only and
   must never drive a security decision. We surface them but do not trust them.

## Multi round-trip requests (MRTR)

A long-running tool does not hold a session open. The server replies with
`resultType: "input_required"` plus the requests it needs answered; the client retries the
original call with answers attached in `inputResponses`.

Recorder implication: one logical tool call can appear as **several request/response
frames**. The timeline UI should group by the original call, not by frame, or a single
`deploy` looks like five unrelated calls.

## Result shape

Every result carries `resultType`:

- `"complete"` — final content.
- `"input_required"` — needs more input; see MRTR.
- absent — treat as `"complete"` (backwards compatibility with pre-2026 servers).

Unknown `resultType` values are invalid to the client. Extensions may register more.

## Errors

Standard JSON-RPC codes for protocol failures. The `-32020..-32099` range is reserved for
the spec:

| Code | Name |
| --- | --- |
| -32020 | HeaderMismatch |
| -32021 | MissingRequiredClientCapability |
| -32022 | UnsupportedProtocolVersion |

`-32000..-32019` is legacy; do not assign new codes there. `-32002` (resource not found)
is retired but clients should still accept it from old servers.

## HTTP header routing

`Mcp-Method` and `Mcp-Name` travel as HTTP headers so gateways can route and authorize on
headers without parsing bodies. `Mcp-Method` is the JSON-RPC method; `Mcp-Name` is the
tool/resource/prompt name where applicable.

Proxy implication: we can enforce policy *before* buffering a potentially large body, and
we can attribute traffic to a tool even when the body stream is still in flight.

## What this means for policy

The failure modes worth detecting are protocol-level, not HTTP-level:

- **Rug-pull**: a tool definition changes after the user approved it. Detect by hashing
  the `tools/list` entry (name + description + inputSchema) and comparing across runs.
- **Tool poisoning**: instructions hidden in tool descriptions. Treat descriptions as
  untrusted text; scan and display them prominently rather than trusting annotations.
- **Cross-server shadowing**: two servers exposing the same tool name with different
  behavior. Key policy on `(server, tool)`, never on tool name alone.

## Open questions for later phases

- How do we fingerprint a server when `serverInfo` is self-reported and optional? Plan:
  hash the first observed `server/discover`/`tools/list` payload, not the claimed name.
- `subscriptions/listen` is a request whose response is an open notification stream. The
  recorder needs a streaming event kind, not a single result.
- OTel `traceparent` in `_meta` gives us free cross-process correlation if the client sets
  it; otherwise we mint our own trace id per proxied run.
