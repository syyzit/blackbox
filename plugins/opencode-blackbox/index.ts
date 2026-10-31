import { spawn } from "node:child_process"

// Records OpenCode's built-in tool calls into Black Box, so editor/shell work that
// never touches an MCP server still lands in the same archive.
//
// MCP tools are already recorded by the proxy. Their names are skipped by prefix so a
// call is never counted twice.

type Options = {
  binary?: string
  skipPrefixes?: string[]
}

type ToolEvent = {
  tool: string
  sessionID: string
  agent?: string
  id: string
  input: unknown
  status?: "completed" | "error"
  result?: unknown
  error?: { message?: string }
}

function run(bin: string, args: string[], body: string): Promise<{ code: number; message: string }> {
  return new Promise((resolve) => {
    let message = ""
    try {
      const child = spawn(bin, args, { stdio: ["pipe", "pipe", "pipe"] })
      const timer = setTimeout(() => {
        try {
          child.kill()
        } catch {}
      }, 3000)

      child.stdout.on("data", (chunk) => (message += String(chunk)))
      child.stderr.on("data", (chunk) => (message += String(chunk)))
      child.on("error", () => {
        clearTimeout(timer)
        resolve({ code: 0, message: "" }) // fail open: a missing binary must not brick the agent
      })
      child.on("close", (code) => {
        clearTimeout(timer)
        resolve({ code: code ?? 0, message: message.trim() })
      })
      child.stdin.on("error", () => {})
      child.stdin.end(body)
    } catch {
      resolve({ code: 0, message: "" })
    }
  })
}

function send(bin: string, payload: unknown): Promise<void> {
  let body: string
  try {
    body = JSON.stringify(payload)
  } catch {
    return Promise.resolve()
  }
  return run(bin, ["ingest"], body).then(() => undefined)
}

export default {
  id: "blackbox.builtin-recorder",

  async setup(ctx: any) {
    const options = (ctx.options ?? {}) as Options
    const bin = options.binary || "blackbox"
    const started = new Map<string, number>()

    // Build the skip list from live MCP servers plus explicit prefixes.
    const prefixes = new Set<string>(options.skipPrefixes ?? [])
    try {
      const servers = (await ctx.mcp.list()) as Array<[string, unknown]>
      for (const [name] of servers) {
        prefixes.add(name.replace(/[^a-zA-Z0-9_-]/g, "_") + "_")
      }
    } catch {
      // If the list fails, explicit prefixes still protect against double counting.
    }
    const skip = [...prefixes]

    await ctx.tool.hook("execute.before", async (event: ToolEvent) => {
      started.set(event.id, Date.now())
      if (skip.some((prefix) => event.tool.startsWith(prefix))) return

      const check = await run(
        bin,
        ["check"],
        JSON.stringify({
          v: 1,
          sessionId: event.sessionID,
          callId: event.id,
          tool: event.tool,
          agent: event.agent,
          input: event.input ?? {},
        }),
      )
      if (check.code === 1) {
        throw new Error(check.message || `blackbox policy blocked ${event.tool}`)
      }

      if (check.code === 2) {
        // A require_approval rule in enforce mode: use OpenCode's own permission UI so
        // the prompt looks and behaves like every other approval.
        const resolve = (decision: string, note: string) =>
          run(
            bin,
            ["resolve", "--session", event.sessionID, "--call", event.id, "--decision", decision, "--note", note],
            "",
          )

        try {
          await ctx.permission.assert({
            sessionID: event.sessionID,
            agent: event.agent,
            source: { type: "tool", messageID: event.messageID, id: event.id },
            action: "blackbox",
            resources: [event.tool],
            save: [event.tool],
            metadata: { reason: check.message },
          })
        } catch (err: any) {
          await resolve("deny", " (rejected via permission flow)")
          throw new Error(String(err?.message ?? `approval rejected for ${event.tool}`))
        }
        await resolve("allow", " (approved via permission flow)")
      }
    })

    await ctx.tool.hook("execute.after", async (event: ToolEvent) => {
      const t0 = started.get(event.id)
      started.delete(event.id)

      if (skip.some((prefix) => event.tool.startsWith(prefix))) {
        return
      }

      const payload: any = {
        v: 1,
        source: "opencode",
        sessionId: event.sessionID,
        callId: event.id,
        tool: event.tool,
        agent: event.agent,
        input: event.input ?? {},
        result: event.status === "completed" ? (event.result ?? null) : null,
        error: event.status === "error" ? String(event.error?.message ?? "tool failed") : "",
        status: event.status ?? "completed",
        durationMs: t0 ? Date.now() - t0 : 0,
        at: new Date().toISOString(),
      }

      // The ingest command has a bounded stdin read; keep huge payloads parseable.
      try {
        if (JSON.stringify(payload).length > 1_000_000) {
          payload.input = { truncated: true }
          payload.result = { truncated: true }
        }
      } catch {
        payload.input = { unserializable: true }
        payload.result = null
      }

      await send(bin, payload)
    })
  },
}
