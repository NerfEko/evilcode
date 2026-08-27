# Post-mortem: nebula-4 hallucination + chronic tool-arg repairs (2026-08-26)

Written for a reviewer agent asked to harden evilcode against the failure modes
below. Every claim is backed by something measured; the reproduction is cheap
and scripted. Author: evilcode review session (2026-08-26/27), working from the
nebula-4 session log plus live replays against ollama-cloud.

## TL;DR

nebula-4 (glm-5.3-flash@ollama-cloud) produced two degenerate replies in a
3-hour session: one at minute 2 and the final one at minute 170. The final one
generated 163,241 bytes (~40k tokens) of repetitive multilingual filler for
9.7 minutes and then the session exited.

- **Primary cause: the model.** Stochastic repetition-loop degeneration, not
  context exhaustion. It happened once at **21,187 tokens (2% of the window)**
  and once at **521,702 tokens (50%)**. Replaying the identical final state
  produced coherent output 3/3 times.
- **ollama-cloud is exonerated on integrity.** It served the full 521,702-token
  prompt with no truncation and no error (`prompt_eval` confirms the count),
  prefill in ~16 s, well-formed streams.
- **evilcode did not malfunction**, but two of its policies parked the session
  in the danger zone and let the degenerate reply run to completion:
  relative-only compaction thresholds (0.85 × advertised 1M window ≈ 891k
  tokens, never reached at ~522k) and no output cap / no degeneration guard.
- **The "broken tool calls" are a separate, chronic GLM quirk**: glm-5.3-flash
  emits `command` where evilcode's bash schema says `cmd`. The existing alias
  repair (built for deepseek) caught 329/329 of them; the repair rate was
  steady all session and is NOT evidence of late-session degradation.

## 1. Session facts

| | |
|---|---|
| Session log | `~/.local/share/evilcode/sessions/nebula-4.jsonl` (1,480 events) |
| Model / provider | `glm-5.3-flash@ollama-cloud`, reasoning effort `max` |
| Window reported by cloud | `glm5_next.context_length = 1048576` (live `/api/show`) |
| Task | Verify all 47 findings in `docs/codex_review2.md`, fix, commit+push+LOOPS each |
| Duration | 20:02:49 → 22:53:05 (2h50m), clean_exit |
| Real work delivered | 17 verified fix commits, each pushed (`afaea0d`…`e4b6459`), plus the R2-05 probe root-cause hunt |
| Tool results / assistant events | 751 / 721 |
| Tool errors | 45 of 751 (6%) — mostly gate holds and edit misses, all recovered |
| Degenerate replies | 2 (events 18 and 1478 of the log) |

## 2. Failure timeline

| Time | Event | Context at that request |
|---|---|---|
| 20:04:37 | todo write correctly rejected (duplicate item id `t44`) | ~21k tok |
| 20:05:11 | **Degeneration #1**: 7.7 KB repetitive filler, zero thinking, zero tool calls. User asked "what just happened?"; the model's own retrospective (event 20) is accurate | 21,187 tok (2%) |
| 20:06–22:41 | Fully productive: 17 committed fixes including a genuine TUI streaming-cache root-cause hunt (R2-05) | 175,527 tok @ 21:00 |
| 22:41:59 | Last tool call; 9.7-minute silence follows | ~522k tok |
| 22:51:39 | **Degeneration #2**: 163,241 bytes (~40k tokens), zero thinking, zero tool calls. Semi-coherent start → recites evilcode's system prompt → CJK/Arabic/Latin token salad | 521,702 tok (50%) |

The 9.7-minute stall is fully accounted for by decode time (~40k tokens at
~70 tok/s). The cloud was not hanging; the model was generating garbage.

## 3. Measured evidence (live replays, 2026-08-26/27)

Method: rebuild the exact request from the session JSONL (assistant `reasoning`
is replayed as the `thinking` field by `internal/provider/ollama.go`
`toOllamaMessages`), then POST to `https://ollama.com/api/chat` with the same
model and `options.num_ctx = 1048576`. Scripts were in `/tmp/nebula-repro/`
(ephemeral); the essential commands are inline below.

1. **No truncation, no cap at ~500k.** Final state (1,477 messages, 2.09 MB
   body): `prompt_eval = 521,702`, HTTP 200 in 15.8 s. Mid-state (458 msgs):
   `prompt_eval = 175,527`. First-failure state: `prompt_eval = 21,187`.
2. **The degeneration is stochastic, not input-driven.** Same final state,
   `think:"max"`, 3 samples: all coherent (eval 372–600 tokens).
3. **Recall at full depth works.** At the 522k state the model quoted the first
   user message verbatim and answered "47 findings, R2-01..R2-47,
   docs/codex_review2.md" correctly. The context is not silently cut.
4. **Behavior at depth degrades without losing memory.** One probe spent a
   2,000-token budget on 6.2 KB of thinking and produced no answer; thinking
   sizes at 522k ranged 0 B–6.2 KB across probes (stochastic).
5. **Cloud capacity limits exist**: back-to-back large prefills can get
   `HTTP 429 {"error":"timed out waiting for a concurrent request slot"}`.
6. **Thinking availability is not a stable depth function** — small-context
   probe returned thinking; several 522k replays returned none despite
   `think:"max"`.

```bash
# reproduce (1) — token accounting at full depth
python3 - <<'PY'
import json, os, urllib.request
key = [l.split('"')[1] for l in open(os.path.expanduser('~/.config/evilcode/config.toml'))
       if l.strip().startswith('api_key')][0]  # first api_key = the ollama-cloud block (verify: not sk-...)
msgs = json.load(open('/tmp/nebula-repro/final_msgs.json'))  # rebuild per §6
body = json.dumps({'model':'glm-5.3-flash','messages':msgs,'stream':False,
                   'think':'max','options':{'num_ctx':1048576,'num_predict':16}}).encode()
req = urllib.request.Request('https://ollama.com/api/chat', data=body, method='POST')
req.add_header('Authorization','Bearer '+key)
r = json.load(urllib.request.urlopen(req, timeout=600))
print(r['prompt_eval_count'])   # -> 521702: served in full, not truncated
PY
```

## 4. Attribution

### 4.1 Model — primary cause
- Degeneration at 2% depth (21k tokens) cannot be a context effect.
- Identical 522k input regenerates coherently → stochastic loop, not a
  deterministic response to the transcript.
- Chronic schema slips all session: 329 `command→cmd` rewrites on bash calls
  (see §5), steady rate per hour (66/179/84 against event counts
  217/295/209 — no end-of-session spike, so not depth-correlated either).
- Both degenerate replies share one signature: no thinking tokens, no tool
  calls, recited-prompt filler. A thinking model that suddenly stops thinking
  and rambles is the tell.

### 4.2 ollama-cloud — exonerated on integrity
- Served 521,702-token prompts completely and quickly; `prompt_eval` proves it.
- Stream framing was clean (the 163 KB reply is valid JSON/UTF-8 end to end).
- Only infra signal observed: 429 concurrency-slot timeouts under my probe
  load. evilcode already classifies 429 as retryable
  (`internal/provider/ollama.go:662-667`) and retries before first delta
  (`internal/agent/agent.go:776-788`), which is the right shape.

### 4.3 evilcode — no malfunction, four exposure multipliers
Nothing computed wrongly. But the harness design maximized time-in-danger-zone
and blast radius:

- **F1 — Relative-only compaction threshold.** `CompactThreshold = 0.85`
  (`internal/agent/compact.go:42-48`, applied at :610 and :623) is a fraction
  of the *advertised* window. With a 1M-window model the session legitimately
  ran to 522k tokens (50%) for hours. The 1M number is GGUF architecture
  metadata, not a quality guarantee; measured behavior (long non-convergent
  thinking, occasional zero-thinking degenerate output) degrades far earlier.
- **F2 — No output bound, no degeneration guard.** `ChatStream`
  (`internal/provider/ollama.go:159-195`) never sets `num_predict`, and the
  agent loop has no repetition detector. A zero-tool-call, 163 KB, 10-minute
  reply ran to completion and was then persisted to the durable transcript.
- **F3 — Chronic GLM arg aliasing is absorbed silently in aggregate** (§5).
- **F4 — No per-request usage in the session log.** `prompt_eval_count` is
  already parsed into `Usage` (`ollama.go:249-252`) and folded into
  `a.lastCtx` (`internal/agent/agent.go:881`), but the session JSONL records
  no usage meta events — this post-mortem required live replays to measure
  depth. Add a usage meta event per turn.

## 5. The broken tool calls: 329 × `command→cmd`

Mechanism (all verified in source):

1. GLM-family models chronically emit `{"command": "..."}` for bash; evilcode's
   bash schema says `cmd`. Training bias beats the schema ("Models emit the
   names they were trained on far more stubbornly than they read the schema" —
   `internal/tools/tools.go:407-412`).
2. `RunOne` repairs before the strict decode: `repairArgs` → `repairObject`
   (`internal/tools/tools.go:477-528`), alias table `argAliases` at :413
   (`command→cmd`, `file_path→path`, `filePath→path`, `old_string→old`,
   `new_string→new`, `replace_all→all`; conditional `pattern→query` at :431).
   Aliases apply only when the schema has the real name and the real name is
   absent, at every object level (nested multiedit included).
3. The model is told each time: the agent appends
   `Note: tool arguments were repaired: command→cmd` to the tool result
   (`internal/agent/agent.go:1001`), and the TUI/attachcmd render
   `· repaired: …` on the tool row (`internal/tui/transcript.go:620-626`).
   The session JSONL persists the `repairs` array per event.

Cross-session attribution (repair events per model, all local session logs):

| Model | Sessions | Repair events | Rate |
|---|---|---|---|
| glm-5.3-flash@ollama-cloud | 11 | 463 | **~42/session** |
| glm-5.2:cloud@ollama-local | 105 | 96 | ~0.9/session |
| deepseek-v4-flash:0731@ollama-local | 16 | 0 | 0 |
| gpt-5.6-luna@codex | 8 | 0 | 0 |

Notes for the reviewer:
- In nebula-4, **all 329 repairs were exactly `command→cmd` on bash** — a
  single, stable bias, not general schema blindness. The two `unknown field
  "op"` errors (multiedit given edit-tool fields) were NOT repairable and the
  model self-corrected from the error text. The alias machinery recovered
  329/329 bash calls; zero calls were lost to it.
- The alias table was added for deepseek ("sent 'command' five times running",
  tools.go:409). Today deepseek sends zero; glm-5.3-flash is the chronic
  offender at ~45× glm-5.2's rate. The mechanism works; the signal is not
  aggregated anywhere.
- Repair rate did NOT spike before either degeneration — do not treat repair
  counts as a degradation detector by themselves.

### Fix sketches

1. **F1 — absolute context ceiling.** In the compaction check, use
   `threshold := min(CompactThreshold*window, absoluteCap)` with `absoluteCap`
   configurable (suggest default ~200k for `context_length >= 500k` models).
   This keeps 128k models exactly as today and stops 1M-advertised models from
   running sessions at 500k+.
2. **F2 — bound degenerate output.** (a) Set `num_predict` from config (suggest
   default 16k–32k for agent turns) in `ollama.go` `ChatStream` and the other
   providers; (b) add a streaming repetition detector (n-gram/suffix loop over
   the accumulated text) that aborts the turn with a retryable error and a
   clear notice; (c) a turn that emits >64 KB of text with zero tool calls and
   zero thinking is almost certainly degenerate — mark it in the transcript
   meta rather than letting it persist as an ordinary assistant message.
3. **F3 — make repairs visible in aggregate.** Count repairs per session/model
   into the status surface (and the picker if cheap). Optional: one schema
   description hint on bash's `cmd` ("the shell command to run; parameter name
   is `cmd`") to shave the rate. Do NOT drop the alias — strict-only decode was
   the retry-loop failure mode the alias exists to prevent.
4. **F4 — usage meta events.** Emit a session-log meta event per turn with
   `prompt_eval_count` / `eval_count` (already available in `Usage`), plus the
   reasoning-token emptiness ratio; that makes depth-related post-mortems a
   one-line grep instead of a replay campaign.

## 6. Reproduction notes

Scripts (ephemeral, in /tmp — rebuild if lost):

- `build_requests.py` — converts `nebula-4.jsonl` into wire-shaped message
  arrays (`final_msgs.json` = state that produced the degenerate reply,
  excluding it; `mid_msgs.json` = 21:00 state).
- `recall_test.py` — depth-recall probes at 522k/175k.
- `sizeprobe.py` / `repair_stats.py` — request-size bisection and the
  cross-session repair attribution table.

All probes used the user's own `ollama-cloud` key from
`~/.config/evilcode/config.toml`, `num_predict` bounded (16–2000 tokens).

## 7. Open questions

- Whether the 429 retry path has been exercised against ollama-cloud in
  practice (code path verified; behavior under real back-to-back large prefills
  untested in-session).
- Whether cloud-side serving quality at >500k depth differs from local
  inference with the same weights (unobservable from here). The evidence only
  shows behavior degrades *somewhere* in the deep-context regime; the token
  accounting is exact.
- Whether a smaller `num_ctx` sent in `options` changes GLM's behavior at the
  same effective prompt size (num_ctx was 1,048,576 in all replays, matching
  production).