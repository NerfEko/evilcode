# evilcode 🦇

A personal AI coding agent harness: terminal UI, agentic tool-calling loop, first-class
Ollama Cloud support. Go + the Charm stack. Linux-only, single user, no telemetry.

The agent loop is table stakes; the feel is the product.

## Build

```sh
go build ./...        # produces ./evilcode
go test ./...
```

Requires Go 1.26+. Runtime deps: `tmux` and `rg` (ripgrep) for the probe rig and the
`grep` tool respectively.

## Quickstart

```sh
./evilcode              # TUI (default)
./evilcode run "..."    # headless one-shot
./evilcode probe ...    # probe rig driver (see below)
```

To run it from anywhere, symlink it onto your PATH. Symlinks (rather than shell
aliases) keep pointing at whatever `go build` last produced, so a rebuild needs
no re-linking, and they work in scripts and non-interactive shells too:

```sh
ln -sf "$PWD/evilcode" ~/.local/bin/evilcode
ln -sf "$PWD/evilcode" ~/.local/bin/ec      # short form
```

## What works today

All five phases complete. See `plan.md` for the full spec and `DEVIATIONS.md` for where the build differs from it.

**Interactive TUI** — `evilcode tui`

- Packed-vs-scrolling transcript: while the conversation fits, it hugs the composer;
  on overflow it becomes a scrolling viewport with a hysteretic scrollbar.
- Widgets docked into the transcript's blank margin at zero layout cost, anchored to a
  transcript line so they scroll with the content instead of skittering.
- Rainbow-decayed prompt numbers on the user band, markdown prose, syntax-highlighted
  code blocks with streaming chrome, and inline diffs tinted toward add/delete while
  keeping their highlighting.
- Slash palette and Ctrl+R history search (both zero layout height), an inline model
  picker, and a full-screen help overlay.
- Centered mode, elastic pull-to-reveal facts line, typing scroll lock.
- Four palettes with `/theme`, including Oklab scoring and palette generation.

**The harness argues back** — the §12 discipline system

`/plan` injects a planning turn and renders the reply as a violet plan card. The `todo`
tool records confidence, intent, and feedback-loop scores; histories are tool-owned so
the model cannot author its own evidence trail. Auto-poke pushes back at turn end when
work is marked done without validation, with circuit breakers on every path.

**Memory** — `/memory`, and the `remember` / `recall` / `reflect` tools

Durable facts survive the session. Every user message is embedded and matched against
the bank; anything close enough goes in as one `<memories>` note and shows in the
transcript as a 🧠 tile listing exactly what was injected. Ambient extraction mines
durable facts every eight turns through the `smol` role, and a session summary is
stored on exit, which is what makes the session picker searchable by what a session was
about rather than by its name. `/memory` reports, `/memory list` shows ids, `/memory
forget <id>` drops one, `/memory off` stops all of it. A dead embedder degrades recall
to substring matching rather than disabling it.

**Daemon and swarms** — `evilcode serve`, `evilcode attach`, `evilcode run --remote`

One process holds N sessions and speaks NDJSON over a unix socket. `attach` is the
ordinary TUI with the socket as its event source, so two terminals can watch one
conversation and a killed terminal loses nothing; the header names both ends,
`server: Crypt ⚰ · client: Bat 🦇`. Per-session ring buffers cover reconnects.

Inside the daemon, agents coordinate: a shared file registry tells one agent when
another rewrote a file it had read, delivered between turns rather than mid-thought;
`send_message`, `broadcast`, and `peers` route through the daemon; `spawn_worker` and
`/summon` start headless workers whose results are validated against a JSON Schema the
spawner supplies, so nobody parses prose. Live agents show in the SwarmStatus widget,
with a one-line strip as the fallback when it cannot dock.

**Images and diagrams**

Mermaid fences render through `mmdc` and display inline with the kitty graphics
protocol (kitty, ghostty, WezTerm; sixel through `img2sixel` elsewhere). Without a
renderer the source shows styled with a line saying what would render it. `Alt+Shift+I`
switches between pictures and placeholders.

**Language servers and a second opinion**

The `lsp` tool speaks to configured servers with gopls preconfigured — diagnostics,
definition, references, hover, symbols, rename. A rename reads and rewrites every
touched file in memory before anything reaches disk. `/advisor` puts a cheap second
model on the conversation; it raises at most one concern per turn.

**Unattended work** — `/overnight`

Works the todo list with nobody watching, under four caps: turns, tokens, wall clock,
and consecutive turns that do not move the list. `/productivity` renders what you have
been doing as a dashboard and a PNG.

**Working on evilcode itself** — `/selfdev`

Opens a session on this repository with a skill encoding the development loop.
`/rebuild` builds, tests, and restarts into the new binary; `/reload` restarts keeping
the session.

**Headless** — `evilcode run "prompt"`

Text on stdout, tool rows and notices on stderr, exit 130 on interrupt. `--remote`
submits into a running daemon and streams the same output back.

**Under the hood** — provider clients (Ollama native, OpenAI-compatible, and a
deterministic mock), TOML config with `model@provider` refs and role routing, the tool
set (read/write/edit with hash anchors, glob/grep/bash, git helpers, `ask`) with
bounded-concurrency batching, the agent loop with safe-point interleaving and retry
classification, and JSONL sessions with crash detection.

Shell completions: `evilcode completions bash|zsh|fish`. Speech to text:
`evilcode dictate`, wired to whatever STT command you configure.

## Config reference

`~/.config/evilcode/config.toml`, or `$EVILCODE_CONFIG`. Everything has a default; a
missing file is a working setup.

```toml
default_model = "glm-5.2:cloud@ollama-cloud"

[[provider]]
name = "ollama-cloud"
kind = "ollama"                # ollama | openai | mock
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"

[[model]]
name = "qwen3-coder:480b-cloud"
context_window = 262144        # override what the provider reports

[roles]                        # every internal side-call goes through smol
smol = ["qwen3:8b@ollama-local"]

[display]
# How tall a live thinking trace grows before it scrolls in its own space.
thinking_lines = 6
# Leave finished traces expanded instead of folding them to "▸ thought (N lines)".
keep_thinking = false
# Show a diff under each edit in the transcript.
inline_diffs = true
theme = "dracula"
centered = false

[features]
auto_poke = true
confine_to_workspace = false   # true restricts file tools to the launch directory
```

Declaring any `[[provider]]` replaces the defaults entirely, so a provider can be
removed. Everything else merges over them.

Data lives under `~/.local/share/evilcode/` — `sessions/*.jsonl` and
`prompt-history.jsonl`.

Environment variables in use today:

| var | meaning |
|---|---|
| `EVILCODE_DETERMINISTIC=1` | fixed session name, frozen animations, no wall-clock text |
| `EVILCODE_PROVIDER=mock` | use the canned deterministic provider |
| `EVILCODE_GLYPH_SAFE_MODE=on\|off` | override for terminals whose glyph atlas chokes on animation |
| `EVILCODE_PROBE_FONT` | probe PNG primary font: four colon-separated paths (regular:bold:italic:bolditalic) |
| `EVILCODE_PROBE_FONTS` | probe PNG fallback fonts, colon-separated; point at a monochrome emoji font for emoji artwork |

## Keymap

| key | action |
|---|---|
| `Enter` | submit; interleave into a running turn; queue in queue mode |
| `Ctrl+Enter` | the opposite of the current send mode |
| `Shift+Enter` / `Alt+Enter` / trailing `\` | newline |
| `Esc` | layered cancel: close overlays, interrupt, then clear input |
| `Ctrl+C` | interrupt; twice when idle to quit |
| `Ctrl+T` | toggle queue mode |
| `Ctrl+G` | toggle a scroll bookmark |
| `Ctrl+U/K/W/A/E/B/F/Z/S` | readline edits |
| `PgUp` / `PgDn` | scroll a page |
| `↑` / `↓` (empty input) | scroll a line |
| wheel | momentum scroll |
| `!` prefix | run the line as a shell command |

`/terminal-setup` explains how to make `Shift+Enter` work in your terminal.

## Probe rig

The probe rig is how an agent verifies the TUI without a human watching.

```sh
go build -o evilcode ./            # the binary probe.sh drives
probe/probe.sh boot                # start a 140x40 tmux pane running evilcode
probe/probe.sh serve               # start a daemon on a private socket
probe/probe.sh attach [session]    # open a client pane against it; splits if one exists
probe/probe.sh keys "/help" Enter  # send keys (--pane=N to pick a pane)
probe/probe.sh frame welcome       # capture plain + ANSI frames to probe/frames/
probe/probe.sh png welcome         # capture and render probe/frames/welcome.png
probe/probe.sh kill                # tear down
```

Golden tests:

```sh
go test -tags probe ./probe/...              # compare frames against probe/goldens/
UPDATE_GOLDENS=1 go test -tags probe ./probe/...   # rewrite goldens
```

PNGs are for the agent's own eyes; goldens catch regressions.
