# Evilcode exhaustive code review

Reviewed: 2026-08-26  
Worktree: the current, uncommitted working tree in `/home/eko/projects/evilcode`  
Scope: all 386 tracked files at commit `f6e651e`, including 312 Go files (56,656 production lines and 32,153 test lines), the installer, documentation, provider integrations, daemon protocol, persistence formats, tools, every registered TUI command, and every widget/render path. Generated binaries, ignored probe captures, release artifacts, and scratch output were not treated as source.

## Executive assessment

Evilcode has a strong internal shape: providers are separated from the agent loop, the daemon owns live conversations, frontends consume one event vocabulary, session logs are append-oriented, file writes are usually staged and atomic, and the test suite contains unusually good regression tests for past failures. The ordinary unit suite, race detector, and vet all pass.

That green baseline is misleading in several important places. The review found release-blocking defects in the fresh-install default model, DeepSeek reasoning/tool replay, generic OpenAI stream completion, image transport over the daemon, session rename coordination, and command timeout semantics. It also found features that the UI advertises but does not implement (`/config`, `lenient_tool_parse`), a productivity screen whose numbers do not mean what its labels say, stale visual goldens, and several lifecycle paths where work or credentials remain attached to an old runtime after the UI reports a successful change.

The most useful repair order is:

1. Make a clean install able to complete its first turn: correct the Ollama model tags/default route and bring the DeepSeek adapter up to the current protocol.
2. Make transport completion explicit: reject truncated OpenAI streams, frame or externalize daemon images/diffs, and wait for daemon shutdown before an updater replaces the executable.
3. Make identity and live configuration transitions atomic: model changes, credentials, rename, resume, swarm maps, todo/memory namespaces, and pollers must move together.
4. Correct tool safety semantics: a timeout must stop the process, mutating tool calls must not be parallel by default, and “confined” must fail closed when the kernel cannot provide the guarantee.
5. Repair or remove misleading surfaces: `/config`, productivity statistics, `lenient_tool_parse`, stale Ctrl+T help, and the unreachable standalone TUI implementation.
6. Put the tmux probe suite and provider contract tests into CI so the current 25 visual failures and future protocol drift cannot ship unnoticed.

## Verification performed

| Check | Result | Meaning |
|---|---:|---|
| `go test ./...` | pass | Unit/integration suite is green. |
| `go vet ./...` | pass | No vet diagnostics. |
| `go test -race ./...` | pass | Covered test paths did not expose races. This does not cover multiple processes or external services. |
| `go test -cover ./...` | pass | Core packages are mostly 60–90%; `internal/mcp`, `internal/attachcmd`, `internal/jsonl`, `internal/probecmd`, `internal/resumecmd`, and `internal/servecmd` are 0%. `internal/tuicmd` is 6.4%, CLI is 10.8%, and headless run is 11.0%. |
| `go build -o /tmp/evilcode-codex-review ./cmd/evilcode` | pass | Current worktree builds. |
| `EVILCODE_BIN=/tmp/evilcode-codex-review go test -tags probe ./probe/...` | **fail** | 25 frames fail in 19 scenarios. Many have a stale `v1.1.1` golden versus `v1.1.3`; several also contain layout-width drift. |
| CI configuration | absent | No tracked `.github` workflow or equivalent release gate runs the checks above. |

The external protocol checks in this review use current primary documentation: the [Ollama DeepSeek V4 Flash tags](https://ollama.com/library/deepseek-v4-flash/tags), [DeepSeek chat API](https://api-docs.deepseek.com/api/create-chat-completion/), [DeepSeek function calling guide](https://api-docs.deepseek.com/guides/function_calling/), [DeepSeek Responses API guide](https://api-docs.deepseek.com/guides/responses_api/), [MCP tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools), and [Brave rate-limit guide](https://api-dashboard.search.brave.com/documentation/guides/rate-limiting).

## Architecture and feature trace

The effective production path is:

```text
evilcode / ec
  -> tuicmd.Run
  -> attachcmd.RunDefault
  -> EnsureRunningPath (spawn daemon if needed)
  -> daemon Server.OpenWithOptions
  -> wiring.Build
       config + repo overrides
       provider + conversation + session Store
       filesystem/exec/search/LSP/MCP/todo/memory/skill tools
       compactor + hooks
  -> Agent.Run / Agent.loop
       append prompt -> optional compact -> stream model
       -> execute tool batch -> append results -> stream again
       -> persist -> publish event/snapshot
  -> NDJSON Unix socket
  -> proxy Agent in attach client
  -> Bubble Tea Model and widgets
```

This division is sound in principle. The main architectural risk is that state is duplicated at several layers: daemon config, per-session cloned config, live provider object, agent model/provider fields, compactor embedder, memory embedder, filesystem vision mode, persisted session metadata, client header, client capability lists, and client preference files. Most high-severity state bugs below are transitions that update only a subset of that graph.

### Surface inventory

| Surface | Implementation | Review outcome |
|---|---|---|
| `tui` / default command | `internal/tuicmd`, then daemon attach | Production path works, but most of `tuicmd` is now unreachable duplicate local wiring. |
| `run` | daemon-backed headless request | Low direct coverage; inherits daemon/provider findings. |
| `serve` | multi-session Unix-socket daemon | Good peer/socket checks; frame bounds, shutdown ordering, queue/ring memory, and rename lifecycle need work. |
| `attach` | proxy agent + Bubble Tea UI | Correct basic mirroring; unbounded poller, stale identity after rename, and oversized frames remain. |
| `resume` | native or Claude/Codex/OpenCode import | Native resume is robust; external imports are lossy and never refresh. |
| `update` / installer | Forgejo release download and atomic rename | Atomic file replacement is good; authenticity and daemon-stop completion are not established. |
| providers | Ollama, OpenAI-compatible, DeepSeek, Codex OAuth, mock | Ollama completion handling is best; OpenAI and DeepSeek have protocol correctness gaps; Codex token lifecycle is too instance-local. |
| model picker/reasoning picker | live catalog aggregation + per-model state | Useful design; duplicate-name selection, local cross-provider failure, vision drift, and unavailable-provider invisibility remain. |
| commands | 65 visible/hidden command names | Dispatch covers nearly all; `/config` is registered but deliberately falls into “not implemented yet.” Ctrl+T help is stale. |
| widgets | context, cache, cloud use, memory, todo, swarm, status | Render logic is generally bounded; productivity data is materially false and visual goldens are stale. |
| tools | FS, grep/glob, shell/background, todo, memory, skill, Brave, LSP, MCP, swarm | Broad and well structured; mutation scheduling, timeout semantics, confinement fallback, MCP completeness, and edit result semantics need correction. |
| persistence | session JSONL + blobs, todo JSON, memory JSONL, prompt history | Good permission and repair work; cross-process locks, blob GC, imported metadata, and full durability are missing. |

## Detailed findings

Severity means: **critical** can make a default installation unusable, corrupt/escape state, or silently execute beyond an explicit safety boundary; **high** breaks a core feature or silently produces a materially wrong conversation/runtime; **medium** is a real defect with a narrower trigger; **low** is maintainability, diagnostics, or polish that can still cause drift.

### A. Defaults, configuration, and routing

#### A1 — Critical: both fresh-install Ollama defaults name the wrong model artifact

Evidence: `internal/config/config.go:233-235` sets `deepseek-v4-flash:0731@ollama-cloud` and the same bare `:0731` tag for `ollama-local`; `README.md:121-128` documents the cloud form. The current official Ollama catalog publishes `deepseek-v4-flash:0731-cloud`, `:cloud`, and `:preview-cloud`, not `:0731`. It is also a 304B cloud-hosted model, not a normal local pull.

Impact: with an Ollama key, a new session asks the cloud endpoint for a nonexistent tag. Without a key, it asks a local Ollama daemon for a cloud-only 304B model that a fresh machine will not have. The claim that a missing config is a “working setup” is therefore false at the first model request.

Solution: set the cloud reference to `deepseek-v4-flash:0731-cloud@ollama-cloud`. Do not pretend the same artifact is a local default; either choose a realistically pullable local model, discover installed local tags and select one, or show first-run setup when none exists. Add an integration test that compares each built-in default to the provider catalog (or a pinned catalog fixture) and a clean-home probe that reaches an intelligible setup state.

#### A2 — High: the built-in direct DeepSeek model is deprecated and the reasoning vocabulary is stale

Evidence: `internal/config/config.go:235` still uses `deepseek-chat`; `internal/provider/provider.go:72-76` offers only none/high/max; `internal/provider/openai.go:75-86` recognizes only names containing `reasoner`, `r1`, or `think`; `internal/provider/openai.go:515-526` maps lower efforts upward. Current DeepSeek V4 uses `deepseek-v4-flash`/`deepseek-v4-flash-pro`, has deprecated `deepseek-chat` and `deepseek-reasoner`, and documents low/high/max thinking effort.

Impact: the direct provider can resolve a retired alias, the current V4 name is classified as non-reasoning, and a requested low effort is silently translated to high. The picker and wire behavior disagree with the service.

Solution: make capabilities model/catalog-driven, update the built-in model names, include low, and map only documented values. Keep dated compatibility aliases behind explicit tests rather than in the default path.

#### A3 — High: repository model overrides lose to global overrides

Evidence: `internal/config/roles.go:61` appends repository `[[model]]` entries after global entries. `internal/config/config.go:953-966` returns the first exact/bare match.

Impact: `.evilcode.toml` says it overrides repository behavior, but a global entry with the same model wins. Context size, anchors, vision, and the dead lenient parsing flag can all remain global without any warning.

Solution: merge model settings by canonical model reference with repository values winning, or prepend repository entries. Test exact qualified collisions and bare-versus-qualified precedence.

#### A4 — High: a daemon started before the config file exists never refreshes `last_model`

Evidence: `LoadFrom` assigns `cfg.Path` only in the successful-read branch (`internal/config/config.go:322-347`). `SaveLastModel` creates the missing file (`internal/config/config.go:453-468`), but `wiring.Build` refreshes it only when `cfg.Path != ""` (`internal/wiring/wiring.go:172-176`).

Impact: start the daemon with no config, switch models, then create a fresh session. The client saves the choice, but the daemon keeps creating sessions with its startup default until restart.

Solution: set `cfg.Path = path` before attempting the read, including ENOENT. Preserve the current “in-memory test config has no path” behavior by making tests construct `Default()` directly rather than calling `LoadFrom`.

#### A5 — High: role fallback chains do not fall back when the request fails

Evidence: `Router.For` walks the chain only until a provider object builds (`internal/config/roles.go:137-156`). `SideCall` then performs one request and returns its network/auth/stream error (`internal/config/roles.go:162-188`).

Impact: a configured but unreachable first model prevents memory extraction, compaction, advisor, titles, and other `smol` work from trying the next advertised fallback. “Fallback chain” currently means constructor fallback, not operational fallback.

Solution: run the whole pre-output request against each role reference in order. Stop after visible output or a non-retryable semantic error; otherwise try the next backend and return a joined diagnostic.

#### A6 — Medium: `lenient_tool_parse` is a documented configuration no-op

Evidence: the field and explanatory comment exist at `internal/config/config.go:93-96`; repository-wide references are only config tests. No provider, agent, or parser reads it.

Impact: users can deliberately enable a compatibility feature and see no behavior change. Small/local models that emit JSON tool calls in text still fail, with no indication that the setting is dead.

Solution: either implement the opt-in parser at the provider/agent boundary with strict schema/name validation, or remove the field and documentation until it exists. Add an end-to-end test, not only a config decoding test.

#### A7 — Medium: validation covers provider names/kinds but leaves many invalid states for runtime

Evidence: `Config.Validate` ends after provider uniqueness and default-provider existence (`internal/config/config.go:915-946`). `SplitModelRef` accepts `@provider`, `model@`, and whitespace (`internal/config/config.go:950-958`). URLs, duplicate model names/MCP names, empty role refs, invalid display enum values, negative context sizes, nonsensical `max_steps`, malformed env variable names, and duplicate key bindings are not rejected centrally.

Impact: errors emerge later, often in a picker or provider call, and duplicate MCP names can recreate tool-name collisions despite namespacing.

Solution: validate all enums and numeric ranges, parse/validate URLs by provider kind, require nonempty model/provider halves, canonicalize names, reject duplicates, and report all config problems in one aggregate error.

#### A8 — Medium: generic OpenAI-compatible endpoints are assumed to accept `reasoning_effort`

Evidence: `NewOpenAI` defaults `supportsReasoningEffort` to true (`internal/provider/openai.go:39-49`). Any configured OpenAI-compatible endpoint can therefore receive that optional field based on a name heuristic.

Impact: compatible gateways that implement chat completions but not OpenAI reasoning controls can reject an otherwise valid request.

Solution: make reasoning control opt-in in `ProviderConfig`, enable it for known first-party adapters/catalog metadata, and default unknown compatible endpoints to false.

#### A9 — Medium: `/config` is exposed in the palette/help but has no implementation

Evidence: `internal/tui/commands.go:96` registers it and `HelpSections` advertises it; `runCommandWithArg` has no `case "config"` and therefore reaches the “not implemented yet” default at `internal/tui/app.go:3620-3626`.

Impact: a visible system command fails by design. It is particularly harmful while diagnosing the routing and override problems above.

Solution: render a redacted effective configuration: source path, repo override path, selected refs/roles, provider names/kinds/URLs, feature/display values, and only presence—not values—of credentials. Add a registry/dispatch test requiring every visible command to have a handler.

### B. Provider and model protocol correctness

#### B1 — Critical: DeepSeek reasoning is not replayed across tool rounds

Evidence: streamed `reasoning_content` is captured (`internal/provider/openai.go:217-221`, `:451-458`) and stored in `provider.Message.Reasoning`, but `oaiMessage` has no `reasoning_content` field (`internal/provider/openai.go:109-120`) and `toOAIMessages` ignores `Message.Reasoning` (`:154-184`). DeepSeek requires the assistant reasoning content to be returned with the tool calls in the next request when thinking is enabled.

Impact: the first DeepSeek response can call a tool, but the second request lacks required reasoning state. Current DeepSeek reasoning/tool workflows can be rejected or lose chain continuity exactly on coding tasks that need tools.

Solution: add the DeepSeek-only replay field and populate it on assistant messages, preserving its adjacency with calls/results. Write a two-round HTTP fixture that asserts the second request contains the first response’s reasoning content.

#### B2 — Critical: generic OpenAI SSE treats a clean premature EOF as success

Evidence: `streamOpenAISSE` breaks on `[DONE]`, but after scanner EOF it always emits `Done: true` (`internal/provider/openai.go:399-470`). Unlike the Ollama adapter (`internal/provider/ollama.go:200-270`), it does not remember whether a protocol terminal marker arrived. The agent itself ignores `Chunk.Done` (`internal/agent/agent.go:751-813`).

Impact: a proxy reset or truncated response is committed as a complete assistant answer. A partial tool-call argument can become a finished call; partial prose is saved with a successful turn end. The user gets neither retry nor error.

Solution: require a terminal protocol record, emit an error on EOF first, and make `Agent.streamOnce` require exactly one terminal `Done`. Add truncated-before-output and truncated-mid-output tests at both provider and agent layers.

#### B3 — High: OpenAI `finish_reason` is decoded and discarded

Evidence: `FinishReason` exists at `internal/provider/openai.go:222`, but `streamOpenAISSE` never reads it.

Impact: `length`, `content_filter`, and malformed tool-call termination are displayed as ordinary completion. `length` should trigger a clear incomplete result or context recovery, not silently save a clipped answer.

Solution: accumulate finish reasons by choice and translate non-`stop`/`tool_calls` values into typed terminal errors or end reasons. Show the reason in the UI and persist it in metadata.

#### B4 — High: synthesized OpenAI tool IDs collide between responses

Evidence: when a gateway omits IDs, every `toolCallAccum.finish` starts at `call_1` (`internal/provider/openai.go:374-390`). Ollama correctly uses a provider-instance atomic sequence (`internal/provider/ollama.go:230-245`).

Impact: IDs can repeat in one conversation, confusing tool-result pairing and provider replay on later turns.

Solution: give `OpenAI` an atomic call sequence or synthesize an ID from response/turn identity plus call index. Test two consecutive responses with omitted IDs.

#### B5 — High: the agent never verifies provider `Done`

Evidence: `streamOnce` processes text, reasoning, tools, provider items, and usage but has no `done` state (`internal/agent/agent.go:751-813`). This weakens every provider’s completion contract and makes future adapters easy to get wrong.

Impact: a provider channel that closes without error is success regardless of protocol completeness. OpenAI currently exploits this accidentally; a mock or new provider can do the same.

Solution: require `Done`, reject multiple terminal chunks/data after terminal, and make provider contract tests common across all adapters.

#### B6 — Medium: retry considers every non-HTTP error transient

Evidence: `retryable` returns true for every error that is not a typed `HTTPError` (`internal/agent/agent.go:724-737`).

Impact: malformed SSE, invalid JSON, unsupported message shape, local serialization errors, and other deterministic failures are retried up to four times if no content was emitted. That costs latency/API calls and obscures the real problem behind “giving up.”

Solution: retry only recognized transport failures, timeouts, 408/409/425/429, and 5xx responses. Treat protocol/validation/serialization errors as permanent.

#### B7 — Medium: embeddings are wired to chat providers that explicitly do not implement them

Evidence: `OpenAI.Embed` returns “not wired” (`internal/provider/openai.go:473-475`); Codex likewise has no useful embedding path. `wiring.Build` nevertheless supplies the active provider to memory and compaction (`internal/wiring/wiring.go:368-371`), and model switching replaces both embedders (`internal/daemon/server.go:1690-1698`).

Impact: memory/semantic compaction works only when the active chat backend happens to be Ollama. Switching to OpenAI, DeepSeek, or Codex silently degrades to lexical/no semantic behavior and can repeatedly generate avoidable errors.

Solution: configure a separate embedding role/provider/model with capability detection. Do not change it merely because the chat model changes. Expose its readiness in `/memory status` and `/config`.

#### B8 — Medium: Codex OAuth coordination is per provider instance, not per auth file/account

Evidence: each `Codex` has its own `authMu` and token copy (`internal/provider/codex.go:58-74`). A daemon creates multiple provider instances. `bearerToken` treats a token with no decodable expiry as indefinitely valid (`internal/provider/codex.go:276-286`), and `ChatStream` does not reload/retry once on 401 (`:422-480`). The advertised client protocol version is also a fixed `0.147.0` constant (`:31-42`).

Impact: simultaneous sessions can refresh the same auth file concurrently; one instance does not see a refresh or `codex login` performed by another. Opaque/changed token formats remain “valid” until every request fails. A stale fixed client version can eventually empty or skew catalog discovery.

Solution: use a shared token manager keyed by canonical auth path/account, with a file lock around refresh writes, file-change reload, and one forced reload/refresh retry on 401. Derive the protocol version from a maintained compatibility constant with a provider contract test and visible failure message.

#### B9 — Medium: provider catalog failures disappear from the model picker

Evidence: `fetchAllModels` gathers providers concurrently but only appends successful results; unavailable configured providers are omitted rather than represented with an error (`internal/tui/app.go:3135-3185`).

Impact: a missing key, dead Ollama daemon, TLS failure, or incompatible `/models` endpoint looks like “this provider has no models.” Users cannot distinguish empty catalog from broken connection.

Solution: return per-provider status rows/errors, keep stale cached results with a warning, and expose retry details under `/models` or `/config`.

### C. Agent loop, compaction, and tool-call orchestration

#### C1 — High: context compaction runs only before the first request of a turn

Evidence: `a.autoCompact(ctx)` is called once before `EventTurnStart` (`internal/agent/agent.go:558-570`). Subsequent tool results loop directly back to `stream` (`:573-626`).

Impact: a large file read, grep, diff, or batch of tool results can push the conversation beyond the model window before the next provider request. The proactive compactor cannot help precisely when tool use creates the spike.

Solution: run a request preflight before every `ChatStream`, using a projection that includes newly appended tool results. On a typed context-length failure before output, compact and retry once.

#### C2 — High: `max_steps` counts model requests, not completed tool rounds

Evidence: the counter is checked at the top of the loop before streaming (`internal/agent/agent.go:573-584`), although the warning calls it “tool rounds.” With `max_steps=1`, one model tool call is executed and the loop stops before requesting the final answer.

Impact: the feature cannot represent the user-visible contract. Small caps strand a valid tool exchange without a concluding response.

Solution: count a step only after a tool-bearing response has been executed, and allow the terminal no-tool response. Persist an explicit `EndMaxSteps` error result if the next tool round would exceed the cap.

#### C3 — High: all calls in a tool batch run concurrently, including mutations

Evidence: `Set.RunBatch` sends every call through an eight-worker pool (`internal/tools/tools.go:120-180`). There is no read/write/dependency metadata. Per-path FS locks protect only some same-file operations; shell commands and different tools can mutate overlapping state freely.

Impact: a model commonly emits “edit A, edit B, test” together. The test can run before edits finish; `bash`, `write`, `edit`, LSP rename, and MCP mutations can race. Tool results are reordered back into request order, hiding the execution order.

Solution: add tool effect metadata (`read-only`, `workspace-write`, `process`, `external-write`) and schedule only independent reads in parallel. Serialize mutations in call order unless a model explicitly supplies a dependency-free group.

#### C4 — Medium: duplicate tool names are accepted and first-wins silently

Evidence: `tools.Set` is a slice and `Find` returns the first name (`internal/tools/tools.go:74-93`). MCP only prefixes with the configured server name (`internal/mcp/mcp.go:109-124`), while config does not reject duplicate MCP server names.

Impact: duplicate servers/tools or future built-in assembly mistakes send duplicate function names to a provider, while execution may call a different definition than the model inferred.

Solution: validate the completed tool set once during wiring, reject duplicates with both origins, and include origin metadata in status output.

#### C5 — Medium: manual compactions consume the automatic lifetime breaker

Evidence: every `Compactor.Compact` increments the single `count` (`internal/agent/compact.go:294-344`); `ShouldCompact` disables itself at three (`:911-923`). `/compact` calls the same method (`internal/daemon/command.go:321-340`).

Impact: three deliberate manual compactions permanently disable automatic protection for that session. Conversely, after three automatic compactions a long-lived session eventually grows into an unrecoverable context error.

Solution: track automatic attempts separately from successful manual operations. Make the breaker consecutive/redundant (for example, repeated compaction without meaningful size reduction), and reset it after successful model output or a material context drop.

#### C6 — Medium: compactor background work has no lifecycle join

Evidence: embedding/relevance operations are detached and governed by internal flags/cancellation (`internal/agent/compact.go:177-199`, `:346+`), but `Compactor` has no `Close`/wait and `Agent.Close` only closes its event signal (`internal/agent/agent.go:183-191`).

Impact: session teardown/model switches can leave bounded embedding calls alive against old providers. The five-second timeout limits but does not eliminate stale work or tests leaking goroutines.

Solution: give the compactor a lifecycle context, cancel and join in-flight work on close, and reset in-flight flags immediately when changing provider epochs.

#### C7 — Medium: hidden prompts are represented inconsistently

Evidence: local `RunHidden` ultimately relies on a mutable prompt/string path, while the daemon separately sets `requestHidden` and rewrites event text (`internal/daemon/server.go:1000-1021`, `:1507-1536`). The core `Agent.prompt` stores only text (`internal/agent/agent.go:122-124`).

Impact: visibility is a transport-side convention rather than part of the turn identity. New frontends or direct local callers can persist/render harness prompts differently.

Solution: define a `TurnInput{Text, Images, Hidden, RequestID, Source}` and carry it through agent events and persistence in one place.

#### C8 — Medium: turn reservation is released before all end-of-turn work is published

Evidence: comments around `releaseRun` state that `endTurn` releases so listeners see an available agent (`internal/agent/agent.go:520-541`). Persistence notices and `TurnEnd` publication can therefore overlap a new caller taking the reservation.

Impact: the next `TurnStart` can be observed before the prior end sequence is fully delivered/persisted, creating ordering surprises for daemon queues and hooks.

Solution: keep the reservation until the durable append and `TurnEnd` enqueue complete; let daemon queueing start the next turn in response to that canonical end event.

### D. Daemon, client, and swarm

#### D1 — Critical: allowed image attachments cannot fit through the daemon frame limit

Evidence: the TUI allows four images of up to `MaxImageBytes` each (`internal/tui/attach.go:34-49`). `ClientMsg.Images` embeds raw bytes in JSON/base64 (`internal/daemon/protocol.go:117-121`). The server scanner is capped at 4 MiB (`internal/daemon/server.go:1941`), while the client scanner is 8 MiB (`internal/daemon/client.go:31-35`). One near-limit image expands by roughly one third before JSON overhead; multiple images are much larger.

Impact: an attachment accepted by the UI can make the server scanner terminate the connection before it parses the prompt. Large snapshots/events/diffs can fail in the reverse direction too. Scanner-overflow EOF is not converted to a helpful protocol error.

Solution: replace line-delimited bulk payloads with length-prefixed frames and a negotiated maximum, or content-address images/diffs into owner-only blob files and send references. Enforce the limit before `Encode`, return a typed “frame too large,” and test maximum-size client/server round trips.

#### D2 — High: daemon queues and replay rings are item-bounded, not byte-bounded

Evidence: busy-session input appends to an unbounded `[]queuedInput` (`internal/daemon/server.go:1503-1536`). Each item may carry images. The ring retains 4,096 full `agent.Event` values (`internal/daemon/ring.go:9-37`), and events can contain image bytes and unbounded display diffs.

Impact: one client can enqueue prompts/images faster than a model finishes, or a session can retain gigabytes of repeated large events. This is local-user access, but it can OOM the daemon and every session it owns.

Solution: impose per-session byte/count limits with backpressure and visible rejection; store bulk payloads once and retain references; byte-bound the replay ring; bound `Result.Diff` separately from model-visible `Output`.

#### D3 — High: rename does not migrate all identity-indexed swarm/client state

Evidence: `renameSession` renames the session store, todo store, map key, `Session.Name`, and `Agent.Session` (`internal/daemon/server.go:1035-1080`). It does not migrate `swarm.spawnedBy`, `spawnCount`, schemas, inbox entries (`internal/daemon/hub.go:36-63`), file-registry identities, or closures/pollers created with the old name. The attach client captures `snap.Session` in input, command, model, summon, and roster closures (`internal/attachcmd/attach.go:104-240`, `:405-438`).

Impact: after rename, that same window can send to the old session name, list itself as another agent, lose worker result routing, or leave queued coordination messages under an unreachable key.

Solution: create one server identity migration under server/swarm/registry locks, publish a snapshot with the new identity, and make the client hold session name in mutable synchronized state updated by snapshots. Cancel/restart identity-bound pollers.

#### D4 — High: `/connect brave` updates only the initiating live session

Evidence: daemon `connect` saves the key and assigns `sess.built.Brave.APIKey` (`internal/daemon/command.go:56-78`) but does not update `srv.Cfg.Web` or other sessions. Future sessions clone the daemon’s stale startup config.

Impact: the UI reports “saved,” yet a new daemon session may have no Brave tool/key until the daemon restarts; existing peer sessions also retain the old value.

Solution: use the same server-wide live-config update mechanism as provider credentials, update all Brave clients, and keep the server config synchronized with the durable file.

#### D5 — High: credential changes skip busy sessions forever and accept arbitrary targets server-side

Evidence: `setCredential` saves before validating that the target is a configured provider (`internal/daemon/command.go:242-260`). It rebuilds matching providers only for sessions idle at that moment (`:265-286`) and records no deferred refresh.

Impact: a crafted socket client can append an unintended provider section. More commonly, every busy session keeps using the old credential even after it becomes idle, while the UI reports success.

Solution: validate the target/kind before saving, centralize credentials behind a dynamic provider credential source, or mark sessions dirty and rebuild at the next safe point. Publish success only after all affected runtimes have either updated or queued the update.

#### D6 — High: updater acknowledges daemon stop before shutdown completes

Evidence: `MsgStop` sends status and then starts `go s.Close()` (`internal/daemon/server.go:1959-1962`). `Client.Stop` returns on that status (`internal/daemon/client.go:169-190`). The updater immediately renames the executable afterward (`cmd/evilcode/update.go:122-132`). `Server.Close` can wait up to ten seconds for builds and then close active sessions (`internal/daemon/server.go:572-623`).

Impact: “stopped” means “shutdown scheduled.” The updater can return while the old daemon still runs tools, writes state, owns/tears down the socket, or races an immediate restart of the new binary.

Solution: stop accepting new work, perform teardown, unlink the socket, then send a final acknowledgement from a dedicated control connection (or wait for EOF plus PID/socket disappearance). Add an update test with an intentionally slow session close.

#### D7 — Medium: roster pollers are never canceled and capture stale session identity

Evidence: every attach starts `go pollRoster(path, snap.Session, swarm)` (`internal/attachcmd/attach.go:240`). The function sleeps forever until a daemon call fails and accepts no context (`:405-438`). Recursive resume/reload re-enters `run` (`:287-301`) without canceling the old poller.

Impact: repeated resumes accumulate two-second dial/list loops. Rename also keeps filtering the old self name. These goroutines can persist as long as the daemon remains reachable.

Solution: derive a context from the TUI run, cancel and join it on exit, and read current identity dynamically.

#### D8 — Medium: fork copies conversation/blobs but not the state its UI implies belongs to the session

Evidence: daemon fork calls only `session.Fork` (`internal/daemon/command.go:133-141`). Todo state is a separate per-session file and is not copied; memory/session-scoped metadata is also not explicitly handled. `session.Transfer`, which writes a handoff summary, is unused outside tests (`internal/session/checkpoint.go:295`, `internal/session/session_test.go:852`).

Impact: a fork looks like a complete branch of work but opens without its plan/todo state. There is no actual user-facing transfer/handoff feature despite the implementation artifact.

Solution: define fork semantics explicitly and either clone todo/checkpoint state transactionally or label it “conversation-only.” Expose a real transfer command or remove the unused API.

#### D9 — Medium: model switch performs an unbounded metadata request under the control lock

Evidence: `setModel` holds `controlMu` and, for heuristic misses, calls `Ollama.Show(context.Background(), model)` (`internal/daemon/server.go:1609-1653`).

Impact: a hung Ollama endpoint can freeze model/credential/control operations for that session indefinitely, and the requesting client gets no bounded failure.

Solution: use `ContextWindowDiscovery`-style timeout, perform discovery before taking the commit lock, then recheck idle/identity and commit atomically.

#### D10 — Medium: dynamic model switch does not update every model-dependent object

Evidence: `setModel` changes `Session.Model`, `Agent.Provider/Model/NumCtx/MaxSteps`, FS flags, memory embedder, and compactor (`internal/daemon/server.go:1680-1699`) but leaves `built.Model` stale and the skill-retrieval closure created in wiring captures the original `prov`. Vision is published only from config override, not discovered `ModelInfo` capability.

Impact: later code reading `built.Model`, skill retrieval, and vision gating can disagree with the live agent/header. A catalog that knows a model has vision cannot enable attachments unless config manually repeats it.

Solution: collect all model-dependent state in a `RuntimeModel` value and swap it as one transaction. Make skill retrieval consult current state and combine explicit override with provider-discovered capabilities.

#### D11 — Medium: socket frame errors are poorly diagnosed

Evidence: the server scanner exits its loop on `Scan()==false` without sending `sc.Err()` to the client (`internal/daemon/server.go:1938-1945`); the client converts a clean close to generic `ErrClosed` (`internal/daemon/client.go:45-58`).

Impact: oversized or malformed transport input looks like an unexplained daemon disconnect—the exact symptom produced by D1.

Solution: where possible, return a bounded protocol error before closing; log scanner errors server-side with connection/session context; distinguish frame-too-large from EOF client-side.

### E. TUI commands, pickers, widgets, and rendering

#### E1 — High: local cross-provider model-switch failure still reports success and creates a split runtime

Evidence: in the local branch, provider `Build` errors are ignored (`internal/tui/app.go:2828-2844`), then header/model fields are updated unconditionally (`:2846-2852`). The old `Agent.Provider` can remain active while `Agent.Model` and the header name the new provider/model.

Impact: the next request sends a model belonging to provider B through provider A while the UI says the switch succeeded.

Solution: prepare the entire target provider/capability state first, return a visible error on any failure, and commit all fields only after success. Prefer deleting this local branch if standalone mode remains unreachable (E8).

#### E2 — High: image rejection consumes attachments but sends their placeholders

Evidence: `submit` draws/clears the prompt, calls `TakeAttachments`, and when vision is false only appends an error (`internal/tui/app.go:3783-3830`). The text still contains `[image N]` placeholders and `Agent.Run` still starts.

Impact: the user loses staged images and the model receives a misleading prompt referring to absent images.

Solution: validate before mutating transcript/editor state. Either block submission and retain attachments, or strip placeholders with explicit user confirmation.

#### E3 — High: `submit` ignores all non-busy `Agent.Run` errors

Evidence: the goroutine handles only `errors.Is(err, agent.ErrBusy)` and discards every other return (`internal/tui/app.go:3832-3844`).

Impact: errors that occur before the normal event path emits a useful terminal state can leave a drawn prompt with weak/no feedback. The proxy forwarding path is especially dependent on socket send errors being visible.

Solution: emit a UI error/turn-end for any returned error not already represented, with an error identity to avoid duplicates.

#### E4 — High: the productivity dashboard fabricates “prompt” and daily activity statistics

Evidence: `CollectStats` adds `Info.Messages` to both `Messages` and `Prompts`, and assigns every message in a session to that session file’s `Modified` day (`internal/tui/productivity.go:48-79`). It declares token fields but never populates or renders them (`:19-28`, `:98-130`).

Impact: assistant/tool messages are labeled prompts; a month-long session appears entirely on its last write day; “since” is the oldest modification time, not creation; token metrics are dead. The graph is authoritative-looking but false.

Solution: stream session entries and count user-role prompts, `MetaTokens`, and entry timestamps by day. Persist/start timestamps explicitly. Add fixture tests spanning days and roles.

#### E5 — Medium: picker identity is sometimes only a model name

Evidence: selection/current logic includes paths that compare `sel.Name` or current model without provider qualification, while duplicate model names are valid across providers (`internal/tui/picker.go`, `internal/tui/app.go:2782-2940`). Vision lookup after local switch calls `m.visionFor(sel.Name)` rather than `targetRef` (`internal/tui/app.go:2912-2917`).

Impact: two providers exposing the same model can highlight/select the wrong row and apply the wrong vision override.

Solution: make canonical `model@provider` the picker key everywhere; use display name only for rendering.

#### E6 — Medium: provider failures are hidden and opening the picker can mutate effort state

Evidence: failed catalog providers disappear (B9). Capability normalization and current-selection logic can adjust the active reasoning effort while the user is merely opening/navigating the picker.

Impact: discovery is not observational, and a temporary sparse catalog can change a persisted/user-visible setting.

Solution: keep catalog inspection pure. Normalize/commit effort only on an accepted model selection or explicit reasoning command.

#### E7 — Medium: hidden command queue has capacity one

Evidence: `/plan` stores its prompt in a single `m.queuedHidden` while interrupting (`internal/tui/app.go:3262-3283`); other hidden commands use the same slot.

Impact: issuing another hidden command while a turn unwinds overwrites the first without a queue or warning.

Solution: use a bounded FIFO of typed turn inputs, or reject a second queued hidden action visibly.

#### E8 — Medium: most standalone TUI wiring is dead production code

Evidence: `tuicmd.Run` now delegates normal operation to `attachcmd.RunDefault`, while `runSessions`/`runOnce` still assemble a complete local provider, MCP, memory, tools, hooks, model switching, and TUI path (`internal/tuicmd/tuicmd.go`). Package coverage is only 6.4%. Several bugs above exist only or differently in that branch.

Impact: two architectures must be maintained but only one is exercised by users. Fixes routinely land in one path and drift in the other.

Solution: remove the local implementation, or expose it deliberately as `--local` and give it parity tests. Prefer one `wiring.Build` path for every runtime.

#### E9 — Medium: help says Ctrl+T toggles queue mode, but the binding was removed

Evidence: `HelpKeys` contains `Ctrl+T` (`internal/tui/commands.go:219-233`); tests explicitly assert Ctrl+T is removed from active behavior. Queueing is automatic on Enter while processing.

Impact: users are taught a nonexistent control.

Solution: remove the line and derive help from actual keymap/actions wherever possible.

#### E10 — Medium: visual acceptance suite is stale and not part of the default gate

Evidence: `probe/probe_test.go` is build-tagged and requires a prebuilt binary. The current run fails 25 captures: `ask-open`, `ask-answered`, `bg-started`, `tui-diff`, both tall-panel frames, help, history, image, both memory frames, prompt numbers, two palette frames, panel, picker, plan, scroll, three swarm frames, two todo frames, widgets, and the base TUI answer.

Impact: broad UI changes can pass every normal test. Many current failures are the stale version `v1.1.1` versus `v1.1.3`, but panel/todo frames also show width changes. Until reviewed individually, the suite cannot distinguish intended update from regression.

Solution: scrub the build version from goldens unless version is the test subject, review/update true layout changes, build the binary inside the tagged test or CI script, and run probes in a release workflow.

#### E11 — Medium: Mermaid cache can return the wrong or partial image

Evidence: cache filenames use a 32-bit FNV-style hash (`internal/tui/images.go:237`, `:435-443`); any existing PNG is trusted without validation (`:243-248`); `mmdc` writes directly to the final path (`:261-270`). `LoadImage` trusts bytes as PNG (`:87-99`).

Impact: hash collision reuses another diagram, and a killed/failed renderer can leave a partial file that is accepted forever. Non-PNG data is sent to Kitty as `f=100`.

Solution: use SHA-256, render to a unique temp file, decode/validate the image, fsync, then rename. Make `LoadImage` normalize through `graphics.ToPNG`, like tool-returned images already do.

#### E12 — Medium: `boundedBuffer.Write` violates the `io.Writer` contract

Evidence: when output exceeds room it truncates `p`, writes it, then returns the truncated length (`internal/tui/images.go:305-315`).

Impact: callers can receive `io.ErrShortWrite` or alter behavior merely because diagnostic output reached the cap; the comment says excess should be silently dropped.

Solution: retain `originalLen`, store only the bounded prefix, and return `originalLen, nil`.

#### E13 — Low: `/login` contains duplicate Codex error assignment

Evidence: the identical notice is assigned twice in the missing-account branch (`internal/tui/selftest.go:550-556`).

Impact: no user-visible difference, but it is a concrete sign of copy/paste drift in an authentication path.

Solution: remove the duplicate and cover the branch in the command test.

#### E14 — Low: graphics override accepts arbitrary protocols silently

Evidence: `graphics.Detect` casts any nonempty `EVILCODE_GRAPHICS` directly to `Protocol` (`internal/graphics/graphics.go:49-54`); `ImageSequence` then falls through to no output.

Impact: a typo can leave image mode apparently enabled but render nothing.

Solution: parse `kitty|sixel|none`, warn/fail on anything else, and show the effective renderer in `/config`.

### F. Filesystem, shell, search, and safety tools

#### F1 — Critical: an explicit foreground timeout does not stop the command

Evidence: when the deadline fires, the foreground process is adopted as a background task and keeps running (`internal/tools/exec.go:270-299`). It is killed only for non-deadline cancellation. The task may then run until the fixed 30-minute background ceiling (`:342-380`, `:575-577`).

Impact: `timeout: 5` is presented as a hard bound but a destructive/build/deploy command can mutate the machine for another 29:55 after the model is told it “exceeded” the timeout. This violates the safety contract of the argument.

Solution: kill the process group on explicit/default timeout and return a timeout error. Add a separate opt-in `detach_on_timeout` only if adoption is desired. Test that a marker cannot be written after timeout.

#### F2 — High: explicit background ignores the requested timeout and bypasses foreground serialization

Evidence: the `background` branch returns before interpreting `a.Timeout` (`internal/tools/exec.go:236-245`); `runBackground` always uses `BackgroundTimeout` and does not take `e.run` (`:342-380`).

Impact: callers cannot bound a detached command below 30 minutes. Background mutation can race foreground commands despite the latter’s run lock.

Solution: pass the validated requested duration into `runBackground`, share an effect-aware scheduler/lock for mutations, and expose the effective deadline in task status.

#### F3 — High: background processes outlive session/daemon close

Evidence: `Background` has per-task cancellation but no `Close` that cancels all; `Exec` has no lifecycle close wired into `wiring.Session.Close`. Detached contexts use `context.Background()` (`internal/tools/exec.go:363`).

Impact: quitting, unloading an idle session, updating, or stopping the daemon can leave shell process groups alive for up to 30 minutes, with no owner left to report/cancel them.

Solution: add `Exec.Close`/`Background.Close`, cancel all running groups, wait with a bounded grace period, and register it in wiring closers.

#### F4 — High: “confine to workspace” silently weakens on old kernels

Evidence: on kernels without `openat2`, `openBeneath` falls back to ordinary path open (`internal/tools/confine_linux.go:43-52`). The comment acknowledges it is the older resolve-then-open race, but no user/model warning is emitted.

Impact: a security feature named confinement does not provide confinement under a supported runtime condition. A symlink-swap TOCTOU can escape the workspace.

Solution: fail closed when confinement is enabled and `openat2` is unavailable, or require an explicitly named unsafe compatibility option that is visible in header/config.

#### F5 — High: confined parent creation still has a symlink TOCTOU

Evidence: `mkdirAllConfined` validates, calls path-based `os.MkdirAll` (which follows symlinks), then verifies afterward (`internal/tools/fs.go:619-647`). The post-check cannot undo directories already created outside the root.

Impact: a concurrent symlink swap can create directories beyond the workspace even if the later probe rejects the file write.

Solution: implement descriptor-relative component creation with `mkdirat`/`openat2`, refusing symlinks at each segment.

#### F6 — Medium: atomic writes do not fsync the containing directory

Evidence: ordinary and confined write paths sync file contents then rename (`internal/tools/fs.go:721-750`, `internal/tools/confine_linux.go:68-122`) but never sync the parent directory. Config writing correctly does (`internal/config/config.go:879-884`). LSP commit and updater have the same omission.

Impact: after power loss, a successful rename can be lost even though the function’s comments imply crash durability.

Solution: fsync the held/open parent directory after rename and propagate errors where durability is promised.

#### F7 — Medium: unconfined and confined writes have inconsistent symlink semantics

Evidence: unconfined `writeAtomic` stats through the symlink then renames over the symlink path; confined `writeAtomicBeneath` resolves/operates beneath the target parent with `NOFOLLOW` behavior.

Impact: writing the same path can replace the symlink in one mode and affect/refuse the target in another. Models/users cannot reason consistently about edits.

Solution: choose and document one policy—prefer refusing final symlinks for writes—and enforce it in both modes.

#### F8 — Medium: line anchors are only 16 bits and paged reads replace prior anchor state

Evidence: `AnchorLen=4` (`internal/tools/anchor.go:12-27`). Each `recordAtAnchors` overwrites the file entry (`:75-79`), so reading page two invalidates page-one anchors. Freshness is based on modtime/size plus the target line hash (`internal/tools/anchor.go`, `internal/tools/fs.go:1108-1172`).

Impact: collisions become plausible in large files; multi-page editing is unexpectedly fragile; an off-target change with preserved metadata can evade freshness detection.

Solution: use at least 32–64 bits, store/merge read windows under a full-file content/version hash, and clearly expire all windows when any write is observed.

#### F9 — Medium: glob walks and stores the entire tree before applying `limit`

Evidence: `glob` appends every match during `filepath.WalkDir`, sorts all, then slices (`internal/tools/fs.go:1200-1264`). Recursive `matchSegments` tries every split for `**` without memoization (`:1268-1302`).

Impact: `limit` bounds only output, not work/memory. Adversarial patterns with repeated `**` can create combinatorial matching on a large tree.

Solution: cap scanned files/time/matches, use a memoized/compiled glob matcher, and report truncation based on the bounded search rather than collecting the universe.

#### F10 — Medium: image reads trust filename extension before handing arbitrary bytes to the model/UI

Evidence: dropped paths use `IsImagePath`; tool reading and attachment checks rely heavily on extension/MIME sniffing, while `provider.DetectImageMIME` has a fallback. `LoadImage` does no decode (`internal/tui/images.go:87-99`).

Impact: renamed arbitrary data can be labeled as an image, produce provider errors, or be sent as invalid Kitty PNG.

Solution: decode image headers with bounded readers before attachment; normalize terminal output to PNG; reject unsupported/corrupt input early.

#### F11 — Medium: `multiedit` can partially mutate while returning a successful tool result

Evidence: failed hunks are skipped, successful hunks are written, and the function returns `nil` error with failure text (`internal/tools/fs.go:978-1101`). This is intentional in comments but is not represented structurally as partial failure.

Impact: agent/UI success badges and swarm mutation observation can treat a partially applied plan as complete. A model can miss one `✗` inside a long diff.

Solution: prefer transaction semantics (all hunks validate, then one write). If partial application remains, add `Partial`/`IsError` structured state, surface a warning badge, and require a reread before subsequent edit.

#### F12 — Medium: shell working-directory sentinel is spoofable

Evidence: the wrapper appends a printable `__evilcode_cwd__` marker and parses the last occurrence from combined output (`internal/tools/exec.go:255-269`, `:309-328`).

Impact: a command or descendant can print a later marker and change the session cwd to an unintended path. It is not shell injection—the model already controls the command—but it corrupts tool state and can redirect later operations.

Solution: communicate status/cwd on a dedicated inherited file descriptor or owner-only temp file with a random token, not stdout/stderr.

#### F13 — Medium: subprocesses inherit the daemon’s entire environment, including unrelated secrets

Evidence: shell commands use `e.commandEnv()`, derived from the process environment, and MCP commands call `cmd.Environ()` plus configured entries (`internal/tools/exec.go`, `internal/mcp/mcp.go:74-78`).

Impact: model-requested commands and third-party MCP servers can read API keys/cookies unrelated to their task.

Solution: construct an allowlisted base environment (PATH, HOME/XDG, locale, terminal, explicitly configured variables), and inject provider secrets only into the HTTP client that needs them.

#### F14 — Medium: destructive-command classification is necessarily bypassable but is presented too strongly

Evidence: the gate recognizes known shells/commands and accepts a minimum-length justification; arbitrary interpreters and mutators such as `tee`, `cp`, `mv`, `rsync --delete`, Python, or a script can perform the same action. The model supplies its own justification (`internal/tools/commandrisk`, `internal/tools/exec.go:221-234`).

Impact: this is an advisory reflection gate, not an authorization boundary. A model can rephrase the command or self-approve with unrelated prose.

Solution: describe it honestly as a heuristic. For real protection, gate effects at the filesystem/process boundary, require user confirmation for protected roots, and validate that justification names both the requested operation and target.

### G. MCP, LSP, web, and external integrations

#### G1 — High: MCP tool discovery reads only the first page

Evidence: `Server.loadTools` calls `ListTools(ctx, nil)` once and iterates `res.Tools` (`internal/mcp/mcp.go:91-132`). The MCP protocol’s list operations are cursor-paginated.

Impact: servers with more than one page silently expose only a prefix, with a misleading tool count/header.

Solution: follow `NextCursor` until empty under a total/tool-count bound. Add SDK-backed pagination tests; `internal/mcp` currently has 0% coverage.

#### G2 — High: MCP drops non-text and structured tool results

Evidence: `Server.call` appends only `*sdk.TextContent` and ignores all other content (`internal/mcp/mcp.go:135-174`).

Impact: images, audio, embedded resources, resource links, and structured content vanish. A successful image-only tool returns an empty success, so vision and UI cannot use it.

Solution: map MCP content types into `tools.Result` text/images/display/resource references, preserve structured JSON, and reject unsupported content explicitly rather than silently dropping it.

#### G3 — Medium: MCP does not refresh on `tools/list_changed`

Evidence: tools are loaded only at connect and stored in a static slice (`internal/mcp/mcp.go:82-132`). The client does not register capability/notification handling.

Impact: dynamically added/removed server tools remain stale for the session and can call names the server no longer provides.

Solution: advertise/listen for tool-list changes, atomically rebuild the set, and update the agent tool definitions at a safe point.

#### G4 — Medium: LSP multi-file rename is not crash-atomic despite the API claim

Evidence: docs call `Rename` atomic (`internal/lsp/ops.go:296-305`). Commit renames map entries in nondeterministic order; if a rename fails, rollback uses plain `os.WriteFile` without staging/fsync (`:389-452`). Process/power loss between renames leaves a partial workspace; rollback itself can truncate.

Impact: the strongest wording is false for exactly the catastrophic failure case. Runtime rollback is best-effort, not a transaction.

Solution: call it staged/best-effort unless implementing a durable journal/backups and recovery-on-start. Sort paths, stage rollback files, fsync directories, and keep a recovery manifest until every rename commits.

#### G5 — Medium: LSP `documentChanges` ignores resource operations and URI parsing is incomplete

Evidence: `WorkspaceEdit.DocumentChanges` only decodes text-document edits (`internal/lsp/client.go:217-244`), so create/rename/delete file operations are discarded. `PathFromURI` strips `file://` manually (`:253-260`) and does not validate scheme/authority.

Impact: refactors from servers that include file moves/creates are partially applied. Nonlocal or unusual file URIs can be misinterpreted.

Solution: decode the tagged union, either implement resource operations transactionally or reject the whole edit. Parse with `net/url`, require `file`, and define authority/platform behavior.

#### G6 — Medium: Brave hardcodes a global one-request-per-second policy and does not retry 429

Evidence: `braveMinInterval=1200ms` and comments assert a universal one-rps limit (`internal/tools/brave.go:29-40`, `:282-309`). Errors, including 429, return immediately (`:236-265`). Current Brave plans expose rate limits through response headers and document `X-RateLimit-Reset`/retry behavior.

Impact: plans that allow much higher throughput are unnecessarily serialized, while actual throttling still fails immediately without honoring the service’s reset time.

Solution: use a shared limiter initialized/adjusted from rate-limit headers, honor `Retry-After`/reset on 429 with bounded jittered retry, and make the conservative fallback configurable.

#### G7 — Medium: cloud-usage failures can produce an empty widget, and the response limit is not actually enforced

Evidence: `cloudusage.Fetch` reads through `io.LimitReader(resp.Body, 8<<20)` without reading one extra byte or rejecting overflow (`internal/cloudusage/cloudusage.go:128-140`). A response larger than 8 MiB is silently truncated and can still be parsed as valid. The widget renders text only for `ErrNotLoggedIn` and `ErrNoUsageData`; rate limits, network failures, timeouts, and ordinary HTTP errors produce no diagnostic when no prior snapshot exists (`internal/tui/cloudusagewidget.go:27-62`). Finally, `cookieHeader` says a full `Cookie` header line may be pasted, but it only checks for `=` and does not strip the literal `Cookie:` prefix (`internal/cloudusage/cloudusage.go:143-151`).

Impact: a changed or oversized settings page can yield partial/misleading usage, and the first refresh can fail as a blank card with no remediation. A user following the function's documented full-header input can store a value that will not authenticate.

Solution: read at most `limit+1` bytes and return a distinct overflow error; render every fetch failure in a sanitized, concise form while retaining and timestamping the last good snapshot; honor `Retry-After` for 429; and normalize optional `Cookie:` prefixes before constructing the header. Keep parser fixtures because this integration intentionally depends on private HTML rather than a versioned API.

### H. Persistence, import, durability, and updates

#### H1 — High: external imports do not persist their source model as session model metadata

Evidence: `ImportExternalFile` writes the model into `MetaImport.Note`, not `MetaModel` (`internal/session/import.go:111-119`). Resume model selection reads actual model metadata.

Impact: imported Claude/Codex/OpenCode sessions resume on the current/default model rather than the source model, even when the parser found it.

Solution: map source model/provider where possible and write canonical `MetaModel`; otherwise show an explicit unresolved-model choice during import.

#### H2 — High: importing the same external session never refreshes it

Evidence: if the deterministic native path exists, import returns its current message count without reading/appending new source content (`internal/session/import.go:73-86`).

Impact: importing a still-growing external conversation once permanently freezes the imported prefix. Re-running resume gives no warning and misses new messages.

Solution: persist source path/ID plus an import cursor/hash, append newly observed source entries without overwriting native continuation, and detect divergence explicitly.

#### H3 — High: Codex/OpenCode imports flatten or drop executable conversation structure

Evidence: `parseCodexFile` accepts only message response items and skips function calls, tool outputs, reasoning items, and provider replay state (`internal/session/import.go:438-523`). OpenCode parts flatten tool input/output into text (`:625-706`). Claude parsing collects all tool results after a single assistant value, potentially changing mixed block order (`:390-435`).

Impact: resumed context is semantically different from the source; provider-required tool adjacency/state is lost. Imported sessions are suitable as prose transcripts, not faithful resumable conversations.

Solution: preserve ordered typed items and provider metadata per source, then normalize with explicit compatibility rules. If faithful continuation is impossible, label imports “transcript-only” and start a summarized handoff instead of replaying malformed history.

#### H4 — Medium: external ID resolution accepts ambiguous substring matches

Evidence: `resolveExternalPath` returns the first file whose stem equals **or contains** the requested ID during filesystem walk (`internal/session/import.go:165-207`).

Impact: a short ID can resolve nondeterministically to the wrong conversation.

Solution: exact header/stem match first; if multiple prefix/substring candidates remain, list them and require disambiguation. Sort traversal for stable diagnostics.

#### H5 — Medium: session logs and memory flush but do not fsync each accepted record

Evidence: session append calls `bufio.Writer.Flush` (`internal/session/store.go:324-341`); memory append also only flushes (`internal/memory/store.go:337-354`).

Impact: an OS/power crash can lose recently acknowledged messages/memories despite comments implying crash resumability. Process crash is mostly covered; power-loss durability is not.

Solution: decide the durability contract. If accepted records must survive, use batched fsync at turn boundaries and for critical metadata; if not, soften comments and expose last durable checkpoint.

#### H6 — Medium: native sessions have no cross-process writer lock

Evidence: each `Store` owns only an in-process mutex and opens the same JSONL append path independently (`internal/session/store.go:68-145`, `:311-341`). The daemon prevents duplicate opens inside one process, but another daemon/local process can resume the same name.

Impact: messages and clean-exit markers from two agents can interleave into one logical conversation. Per-write append atomicity does not make conversation order coherent.

Solution: acquire an advisory lock for the lifetime of a writable session, include PID/socket diagnostics, and offer read-only inspection when locked.

#### H7 — Medium: image/blob lifecycle has no garbage collection

Evidence: messages content-address images beside the log; rewind/compaction rewrite the conversation but do not sweep now-unreferenced blobs. Fork copies blob state, including orphans.

Impact: repeated image use, rewind, compact, and fork cause monotonic disk growth.

Solution: compute referenced hashes after any rewrite, atomically move unreferenced blobs to trash, and provide periodic/explicit GC with dry-run accounting.

#### H8 — High: updater and installer authenticate transport, not the binary artifact

Evidence: updater checks HTTPS, host, size, and ELF magic (`cmd/evilcode/update.go:136-179`); installer uses `curl -fL`, chmod, and move (`install.sh:184-212`). Neither verifies a signed manifest or checksum rooted outside the release payload.

Impact: compromise of the release server/account/CDN can replace the executable with arbitrary code that runs as the user. ELF magic only proves file format.

Solution: publish SHA-256 manifests signed with a pinned public key (minisign/cosign or equivalent), verify before chmod/rename, and fail closed. The checksum must not be trusted solely because it came from the same unsigned release response.

#### H9 — Medium: installer reinstall/remove does not coordinate with the daemon and can delete an unrelated `ec`

Evidence: `do_install` overwrites the binary without stopping the daemon; `do_remove` deletes `$(dirname installed_path)/ec` unconditionally after confirmation (`install.sh:246-267`).

Impact: an old daemon continues running after reinstall/removal. If `evilcode` was discovered on PATH beside an unrelated real `ec` file, uninstall removes it too.

Solution: invoke a bounded daemon stop before replacement/removal, and delete `ec` only if it is a symlink whose resolved target/basename is this installation.

#### H10 — Medium: directory durability is inconsistent across update paths

Evidence: config writes fsync their directory; tool writes, LSP commit, updater binary rename, and installer move do not.

Impact: the code uses “atomic” and “durable” interchangeably, but rename atomicity does not guarantee persistence across power loss.

Solution: provide one shared atomic-write/install helper per package boundary that syncs content and directory and clearly documents what it guarantees.

### I. Todo, memory, and long-running automation

#### I1 — High: todo completion gate validates at most one newly completed group

Evidence: `completesGroup` builds a map, iterates it, and returns the first newly completed group (`internal/todo/model.go:741-756`). The caller validates only that group (`:665-684`). Map iteration order is nondeterministic.

Impact: one write can finish multiple groups; if the randomly selected group has sufficient ownership, another group with missing/low ownership is accepted without validation.

Solution: return a stable list of every newly completed group and validate all before applying the transaction. Add a test completing two groups with mixed scores repeatedly.

#### I2 — High: ungrouped todo completion bypasses the hard ownership gate

Evidence: `completesGroup` skips items whose `Group` is nil (`internal/todo/model.go:744-750`), even though UI/group helpers explicitly treat ungrouped work as a bucket.

Impact: a flat plan can be marked entirely complete with no end-to-end ownership evidence, defeating the central hard gate for the simplest/default todo shape.

Solution: canonicalize nil group to a stable ungrouped key and apply the same gate, or make grouping mandatory when quality gates are enabled.

#### I3 — Medium: status can contradict dependencies

Evidence: validation checks missing dependencies and cycles (`internal/todo/model.go:648-663`) but does not reject an in-progress/completed item whose `blocked_by` items remain incomplete.

Impact: the plan can say an item is both complete and blocked; downstream summaries/gates are then unreliable.

Solution: derive blocked state from dependency status rather than storing contradictory status, or reject transitions until dependencies are completed/cancelled under an explicit rule.

#### I4 — Medium: overnight stall detection compares only the todo summary

Evidence: `ShouldContinue` receives a string state and treats equality as no movement (`internal/tui/overnight.go:155-203`). Callers commonly pass the concise todo summary. Edits/tests can progress while counts/status remain unchanged, and wording/presentation changes can look like progress without useful work.

Impact: unattended work can stop after three productive turns that do not change todo counts, or continue because superficial todo text changed.

Solution: compute a structured progress fingerprint from todo revision, repository diff/test evidence, completed tool checks, and blocked transitions. Require tangible progress, not summary-string inequality.

#### I5 — Medium: memory enablement and semantic readiness are conflated

Evidence: a memory manager can be “ON” while its active provider cannot embed (B7); status reports enabled/count/scope but not embedding failures/readiness (`internal/daemon/command.go:172-229`).

Impact: users believe semantic recall is active when it is operating only partially or erroring in the background.

Solution: expose lexical versus semantic mode, embedding provider/model, last error, and queued work in `/memory status` and the widget.

### J. Testing, observability, and maintainability

#### J1 — High: there is no automated release gate

Evidence: no tracked CI workflow exists. Normal `go test ./...` omits the build-tagged tmux suite, which currently fails broadly.

Impact: provider protocol regressions, stale goldens, installer shell errors, cross-platform compilation failures, and low-coverage command paths can ship from a locally green unit run.

Solution: add CI for format/vet/unit/race (race can be nightly if expensive), build matrix, `go test -tags probe`, `shellcheck install.sh`, and provider contract fixtures. Make release publishing depend on the gate.

#### J2 — High: critical integration packages have no tests

Evidence: coverage is 0% for MCP and attach command; serve/resume/probe/jsonl wrappers also have none, while CLI/headless/TUI command wiring is very low.

Impact: D1, D3, D7, G1, G2, and command-registry drift live exactly at untested seams.

Solution: prioritize contract tests over more renderer unit tests: max daemon frames, rename while attached, resume poller cancellation, MCP pagination/multimedia, clean-home startup, and every visible slash command.

#### J3 — Medium: comments repeatedly promise stronger behavior than code supplies

Examples: missing config is a working setup (A1); role fallback chains are “working provider” fallback (A5); timeout is a bound (F1); confined operation is secure on all paths (F4/F5); LSP multi-file rename is atomic (G4); productivity prompts/days/tokens are real (E4).

Impact: maintainers and users make decisions from contracts that tests do not actually enforce.

Solution: turn invariant comments into executable contract tests. Where a guarantee is intentionally best-effort, use that phrase and surface the degradation.

#### J4 — Medium: generated/demo provider fixtures make repository-wide code searches noisy

Evidence: `internal/provider/demo_search.go` contains very large embedded transcripts and foreign source excerpts. Broad symbol/security searches hit those strings as though they were Evilcode code.

Impact: audits and static grep are easier to misread, and production binary/source size grows for demo content.

Solution: move large scenarios to `testdata`/embedded assets with clear generated headers, or build-tag demo providers out of release builds when they are not user-facing.

## Things that are already sound and should be preserved

The corrective work should retain several good foundations:

- Unix-socket path/owner/mode checks and peer credential checks are appropriately strict for a shell-bearing daemon (`internal/daemon/protocol.go`, socket security files).
- The daemon prevents duplicate in-process session construction and shares one memory store across sessions (`internal/daemon/server.go:70-101`, `:790-970`). This avoids a serious class of concurrent JSONL corruption.
- Ollama streaming explicitly requires `done:true` and uses a monotonic synthetic tool-call sequence (`internal/provider/ollama.go:200-270`). Use it as the provider contract model.
- Codex Responses streaming does require `response.completed` and treats `incomplete`/`failed` as errors (`internal/provider/codex.go:760-900`).
- Session names, symlinks, permissions, append repair, and exclusive creation receive careful defensive handling (`internal/session/store.go`).
- File writes stage, sync, and rename instead of truncating in place; confined writes use parent descriptors on modern Linux (`internal/tools/fs.go`, `internal/tools/confine_linux.go`). Add parent fsync/fail-closed behavior rather than replacing the design.
- Tool batches always produce one result per requested call, including overflow/cancellation, preserving provider adjacency (`internal/tools/tools.go:120-205`).
- Event payloads are sanitized before terminal rendering, and UI rendering is kept on Bubble Tea’s update path rather than background goroutines.
- Visual probes isolate HOME/XDG/tmux state and reset fixtures carefully (`probe/probe.sh`, `probe/probe_test.go`). The mechanism is good; it needs to be current and mandatory.

## Recommended implementation sequence

### Release blocker patch set

1. Correct Ollama/DeepSeek defaults and current reasoning catalogs (A1, A2).
2. Replay DeepSeek reasoning and enforce terminal completion in both provider and agent layers (B1–B5).
3. Replace/bypass NDJSON for images and other bulk data, with byte bounds/backpressure (D1, D2, D11).
4. Make command timeout kill, and cancel background work on runtime close (F1–F3).
5. Make rename a full identity migration and make attached client identity mutable/cancellable (D3, D7).
6. Fix all 25 probe expectations/regressions, then gate releases on them (E10, J1).

### Correctness patch set

1. Centralize a transactional runtime-model/live-config swap (A4, D4, D5, D9, D10, E1, E5).
2. Preflight context before every model call and repair compaction counters/lifecycle (C1, C2, C5, C6).
3. Add effect-aware tool scheduling, duplicate validation, and structured partial failures (C3, C4, F11).
4. Complete MCP and LSP protocol handling (G1–G5).
5. Repair import fidelity/refresh/model metadata and add session locking/blob GC (H1–H7).
6. Validate all completed todo groups including ungrouped work (I1–I3).

### Product/clarity patch set

1. Implement `/config`; remove dead Ctrl+T help and either implement or remove `lenient_tool_parse` (A6, A9, E9).
2. Rebuild productivity from actual entries/timestamps/tokens (E4).
3. Make provider/model/memory readiness and degraded modes visible (B9, E6, I5).
4. Remove or officially support the duplicate local TUI architecture (E8).
5. Add signed artifact verification and coordinated install/update lifecycle (H8–H10).

## Final conclusion

The project is not structurally unsound; it is suffering from boundary drift. Inside individual packages, the code is generally careful. The failures appear where a guarantee crosses boundaries: provider stream to agent, image to JSON frame, daemon identity to client closure, config file to live provider, timeout to background registry, source transcript to native session, or widget label to stored data. Fixing those boundaries and turning their comments into contract tests will produce a much more reliable system than adding more features now.

The current release should not be considered clean-install ready until A1, B1/B2, D1, D3, F1, and the failing probe suite are resolved.
