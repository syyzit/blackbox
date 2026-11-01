# Working on Black Box

## Dogfooding: record your own file work

This repository can route its own MCP filesystem server through Black Box, so that an
agent editing Black Box is recorded by Black Box. The project-local `opencode.json`
declares a server named `repo`; when it is present, its tools appear as `repo_*`.

- Prefer the `repo_*` tools for reading and writing files inside this repository. That
  is the point of the experiment: every file operation becomes a recorded frame.
- If the `repo` server is unavailable or a tool errors, fall back to the built-in tools
  and say that you did.
- Never use the `repo_*` tools on paths outside this repository.
- Prefer `shell_run_command` for builds, tests, and read-only git inspection. It runs a
  program directly, with no shell: pass arguments in `args`, and do not expect pipes,
  redirection, or `&&` to work.
- `shell_run_command` allows `go`, `git`, `make`, and a few read-only utilities. Git is
  restricted to read-only forms (listing branches, tags and remotes, not changing them);
  commits and pushes still need the normal shell and are not recorded. Path arguments
  must stay inside the repository, and `find` cannot use `-exec` or `-delete`. This is a
  guard rail, not a sandbox: `go` and `make` run code from the repository.
- Both servers are proxy-wrapped, so file and command work appear in `blackbox sessions`.
  Each MCP connection is its own session, so the two servers produce separate sessions.

After a task, inspect what happened:

```sh
blackbox sessions
blackbox report --since 1h
blackbox search <term>
blackbox diff <run-a> <run-b>
```

The interesting loop is `diff`: attempt a change, record the session, attempt it a
different way, then diff the two runs to see exactly where the approaches diverged.

Two things to know when reading the recordings:

- A session is an MCP connection, not an agent turn. The background service may keep the
  server alive across prompts, so one session can contain several tasks; use the event
  times to tell them apart.
- Server-initiated requests such as `roots/list` appear as frames in the opposite
  direction. They are not calls.

See `docs/dogfood.md` for the setup and the findings from the first real runs.

## Normal development

```sh
make build
go test ./...
```
