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

## The shell server

`cmd/mcpshell` is a small MCP server with one tool, `run_command`, so builds, tests, and
read-only git inspection are recorded too:

```jsonc
"shell": {
  "type": "local",
  "command": [
    "blackbox", "run", "--",
    "/absolute/path/to/blackbox/bin/mcpshell",
    "--root", "/absolute/path/to/blackbox"
  ],
  "codemode": false
}
```

It is deliberately narrow:

- **No shell.** Programs run via `exec`, so pipes, `&&`, and redirection are not
  interpreted; arguments are passed literally.
- **Allowlist.** `go`, `git`, `make`, and a few read-only utilities. Git is limited to
  read-only subcommands (`status`, `diff`, `log`, `show`, ...).
- **Rooted.** A `cwd` must resolve inside `--root`.
- **Bounded.** Default 30s timeout and a 64KB output cap.

Build it with `make build`; `install.sh` installs only the main binary.

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

4. **Only MCP traffic is visible.** Built-in file and shell tools bypass the servers
   entirely, so the recording is as complete as the agent's willingness to use `repo_*`
   and `shell_run_command`.

5. **The shell server works end to end.** A real OpenCode turn ran
   `shell_run_command {"command":"go","args":["version"]}`; Black Box recorded the call,
   its 41ms duration, and the full output. `blackbox policy test` with a candidate rule
   denying `run_command` reports `deny 1` against that recording.

6. **Handshakes were inflating call counts.** `initialize` and `tools/list` responses were
   counted as "calls" in `stats` and `report`. Both now count only `tools/call` responses,
   which is what a person means by a call.

## The plugin: built-in tools too

OpenCode's built-in tools (`shell`, `read`, `write`, `edit`, ...) never touch an MCP
server, so the proxy cannot see them. `plugins/opencode-blackbox` hooks
`tool.execute.before` / `tool.execute.after`, times the call, and pipes it to:

```sh
blackbox ingest < event.json
```

which writes a normal `tools/call` pair into a session named `oc-<opencode-session>`.
Configure it in the local `opencode.json`:

```jsonc
"plugins": [
  {
    "package": "./plugins/opencode-blackbox",
    "options": {
      "binary": "/absolute/path/to/blackbox",
      "skipPrefixes": ["repo_", "shell_"]
    }
  }
]
```

`skipPrefixes` (plus the live MCP server list, read at setup) keeps MCP calls from being
recorded twice. Verified against a real turn: the built-in `shell` call appeared as an
`opencode builtins` session with its timing and output, while a `repo_list_directory`
call landed only in the MCP session.

Because built-ins and MCP traffic may live in different sessions, the coverage check
now classifies changes against **every event in the session's time window**, not just the
session's own frames. A built-in write recorded by the plugin still counts as `covered`.

## Coverage catches the bypass

The point of the coverage check is to make the cooperative boundary honest. Verified with
a live recording: a file was modified and another created from outside the MCP servers
while a session was running.

```
blackbox · coverage: 2 changed, 2 not seen in a recorded write
  unrecorded a.txt (modified)
  unrecorded b.txt (created)
```

The UI shows the same thing as a `coverage gap` badge and a coverage tab. A change named
in a recorded write call is `covered`; a change that could plausibly come from a recorded
shell call is `unverified`; everything else is `unrecorded`.

The window is the MCP connection, not the agent turn, for the same reason sessions are:
OpenCode keeps the server process alive across prompts.

## Shadow mode in practice

Before enforcing rules on built-ins, run the policy in `~/.blackbox/policy.json` with
`"mode": "shadow"`. The plugin calls `blackbox check` for every built-in tool, the proxy
checks every MCP call, and both only record what would have happened.

A real built-in shell call under a `require_approval` rule:

```
$ blackbox decisions oc-ses_e6ce59bc9ffe
TIME      ACTION            MODE    APPROVED  TOOL   REASON
18:55:44  require_approval  shadow  false     shell  built-in shell
```

The command still ran and its output was returned. The same rule against the MCP shell
server:

```
18:56:00  require_approval  shadow  false  run_command  MCP shell (shadow: not enforced)
```

`blackbox report` gains a `WOULD` column so shadow candidates are visible next to enforced
blocks. Once a rule looks relevant, change `mode` to `enforce`; nothing else moves.

Note: editing the plugin source does not hot-reload it. Touching `opencode.json` reloads
the plugin; the proxy picks up a new policy on its next connection.

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
