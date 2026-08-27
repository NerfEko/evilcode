# Codex review 2

Date: 2026-08-26  
Reviewed revision: `02fce08` plus the uncommitted working-tree changes present during the review  
Scope: all production Go packages, commands, installer/updater, configuration, persistence, daemon protocol, TUI, tools, MCP/LSP integrations, tests, probes, and project structure

## Executive summary

The project has a stronger defensive core than its size initially suggests. The ordinary build, vet, race suite, shuffled suite, and module verification pass. Session persistence, process cleanup, socket ownership, provider stream termination, strict tool argument decoding, and several previous concurrency defects all have useful regression coverage.

It is still not release-ready. The highest-risk issue is the daemon wire protocol: every completed turn can carry the entire conversation, including raw image bytes encoded into JSON, while clients reject frames above 8 MiB. A long or image-heavy session can therefore disconnect a healthy attached client by design. The replay ring compounds this with incorrect overwrite accounting and incomplete size estimates. The TUI then discards staged attachments before observing send failure and ignores most `Agent.Run` errors, so the same failure can look like a lost prompt or lost image.

The next priorities are effect-aware tool scheduling, filesystem confinement on older kernels and during directory creation, configuration/runtime transitions, and UI paths that eagerly load or render entire files. Release engineering also needs attention: the probe suite currently fails, one concurrency test is flaky, 23 Go files are not formatted, important command packages have little or no coverage, and there is no CI definition in the repository.

Recommended order:

1. Redesign daemon framing/snapshots and correct ring accounting.
2. Preserve input on transport failure and surface every turn-start error.
3. Serialize mutating tools and harden confinement.
4. Fix probe/gofmt gates and add CI.
5. Make provider/model/credential transitions atomic and diagnosable.
6. Split the TUI and daemon monoliths along state-ownership boundaries.

## Verification performed

| Check | Result |
|---|---|
| `go build -o /tmp/evilcode-codex-review2 ./cmd/evilcode` | Pass |
| `go vet ./...` | Pass |
| `go test ./... -count=1` | Failed once, then passed; see R2-06 |
| `go test ./... -shuffle=on -count=1` | Pass |
| `go test -race ./... -count=1` | Pass |
| `go test -cover ./... -count=1` | Pass; important gaps listed in R2-39 |
| `go mod verify` | Pass |
| `git diff --check` | Pass at the time checked |
| `gofmt -l .` | Fail: 23 files listed |
| `EVILCODE_BIN=/tmp/evilcode-codex-review2 go test -tags probe ./probe/... -count=1` | Fail; see R2-05 |

`staticcheck`, `govulncheck`, and `shellcheck` were not installed, so this review does not claim results from them. The worktree was being edited concurrently during the review; this document is the only file intentionally added by the review.

## Critical and high-priority findings

### R2-01 — Server-to-client frames inevitably outgrow the client's limit

Severity: critical  
Evidence: `internal/daemon/server.go:1016-1047`, `internal/daemon/server.go:1451-1481`, `internal/daemon/server.go:1987-1997`, `internal/daemon/client.go:37-39`, `internal/tools/fs_image.go:28-55`

`publishEvent` attaches the complete post-turn conversation to every `EventTurnEnd`. Initial snapshots also copy all messages and their `Images`. The server directly JSON-encodes those objects without checking their size, while the client scanner has a fixed 8 MiB maximum. JSON base64 expands image data by roughly one third. A single image read may be 20 MiB, and TUI attachments allow multiple 4 MiB images. Long text-only histories eventually reach the same failure.

Impact: a normal completed turn or attach can make the daemon disconnect the client. Because a full history is serialized after every turn, encoding and bandwidth are also O(total history) per turn and O(n²) over a long session.

Fix: keep binary payloads in the existing blob store and send content-addressed references; send incremental state revisions rather than a full history on every turn; page or chunk initial history; use one explicit length-prefixed protocol limit in both directions; and reject or downgrade an oversized server message before writing it.

### R2-02 — Replay-ring byte accounting is wrong and does not bound retained memory

Severity: critical  
Evidence: `internal/daemon/ring.go:45-84`, `internal/daemon/server.go:1340-1362`, `internal/daemon/server.go:1987`

When the fixed-size ring wraps, `Add` overwrites `r.buf[r.next]` but never subtracts the overwritten event's size before adding the replacement. `r.bytes` therefore grows incorrectly and eventually evicts far more history than required. Conversely, `eventBytes` omits `SnapshotMessages`, tool-call arguments, ask payloads, background state, and several other retained fields. The largest current field—the complete history placed on turn-end events—is not counted at all. The loop also intentionally retains one event even if that event alone exceeds 16 MiB.

Each subscriber channel and connection output channel is buffered for 256 *items*, not bytes. Hundreds of large diffs, displays, or images can therefore retain hundreds of megabytes per slow client even if the ring were correct.

Fix: subtract the overwritten slot before replacement; test more than `RingSize` small events; calculate an encoded or conservative deep size; keep bulk payloads out of events; and use byte-budgeted client queues with a clear resync/disconnect policy.

### R2-03 — A rejected turn can silently lose attachments and report success in the UI

Severity: critical  
Evidence: `internal/tui/app.go:4026-4101`, `internal/daemon/client.go:59-75`, `internal/attachcmd/attach.go:230-243`

`dispatchTurn` calls `TakeAttachments` before `Agent.Run`. Its goroutine handles only `agent.ErrBusy`; every other error is dropped. In attached mode, a frame that exceeds the client's limit is rejected locally, but the UI has already cleared the editor, appended the prompt, and consumed its attachments. Hidden runs similarly discard all returned errors.

Impact: transport, persistence, authentication, and protocol errors can look like a submitted turn while no turn started. Images are no longer available for retry.

Fix: make dispatch return a typed completion message to the Bubble Tea update loop. Do not commit the user row or consume staged input until the daemon acknowledges `TurnStart`; on failure restore text/images and show the error. Apply the same rule to queued and hidden turns.

### R2-04 — The side panel can freeze or exhaust memory on large files

Severity: high  
Evidence: `internal/tui/app.go:5685-5729`, `internal/tui/sidepanel.go`

`fileDiffContent` opens the changed path and uses unbounded `io.ReadAll`, then splits the complete file into strings. The side-panel renderer builds rows and syntax-highlights the complete body. A click on a large generated file can therefore allocate several copies and block the UI loop.

`readQuickView` has the opposite correctness problem: it reads only `MaxResultBytes+1` and then passes that prefix to `tools.Truncate`. The resulting notice says roughly one byte was omitted and uses the one-byte tail of the prefix, even if the actual file is gigabytes long. It neither shows the real tail nor reports the real omitted size.

Fix: stat first, enforce a byte/line ceiling, stream a window around the relevant diff line, highlight only visible rows, and render an accurate paging/truncation notice. Do not use the model-facing `Truncate` helper on an already truncated prefix.

### R2-05 — The shipped probe gate currently fails

Severity: high  
Evidence: `probe/`

The tagged probe command failed in seven scenarios: `tui-difflong`, `tui-panel`, `tui-picker`, `tui-scroll`, `tui-swarm`, `tui-todos`, and `tui-widgets`. The first five showed layout/help/golden drift; the latter two timed out waiting for expected UI state. These are user-visible terminal workflows, not isolated unit-test details.

Fix: determine which diffs are intentional, update goldens only for intentional output, repair the two timeouts, and run the full probe suite in CI against the built binary.

### R2-06 — The live-worker concurrency test is flaky because its helper resurrects finished workers

Severity: high for the gate; medium for production risk  
Evidence: `internal/daemon/reserve_worker_test.go:69+`

One full suite run failed `TestConcurrentSpawnsStayUnderTheLiveLimit` after observing six workers against a cap of four. Repeating the exact test 200 times did not reproduce it. The test helper `holdWorkers` scans sessions and replaces closed `done` channels even after `markFinished` has released their reservations. Under unlucky scheduling it can make sequentially finished workers appear live at the same time. The production reservation counter is protected by the server lock; the evidence points to the helper, not a demonstrated cap breach.

Fix: have the test hold workers before they finish, never mutate terminal sessions back into a live-looking state, and assert both the reservation count and terminal lifecycle. Keep a stress count in CI.

### R2-07 — All tool calls are parallel, including writes and shell commands

Severity: high  
Evidence: `internal/tools/tools.go:141-215`

`RunBatch` sends every allowed call through an eight-worker pool. Tools do not declare whether they are read-only, mutating, process-spawning, interactive, or order-sensitive. Two edits to one file, `mkdir` followed by `write`, tests racing a generated-file update, or `cd` racing another shell command can execute in a different order from the model's request. Returning outcomes in original order repairs only transcript presentation, not effects.

Fix: add effect metadata to `Tool` and schedule conservatively: parallelize explicitly read-only calls, serialize mutations in requested order, and use resource keys when safe parallelism is known. Default unknown/MCP tools to serialized.

### R2-08 — Filesystem confinement is not a stable security boundary

Severity: high  
Evidence: `internal/tools/confine_linux.go:15-50`, `internal/tools/fs.go:618-647`

On kernels without `openat2`, `openBeneath` falls back to ordinary pathname open while callers and comments still describe confined operation. `mkdirAllConfined` also performs resolve → `os.MkdirAll` → verify. A symlink swap can create directories outside the workspace before the post-check detects it.

Fix: fail closed when strong confinement is unavailable unless the user explicitly opts into weak mode. Implement directory creation as a descriptor walk using `openat`/`mkdirat` with no-follow checks, or require a platform primitive that provides equivalent semantics.

### R2-09 — Repository model overrides do not override global model entries

Severity: high  
Evidence: `internal/config/roles.go:27-70`, `internal/config/config.go:994-1012`

Repo model entries are appended after global entries, but `ModelOverrides` returns the first exact match and then the first bare match. A global entry for the same model therefore wins, contradicting the documented merge direction. A bare global model can also mask a later repo bare model.

Fix: merge model entries by canonical reference with explicit last-wins precedence, while retaining the rule that a provider-qualified entry beats a bare entry for that provider.

### R2-10 — Role “fallback chains” stop before the operation that normally fails

Severity: high  
Evidence: `internal/config/roles.go:121-185`

`Router.For` falls back only when a provider cannot be constructed. `SideCall` then makes exactly one network/stream call. Authentication failure, rate limiting, model-not-found, timeout, and mid-stream errors never try the next configured role entry, despite the type and comments calling this a fallback chain.

Fix: resolve and attempt the chain inside `SideCall`; retry only failures classified as safe before visible output, respect the shared context/deadline, and aggregate diagnostics without exposing credentials.

### R2-11 — Embeddings are coupled to the active chat provider and usually do not work

Severity: high  
Evidence: `internal/provider/openai.go:519-521`, `internal/provider/codex.go:1053+`, `internal/wiring/wiring.go:323-383`

Compaction and memory receive the active chat provider as their embedder. OpenAI-compatible and Codex providers return “embeddings not wired”; changing the chat model/provider also changes the semantic vector space used for memory and compaction. The configured feature can therefore silently degrade based on an unrelated chat choice.

Fix: add a separately configured embedding provider/model role, record vector-space identity with stored vectors, expose readiness in `/memory status`, and make lexical fallback explicit rather than presenting semantic memory as enabled.

### R2-12 — Credential changes are persisted before validation and are not applied atomically

Severity: high  
Evidence: `internal/daemon/command.go:242-289`

`setCredential` writes any non-empty target to config before checking that it names a configured provider. Provider rebuild errors are ignored. Only idle sessions using that provider are rebuilt; busy sessions get the new config value but keep their old provider instance indefinitely. The command still reports “API key saved”. The Brave connect path similarly updates only the current runtime rather than all relevant sessions.

Fix: validate the target first, build replacement clients before committing, update every affected runtime at a safe synchronization point, report partial failures, and represent a pending credential generation so a busy session refreshes immediately after its turn.

### R2-13 — Model switching is a scattered, potentially blocking partial transition

Severity: high  
Evidence: `internal/daemon/server.go:1684-1813`, `internal/tui/app.go:2956+`

The daemon holds `controlMu` while calling Ollama `Show(context.Background())`, so an unresponsive endpoint can block model control and snapshots without a deadline. The switch then updates provider, model, reasoning levels, vision, context, memory, compactor, config, and metadata in several steps; some build-time fields/closures still describe the original model/provider. The standalone TUI path ignores provider build errors and can update the visible model while retaining the old provider.

Fix: build an immutable `RuntimeModel` candidate off-lock with bounded calls, validate it completely, persist it, then swap one pointer under the lock. Remove or bring the standalone path under the same transition API.

### R2-14 — Automatic compaction runs too early and manual compaction consumes its safety allowance

Severity: high  
Evidence: `internal/agent/agent.go:559-580`, `internal/agent/compact.go:289-348`, `internal/agent/compact.go:910-990`

Auto-compaction runs once before the first model request. Large tool results added later in the same turn can overflow the provider context before the next request. Also, every successful `Compact` increments the same `count` checked against `MaxAutoCompactions`; three manual compactions disable automatic protection for the rest of the session.

Fix: check before every provider request, estimate the pending tool-result addition, and keep separate automatic-loop protection from a lifetime/manual compaction counter.

### R2-15 — The self-updater trusts an unsigned executable

Severity: high  
Evidence: `cmd/evilcode/update.go:115-132`, `cmd/evilcode/update.go:163-194`, `docs/DEVIATIONS.md`

The updater downloads over HTTPS, enforces a size limit, and checks only the ELF magic before replacing the running executable. A compromised release server, account, DNS/TLS endpoint, or asset can supply arbitrary code that passes this check.

Fix: publish signed checksums or attestations, pin a verification key in the client, verify asset name/platform/digest/signature before stopping the daemon, and sync the containing directory after rename.

### R2-16 — Model-run shell commands inherit the daemon's entire environment

Severity: high  
Evidence: `internal/tools/exec.go:162-177`

`commandEnv` begins with `os.Environ` and removes only the old scratch variables. Any unrelated tokens, credentials, agent sockets, build secrets, or desktop/session variables inherited by `evilcode serve` are visible to every model-authored shell command and its children.

Fix: construct an allowlist (`PATH`, locale, terminal basics, selected build variables), inject secrets only into the integration that needs them, and provide an explicit user-configured pass-through list.

## Medium-priority correctness and product findings

### R2-17 — Configuration validation covers only a small fraction of invalid states

Severity: medium  
Evidence: `internal/config/config.go:922-965`

Validation checks provider name/kind uniqueness and the default provider reference. It does not reject empty halves such as `@provider` or `model@`, invalid URLs, negative/absurd context and step limits, invalid display enums, duplicate model/MCP names, unknown role providers, invalid reasoning values, contradictory keybindings, or malformed LSP commands. It stops at the first error.

Fix: validate the complete normalized config and return an aggregate with TOML paths and actionable messages.

### R2-18 — Generic OpenAI-compatible providers are assumed to support `reasoning_effort`

Severity: medium  
Evidence: `internal/provider/openai.go:45-65`

The generic constructor defaults `supportsReasoningEffort` to true, but the public provider config has no reliable capability flag for arbitrary compatible gateways. The client can send an unsupported field based on model-name heuristics.

Fix: default generic compatibility to the conservative protocol, add explicit capability configuration/discovery, and enable OpenAI-specific fields only for known endpoints or opt-in providers.

### R2-19 — Model-catalog failures are swallowed and opening the picker can mutate runtime state

Severity: medium  
Evidence: `internal/tui/app.go:3264-3415`

Provider build and `Models` errors are dropped, so an unreachable or unauthenticated provider simply contributes no rows. Users cannot distinguish an empty catalog from a broken integration. `applyModels` may also change and persist reasoning effort merely because capabilities finished loading. `showPicker` selects by model name alone, so duplicate names across providers can highlight the wrong row.

Fix: carry per-provider errors into disabled diagnostic rows, keep discovery read-only, require explicit confirmation for a setting change, and identify current selection by the full `model@provider` reference.

### R2-20 — `/config` is advertised but unimplemented

Severity: medium  
Evidence: `internal/tui/commands.go:98`, `internal/tui/app.go:3857-3863`

The command registry/help presents `/config`, but it falls through to “not implemented yet”. This is a shipped dead end, not merely internal scaffolding.

Fix: implement the promised behavior or remove it from the public registry until it exists. Add a registry test requiring every visible command to have a handler.

### R2-21 — MCP support drops valid protocol data and never refreshes tools

Severity: medium  
Evidence: `internal/mcp/mcp.go:90-160`

Tool discovery performs one `ListTools` call and ignores pagination. The client has no tool-list-change handler. Tool results preserve only `TextContent`; structured content, images, audio, resources, and resource links disappear. Duplicate server names can still produce ambiguous namespaced tools. The advertised client version is hardcoded to `0.1.0` while the application is at v1.2.0.

Fix: consume `NextCursor`, register list-change notifications, map every supported MCP content type into `tools.Result`, validate unique server names, and source protocol identity from the build version.

### R2-22 — Codex OAuth state is per provider instance and embeds a drifting client version

Severity: medium  
Evidence: `internal/provider/codex.go:36`, Codex authentication fields/methods

Each provider instance owns its own auth mutex and token copy even though instances use the same account file. Concurrent sessions can refresh independently and overwrite newer state. `codexClientVersion = "0.147.0"` is also compiled into model requests and will drift from the installed Codex protocol unless deliberately maintained.

Fix: use one account-scoped token manager with singleflight refresh and atomic generation checks; derive compatibility metadata from a maintained protocol layer and test server-version changes.

### R2-23 — Partial `multiedit` failure is returned as a successful tool call

Severity: medium  
Evidence: `internal/tools/fs.go:985+`

`multiedit` deliberately applies entries independently. A later failure can leave earlier mutations in place while the tool result itself remains non-error. Models and unattended workflows can read that as an atomic successful change.

Fix: offer a transactional default using prevalidated in-memory replacements and atomic per-file commit/rollback, or at minimum set `IsError`/return an error when any element fails and clearly enumerate applied versus unapplied edits.

### R2-24 — Four-hex line anchors collide at ordinary file sizes and paged reads invalidate prior anchors

Severity: medium  
Evidence: `internal/tools/anchor.go:52-85`, `internal/tools/anchor.go:255-292`

Anchors have only 16 bits. Birthday collisions become likely after a few hundred distinct lines, causing valid edits to be rejected as ambiguous. `recordAtAnchors` replaces the entire stored map for a file, so reading a second page invalidates anchors from the first page even when the file has not changed.

Fix: use a longer content hash plus file revision, and merge page windows for the same stable file identity. Tests should cover collisions and edits referencing two separately read pages.

### R2-25 — `glob` applies its limit after walking, collecting, and sorting everything

Severity: medium  
Evidence: `internal/tools/fs.go:1196-1256`

The `limit` caps response length but not work or memory. A broad glob still traverses the complete workspace, retains every match, and sorts all of them. Generated directories outside the short hardcoded ignore list can create noticeable stalls.

Fix: use a bounded top-K strategy if sorted output is required, support repository ignore rules, expose truncation, and consider a traversal/file/time budget.

### R2-26 — Image input trusts filename extensions rather than validating content

Severity: medium  
Evidence: `internal/tools/fs_image.go:28-80`, TUI attachment loading

Files are classified as images by extension and raw bytes up to 20 MiB are sent to providers. Corrupt files and arbitrary renamed data reach base64 transport and provider APIs; dimensions may be unavailable without making the input invalid.

Fix: decode or validate magic/MIME for supported formats, reject malformed data, normalize if needed, and make image limits consistent with the daemon frame/blob design.

### R2-27 — Shell working-directory tracking can be spoofed by command output

Severity: medium  
Evidence: `internal/tools/exec.go:270+`

The shell wrapper uses the fixed marker `__evilcode_cwd__`. A command that prints marker-shaped output can be mistaken for the wrapper's control message and change the remembered cwd or corrupt displayed output.

Fix: generate a cryptographically random nonce per invocation and send control data over a separate descriptor or a length-delimited side channel.

### R2-28 — Mermaid caching can return a wrong or partial image

Severity: medium  
Evidence: `internal/tui/images.go:225-267`, `internal/tui/images.go:435-442`

Cache names use 32-bit FNV and any existing `.png` is accepted without validating its source or completeness. Hash collisions can return a different diagram. A crashed renderer can leave an output that future calls trust. The input and output are written directly rather than published atomically.

Fix: key with SHA-256, render to a unique temporary output, validate the decoded image, atomically rename it, and store/check source metadata.

### R2-29 — Productivity statistics are materially incorrect

Severity: medium  
Evidence: `internal/tui/productivity.go:46-87`

Every stored message is counted as both a message and a prompt, including assistant/tool/system records. All activity in a session is assigned to the file's last-modified day. “Since” is therefore the oldest last-modified timestamp, not the first activity date. Token fields are not populated.

Fix: derive stats from timestamped entries and roles, migrate old entries with an explicit “unknown” bucket, and label estimates when exact historical data is unavailable.

### R2-30 — Hidden automation has a one-slot overwrite queue

Severity: medium  
Evidence: `internal/tui/app.go:538-542`, `internal/tui/app.go:3497-3504`, `internal/tui/app.go:4207-4216`

`queuedHidden` is one string. A plan, resume, overnight continuation, or other harness action queued while another is pending replaces the previous action. The overwritten action has no durable identity or visible failure.

Fix: use a typed FIFO with source, request ID, cancellation, and deduplication policy; render its state like the user-message queue.

### R2-31 — Todo dependencies are syntactic, not state-aware

Severity: medium  
Evidence: `internal/todo/model.go:77-92`, `internal/todo/model.go:600-675`, `internal/todo/model.go:1000-1025`

Validation rejects missing dependencies and cycles but allows a blocked item to be marked in-progress or completed while its prerequisites remain unfinished. `Blocked()` and the summary treat any non-empty `BlockedBy` as blocked even after those dependencies complete. Correctness relies on the model rewriting dependency arrays manually.

Fix: resolve blockers against current item status, reject impossible transitions, and either remove satisfied dependencies automatically or distinguish declared dependencies from currently blocking ones.

### R2-32 — Overnight progress detection compares a lossy summary string

Severity: medium  
Evidence: `internal/tui/overnight.go:152-195`, `internal/todo/model.go:1000-1025`

Stall detection compares only a summary such as `1/9 done, 1 in progress`. Meaningful edits, evidence, confidence, priority, or movement between tasks can look stalled; cosmetic count changes can look like progress. This can terminate useful work or keep ineffective work running.

Fix: compare a stable semantic revision/digest and require evidence-bearing state transitions, tool activity, or repository changes according to the overnight contract.

### R2-33 — `/fork` forks only conversation storage

Severity: medium  
Evidence: `internal/daemon/command.go:133-141`, `internal/session/store.go:1015+`

The command copies the session log and blobs, but session-private todo state, checkpoints/auxiliary state, and other runtime-owned artifacts do not form one branch. The UI wording suggests a complete branch.

Fix: define a session manifest and fork all owned state transactionally, or rename/document this as “fork conversation”.

### R2-34 — Imported sessions are resumable but semantically lossy

Severity: medium  
Evidence: `internal/session/import.go`

Codex and OpenCode import paths flatten or omit reasoning/tool/provider-specific items. Native re-import returns an existing import rather than re-reading a source that may have grown. ID lookup can become ambiguous across files. A resumed imported conversation may therefore lack the tool-call/result adjacency or provider state of the source.

Fix: preserve typed source items where possible, store import provenance and source revision, make refresh explicit, and fail on ambiguous external IDs with candidate paths.

### R2-35 — Persistence semantics are inconsistent across stores and processes

Severity: medium  
Evidence: `internal/session/store.go`, `internal/session/blob.go`, `internal/memory/store.go`, `internal/tools/fs.go:694-750`, `internal/tools/confine_linux.go:68-123`

Some paths flush buffered data without `fsync`; some atomic replacements sync the file but not the containing directory; some paths correctly sync both. Native JSONL sessions have no obvious cross-process writer lock, so two daemon/process instances can interleave logical writes. Rewind/compaction/fork can leave unreferenced blobs indefinitely.

Fix: document durability levels, centralize atomic replace/append helpers, sync parent directories where durability is promised, lock each session across processes, and add reference-aware blob garbage collection.

### R2-36 — LSP rename is rollback-capable but not transactionally atomic

Severity: medium  
Evidence: `internal/lsp/ops.go:306-440`

Multiple files are replaced sequentially. Rollback handles ordinary errors, but a crash between renames leaves a partially applied workspace. LSP resource operations and URI variants have narrower support than `WorkspaceEdit` permits.

Fix: preflight all edits and resource operations, journal the transaction durably, recover or roll back on startup, and explicitly reject unsupported URI schemes/operations before touching files.

### R2-37 — The Brave client hardcodes service policy and does not adapt to throttling

Severity: medium  
Evidence: `internal/tools/brave.go:20-70`, `internal/tools/brave.go:225-301`

All searches are serialized at a hardcoded 1.2-second interval based on an assumption about one service tier. HTTP 429 and `Retry-After` are returned as ordinary errors with no bounded retry. This can unnecessarily slow higher-tier keys and still fail under server-side policy changes.

Fix: make rate policy configurable, honor response headers, implement bounded jittered retry for retryable statuses, and expose rate-limit diagnostics.

### R2-38 — Several small API contracts are misleading or broken

Severity: medium/low  
Evidence: `internal/tui/images.go:298-315`, `internal/graphics/graphics.go:51-52`, `internal/tools/tools.go:105-115`, `internal/agent/agent.go:678-692`

- `boundedBuffer.Write` truncates `p` and returns the truncated length with nil error, violating the normal `io.Writer` contract. It should return the original input length when intentionally discarding overflow, or `io.ErrShortWrite`.
- `EVILCODE_GRAPHICS` accepts any string; invalid values later degrade to silent no-op rendering. Validate the enum at startup.
- `Set.Find` silently uses the first duplicate tool name. Validate the completed set after built-ins, MCP, and skills are assembled.
- `Agent.endTurn` releases its run reservation before checking persistence and emitting terminal events. Direct/local callers may start the next run before the prior terminal sequence is published. The daemon's outer reservation reduces this risk there, but the lower-level contract remains surprising.

## Maintainability and architecture

### R2-39 — Coverage is uneven exactly where integration risk is highest

Severity: medium  
Evidence: coverage run from this review

Notable statement coverage: `attachcmd` 0%, `jsonl` 0%, `mcp` 0%, `probecmd` 0%, `servecmd` 0%, `runcmd` 11%, `tuicmd` 6.3%, `cmd/evilcode` 10.8%, `completions` 39%, and `wiring` 58.7%. Core packages are healthier (`agent` 76%, `config` 83%, `core` 90.3%, `provider` 73.1%, `session` 74.3%, `tools` 73.5%, `tui` 65.8%), but command wiring, protocol adapters, and MCP—the places where components disagree—are weakly protected.

Fix: prioritize end-to-end tests for attach/send/receive limits, command registration, server startup/shutdown, config-to-runtime wiring, MCP pagination/content, update verification, and standalone/headless execution.

### R2-40 — There is no repository CI gate

Severity: high process risk  
Evidence: no workflow file was found under the common CI locations

The current probe failure, intermittent test failure, and formatting drift can all reach the main branch without automation.

Minimum gate:

```sh
gofmt -l .                 # fail on any output
go vet ./...
go test ./... -shuffle=on -count=1
go test -race ./... -count=1
go build ./cmd/evilcode
go mod verify
EVILCODE_BIN=... go test -tags probe ./probe/... -count=1
```

Add `staticcheck`, `govulncheck`, and `shellcheck install.sh` once their findings are triaged. Use a second stress job for concurrency tests rather than making every ordinary job slow.

### R2-41 — Formatting is already outside a reproducible baseline

Severity: low, but gate-blocking  
Evidence: `gofmt -l .`

Twenty-three files were reported, including production files in provider, tools, TUI, and the current working-tree changes. Several are more than a missing final newline. Formatting drift creates noisy diffs and makes generated/reviewed changes harder to compare.

Fix: format the tree in a dedicated change after preserving current edits, then make CI fail on formatter output.

### R2-42 — `internal/tui/app.go` is a state machine and half the application in one file

Severity: medium  
Evidence: `internal/tui/app.go` is about 6,053 lines with 181 top-level functions

The file owns input editing, rendering transitions, remote/local transport, models, commands, queues, attachments, panels, overnight control, session actions, animations, and several caches. Large functions such as command dispatch, key handling, event application, and view rendering combine policy with mutation. The silent-dispatch and model-switch findings are consequences of this ownership being difficult to reason about.

Recommended extraction:

- a pure event reducer with explicit effects;
- a typed command registry whose entries own parsing, help, and handlers;
- a transport/outbox state machine with acknowledgement and retry;
- model/catalog state independent from runtime model transitions;
- panel/document providers with byte budgets and lazy rendering;
- overnight/queue orchestration with typed requests.

Keep `Model.Update` as composition, not the implementation of every feature.

### R2-43 — `internal/daemon/server.go` has too many synchronization responsibilities

Severity: medium  
Evidence: `internal/daemon/server.go` is 2,193 lines

One file owns socket lifecycle, protocol I/O, session maps, event pumps, snapshots, subscriptions, idle eviction, provider/model transitions, worker limits, asks, background state, and persistence-facing lifecycle. Lock ordering and state duplication are spread across `mu`, `controlMu`, agent reservations, and server reservations.

Recommended extraction:

- framing/codec with one size policy;
- session actor or serialized command loop;
- replay log and subscriber backpressure;
- immutable snapshot builder;
- runtime-model transition service;
- lifecycle/eviction manager.

An actor-style session loop would remove many lock-order questions and make snapshot/event ordering testable.

### R2-44 — Runtime state is duplicated instead of having one canonical owner

Severity: medium  
Evidence: model/provider/effort/vision/context values appear across `Session`, `Built`, `Agent`, `Config`, TUI header, picker entries, memory, compactor, and persisted metadata

The same setting is copied into many mutable objects and synchronized through events. A missed assignment produces a split-brain UI or a component using the previous provider. Credential and model-switch behavior already demonstrate this.

Fix: define canonical runtime state with a monotonically increasing revision. Project read-only views into the TUI, memory, and compactor, and reject stale transitions/events.

### R2-45 — Comments and identifiers contain stale or duplicated claims

Severity: low  
Evidence: `install.sh:13-15`, `internal/tools/anchor.go:255-265`, `internal/mcp/mcp.go:90`, background-command comments

- The installer says only linux/amd64 ships while its code supports arm64.
- `AnnotateLines` has two adjacent doc comments saying the same thing.
- MCP identifies v0.1.0 independently of the application version.
- Some background-command comments still describe adoption behavior that was changed when timed-out foreground processes began being killed.
- Names such as `For`, `SideCall`, `Built`, `Model`, `Set`, and `Info` are overly generic in a codebase with many domain layers; they hide ownership in call sites.

Fix: delete historical bug narratives once tests carry the rationale, keep comments about current invariants, generate version strings, and prefer names such as `ResolveRoleBackend`, `RunRoleCompletion`, `SessionRuntime`, and `ToolSet` where they reduce ambiguity.

### R2-46 — Local/standalone TUI wiring is a lightly tested second implementation

Severity: medium  
Evidence: `internal/tuicmd/tuicmd.go`, `internal/tui/app.go:3014+`; `tuicmd` coverage 6.3%

Normal operation delegates to the daemon/attach path, but substantial local provider switching and lifecycle code remains. That path has different error handling and state updates, including ignoring a cross-provider build error. Maintaining two implementations makes fixes land in only one side.

Fix: decide whether standalone mode is a supported product path. If yes, share a runtime-control interface and add parity tests. If no, remove the dead branch and its duplicated state.

### R2-47 — Binary/demo fixtures and broad mock logic add production weight

Severity: low  
Evidence: production `internal/provider/mock.go` and probe/demo behaviors compiled with the normal binary

The deterministic mock is useful for probes, but a large scripted provider and demo-specific behavior are part of production code and formatting surface. This increases binary/review surface and makes production conditionals easier to trigger accidentally.

Fix: separate the reusable minimal mock interface from tagged probe fixtures, or generate/register rich scenarios only in probe builds.

## Missing or incomplete product behavior

These are not all release blockers, but they should be explicit backlog items rather than appearing complete:

- A real configuration UI/command is missing even though `/config` is advertised.
- Semantic embeddings are not available for the common hosted-provider paths.
- MCP pagination, dynamic tool refresh, and non-text results are missing.
- Forking is not a complete session branch.
- Import refresh and fidelity guarantees are incomplete.
- Model catalog errors, memory readiness, and partial credential propagation lack user-visible diagnostics.
- There is no explicit protocol compatibility negotiation beyond a version field and hard frame constants.
- Blob lifecycle/garbage collection is absent.
- Update authenticity is not verified.

## Strengths worth preserving

- Provider streams now treat abnormal termination as errors instead of silently committing clipped answers.
- Tool arguments are strictly decoded, reject trailing JSON, preserve one result per tool call, bound batch size, and recover panics.
- Session storage has extensive repair/reopen tests and several atomic/synced paths.
- Process groups, foreground timeouts, background shutdown, and daemon connection teardown are handled deliberately.
- Socket path ownership checks and same-user peer checks materially reduce local-daemon exposure.
- Race testing passes across the full package set.
- The code has many comments that explain real invariants; after stale historical notes are trimmed, this is an asset.

## Proposed milestone plan

### Milestone 1 — Transport integrity

- Add failing tests for an 8+ MiB snapshot, a 20 MiB tool image, repeated ring wrap, and slow-subscriber byte pressure.
- Move images and large displays to blob references.
- Replace full-history turn-end payloads with revisioned deltas and paged snapshots.
- Add server-side size checks, acknowledgements, and retryable client errors.
- Restore editor/attachments when dispatch fails.

### Milestone 2 — Deterministic execution and security

- Add tool effect/resource metadata and serialize mutations.
- Fail closed or clearly opt into weak confinement on unsupported kernels.
- Replace `mkdirAllConfined` with descriptor-relative creation.
- Sanitize subprocess environments.
- Add signed update verification.

### Milestone 3 — Runtime consistency

- Introduce one atomic runtime-model object and bounded discovery calls.
- Implement true role fallbacks.
- Add a dedicated embedding role and readiness reporting.
- Make credential rotation validate/build/swap atomically.
- Correct repo override precedence and broaden config validation.

### Milestone 4 — Quality gates and decomposition

- Fix the seven probe scenarios and the flaky worker helper.
- Format the tree and add CI.
- Add command/wiring/MCP/attach coverage.
- Extract transport, command, panel, model, and automation state from `tui/app.go` and `daemon/server.go` incrementally, with behavior tests around every extraction.

## Release recommendation

Do not cut a release until R2-01 through R2-08 and the failing gates in R2-05/R2-06/R2-40/R2-41 are resolved. The daemon framing issue is deterministic at sufficient history/image size, not a theoretical edge case. After those items, the project is in a good position for a stabilization release; the remaining medium findings can be scheduled by product priority as long as their current limitations are documented.
