# MCP gaps

An audit of the MCP integration in `internal/mcp`, driven by the question
"can we set servers up and get full functionality?" It confirms the open
findings in `docs/codex_review.md` (G1–G3) and `docs/codex_review2.md`
(R2-21), and adds gaps those reviews did not list.

Everything below was verified against the current source; file:line
references point at the code as of this writing.

## What works today

- **Config validation** — name grammar, the `__` namespace-separator ban,
  duplicate server names, and command/args/env are all rejected up front
  (`internal/config/config.go:1123-1145`).
- **Tool namespacing** — MCP tools are exposed as `server__tool`, so a
  server can never shadow a built-in (`internal/mcp/mcp.go:125`).
- **Failure isolation** — a missing or failing server is reported and
  skipped; the session starts anyway (`internal/mcp/mcp.go:59-72`). MCP
  tool errors come back as tool errors the model can act on
  (`internal/mcp/mcp.go:165-172`).
- **SDK-backed protocol** — the official Go SDK
  (`github.com/modelcontextprotocol/go-sdk v1.7.0`), no hand-rolled wire
  code.

## Gaps

### 1 — High: tool calls have no timeout

Evidence: `Server.call` runs `Session.CallTool` on the caller's context
with no deadline (`internal/mcp/mcp.go:148`). No `WithTimeout` exists
anywhere on the MCP call path. Every comparable integration bounds its
calls: LSP uses `RequestTimeout` (`internal/lsp/ops.go:95`), Brave uses
`braveSearchTimeout` (`internal/tools/brave.go:227`), exec uses
`e.Timeout` (`internal/tools/exec.go:769`).

Impact: one hung MCP server stalls the entire turn until the user
interrupts manually. The 10-second `ConnectTimeout` (`mcp.go:33`) covers
only the handshake.

Solution: wrap each call in a bounded context with a sensible default
(configurable per server), so a wedged server costs one tool result, not
the turn.

### 2 — High: non-text and structured tool results are dropped

Evidence: `Server.call` appends only `*sdk.TextContent` and ignores all
other content (`internal/mcp/mcp.go:156-163`). `res.StructuredContent`
is never read.

Impact: images, audio, embedded resources, resource links, and
structured content vanish. A successful image-only tool returns an empty
success, so vision and UI cannot use it; structured-output servers lose
their payload entirely.

Solution: map MCP content types into `tools.Result` (text, images,
display/resource references), preserve structured JSON, and reject
unsupported content explicitly rather than silently dropping it.

### 3 — High: tool discovery reads only the first page

Evidence: `Server.loadTools` calls `ListTools(ctx, nil)` once and never
follows the cursor (`internal/mcp/mcp.go:106-107`). The MCP list
operations are cursor-paginated.

Impact: servers with more than one page of tools silently expose only a
prefix, with a misleading tool count in the header. Tools past page one
do not exist as far as the model is concerned.

Solution: follow `NextCursor` until empty under a total/tool-count
bound, with SDK-backed pagination tests (`internal/mcp` has no coverage).

### 4 — Medium: stdio transport only

Evidence: `connectOne` uses `sdk.CommandTransport` exclusively
(`internal/mcp/mcp.go:91`); the config accepts only a command, args, and
env (`internal/config/config.go:199-203`).

Impact: hosted and remote MCP servers (streamable HTTP / SSE endpoints)
cannot be configured at all — only servers that run as local
subprocesses.

Solution: accept a `url` alternative to `command` and use the SDK's HTTP
transport, with the same non-fatal connect semantics.

### 5 — Medium: no `tools/list_changed` refresh

Evidence: `sdk.NewClient` is created with no notification handling
(`internal/mcp/mcp.go:90`) and tools are loaded once at connect into a
static slice (`internal/mcp/mcp.go:134-137`).

Impact: servers that add or remove tools mid-session (login flows,
dynamic registries) stay stale until restart, and stale calls can hit
tool names the server no longer provides.

Solution: advertise the capability, listen for the notification, rebuild
the tool set atomically, and update the agent's tool definitions at a
safe point.

### 6 — Medium: server processes inherit the daemon's entire environment

Evidence: `cmd.Env = append(cmd.Environ(), cfg.Env...)`
(`internal/mcp/mcp.go:87`). Model-run shell commands were hardened to an
allowlist (`Exec.commandEnv`, `internal/tools/exec.go:214-232`, R2-16),
but the MCP path was not.

Impact: every configured MCP server subprocess sees the daemon's full
environment — provider API keys and unrelated secrets included — not
just the `env` entries the user configured. A third-party server is an
untrusted process holding every credential the harness has.

Solution: start from the same allowlist the shell path uses, plus the
explicit `env` entries from the server's config block.

### 7 — Medium: no reconnect and no status surface

Evidence: there is no restart logic and no `/mcp` command anywhere in
the codebase; connect failures only print to stderr
(`internal/daemon/spawn.go:97-99`).

Impact: a server that dies mid-session is dead for the rest of the
session, with every call failing and no way to see or fix that from the
UI. In a daemon, the stderr diagnostic is not even visible to the user.

Solution: detect transport failure, attempt a bounded reconnect, and
surface per-server status (connected / tools / last error) in the TUI.

### 8 — Medium: only tools surface; prompts and resources unsupported

Evidence: only `ListTools`/`CallTool` are used (`internal/mcp/mcp.go`);
the client never calls `ListPrompts`, `ListResources`, or `ReadResource`.

Impact: MCP servers exposing prompts or resources (git, filesystem, and
others) keep those features invisible to the model; only their tools
work.

Solution: either adapt prompts/resources into the tool set (e.g. a
`<server>__read_resource` tool) or document them explicitly as out of
scope.

### 9 — Low: minor defects

- **Silent empty-schema fallback** — if marshalling `InputSchema` fails,
  the tool silently gets `{"type":"object","properties":{}}`
  (`internal/mcp/mcp.go:114-119`), so the model gets no argument hints
  and calls likely fail with no diagnostic.
- **Hardcoded client version** — `sdk.Implementation{Version: "0.1.0"}`
  (`internal/mcp/mcp.go:90`) while the application is far past it; wrong
  version in capability negotiation.
- **Per-session process fan-out** — every daemon session, including each
  spawned worker, starts its own copy of every configured server
  (`internal/daemon/spawn.go:89-101`). N workers means N sets of MCP
  server processes, handshakes, and any server-side quota they consume.
  Isolation is intentional, but it scales linearly with session count.
- **Duplicate tool names first-wins** — if one server lists the same
  tool name twice, the set keeps the first (`internal/tools/tools.go`),
  with execution possibly bound to the other (C4 in `codex_review.md`).

## Suggested fix order

1. Bounded call timeouts (#1) — smallest change, protects every session.
2. Content-type mapping (#2) — unlocks image and structured-output
   servers.
3. Cursor pagination (#3) — required for large catalogs.
4. Environment allowlisting (#6) — security; mirrors the shell fix.
5. Reconnect + status (#7), `tools/list_changed` (#5), HTTP transport
   (#4), prompts/resources (#8) — larger surface, order by need.

## Testing note

`internal/mcp` had no test coverage at review time (`codex_review.md`,
test matrix). Any fix above should land with SDK-backed tests — the SDK
supports in-process server pairs that make pagination, content types,
and list-change notifications testable without spawning real servers.