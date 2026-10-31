# Dogfooding: recording the agent that builds Black Box

Black Box can watch itself being developed. The project-local `opencode.json` declares a
single MCP server named `repo`: the standard filesystem server, pointed at this
repository, wrapped in `blackbox run`.

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "repo": {
        "type": "local",
        "command": [
          "blackbox", "run", "--",
          "npx", "-y", "@modelcontextprotocol/server-filesystem",
          "/absolute/path/to/blackbox"
        ],
        "cwd": ".",
        "codemode": false
      }
    }
  }
}
```

`codemode: false` exposes the tools directly as `repo_*`, which keeps the loop simple.
Use the absolute path to the binary if the agent's process does not inherit your `PATH`.

`AGENTS.md` tells the agent to prefer `repo_*` for file work and to fall back only when
the server is unavailable. The config file is gitignored because it contains a local path;
`AGENTS.md` is committed because the guidance is portable.

## What the first real runs showed

Two OpenCode turns were run against this setup (`list_directory`, then
`read_text_file`). Both tool calls went through the proxy and were recorded, which is
the thing the fake-server tests could not prove.

Four findings that only a real agent could surface:

1. **A session is an MCP connection, not an agent turn.** OpenCode's background service
   kept the server process alive, so two separate prompts landed in the same Black Box
   session two minutes apart. A third session came from `opencode mcp list`, which starts
   the server just to inspect it. Diffing "two runs" therefore needs an explicit run
   boundary, not just a session id.

2. **Legacy servers still issue server-to-client requests.** OpenCode defaults to the
   legacy handshake, so `roots/list` arrived from the server mid-session. The recorder was
   pairing that reply through the client-request table, which could collide with a client
   id. It now keeps a separate table per direction, with a regression test.

3. **`stats` counted those server requests as calls.** A pending `roots/list` inflated the
   call count. Stats now counts only completed responses.

4. **Only MCP traffic is visible.** Built-in file and shell tools bypass the server
   entirely, so the recording is as complete as the agent's willingness to use `repo_*`.
   Shell commands and test runs remain invisible.

## The loop

```sh
blackbox sessions            # what just happened
blackbox report --since 1h   # calls, errors, latency, blocks
blackbox search <term>       # find a specific frame
blackbox policy test <id> --policy candidate.json
blackbox diff <run-a> <run-b>
```

The useful cycle is: make a change, record, inspect the report, change the approach,
record again, and diff the two sessions to see where the behavior diverged.
