# evilcode

![License](https://img.shields.io/badge/license-GPL--3.0-blue?style=for-the-badge)
![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![Built with Charm](https://img.shields.io/badge/built%20with-Charm-FF5F87?style=for-the-badge)
![Ollama](https://img.shields.io/badge/ollama-cloud%20%26%20local-000000?style=for-the-badge&logo=ollama&logoColor=white)
![Linux](https://img.shields.io/badge/linux-only-FCC624?style=for-the-badge&logo=linux&logoColor=black)

A terminal coding agent. Agentic tool-calling loop, a TUI built on the Charm stack,
and first-class support for Ollama Cloud alongside anything that speaks the OpenAI API.

Single user, no telemetry, no account. Sessions are JSONL files on your own disk.

## Requirements

Go 1.26 or newer. Linux only.

At runtime: `rg` for the `grep` tool, `tmux` for the probe rig. Optional extras are
used if present — `gopls` for the `lsp` tool, `mmdc` for mermaid diagrams, `img2sixel`
for images on terminals without the kitty graphics protocol.

## Install

```sh
git clone https://git.evileko.dev/evileko/evilcode
cd evilcode
go build -o evilcode ./
```

Put it on your PATH with a symlink rather than a copy, so a rebuild takes effect
without re-linking:

```sh
ln -sf "$PWD/evilcode" ~/.local/bin/evilcode
ln -sf "$PWD/evilcode" ~/.local/bin/ec
```

Shell completions:

```sh
evilcode completions zsh  > ~/.zfunc/_evilcode
evilcode completions bash > /etc/bash_completion.d/evilcode
evilcode completions fish > ~/.config/fish/completions/evilcode.fish
```

## Usage

```sh
evilcode                      # interactive TUI
evilcode run "fix the parser" # headless, one shot
evilcode serve                # daemon hosting sessions
evilcode attach [session]     # attach a TUI to the daemon
evilcode run --remote "..."   # submit into a running daemon
```

`evilcode run` writes the model's text to stdout and everything else to stderr, so it
composes with other tools. It exits 130 on interrupt.

Sessions are the source of truth: every message is written as it lands, `/compact` and
`/rewind` rewrite the log atomically — a backup is written and synced before the
primary is replaced, and the live session follows the rewrite — and a turn that could
not be fully written says so rather than leaving a session that comes back short on
resume. Closing a session waits for its in-flight turn to finish, so a shutdown or a
closed terminal does not drop the messages the turn was still writing.

## Configuration

`~/.config/evilcode/config.toml`, or wherever `$EVILCODE_CONFIG` points. Every key has
a default and a missing file is a working setup.

```toml
default_model = "glm-5.2:cloud@ollama-cloud"

[[provider]]
name = "ollama-cloud"
kind = "ollama"                  # ollama | openai | mock
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"

[[model]]
name = "glm-5.2:cloud"
context_window = 262144          # override what the provider reports
anchor_edits = true              # hash-anchored edit mode

[roles]
smol = ["qwen3:8b@ollama-local"] # every internal side-call routes here

[display]
theme = "dracula"                # dracula | nosferatu | gloom | daywalker
thinking_lines = 6               # height of a live reasoning trace
keep_thinking = false            # leave finished traces expanded
inline_diffs = true
centered = false

[features]
auto_poke = true
memory = true
advisor = false
confine_to_workspace = false     # restrict file tools to the launch directory

[lsp]
go = ["gopls"]
```

Declaring any `[[provider]]` replaces the built-in set entirely, so a provider can be
removed. Everything else merges over the defaults.

A repository can pin its own `default_model` and `[roles]` in `.evilcode.toml` at its
root. Provider credentials are deliberately not overridable that way: checking out a
repository must not be able to redirect your API keys.

State lives in `~/.local/share/evilcode/` as `sessions/*.jsonl`,
`prompt-history.jsonl` and `memory.jsonl`.

### Environment

| Variable | Effect |
|---|---|
| `EVILCODE_CONFIG` | config file path |
| `EVILCODE_PROVIDER=mock` | use the deterministic canned provider |
| `EVILCODE_DETERMINISTIC=1` | fixed session name, frozen animation, no wall-clock text |
| `EVILCODE_GRAPHICS` | force `kitty`, `sixel` or `none` |
| `EVILCODE_DICTATE` | speech-to-text command for `evilcode dictate` |

## Features

### Terminal UI

The transcript hugs the composer while the conversation is short and becomes a
scrolling viewport when it overflows. Widgets dock into the blank margin beside the
text at no layout cost, anchored to a transcript line so they scroll with the content
rather than skittering as it moves.

Markdown prose, syntax-highlighted code blocks with streaming chrome, and inline diffs
tinted toward add and delete while keeping their highlighting. `/diff` cycles a side
panel between an inline diff, a pinned one, and a whole-file view with change gutters.

Overlays cost zero layout height: the slash palette, `Ctrl+R` history search across
every session, an inline model picker, and a full-screen help. Four palettes ship, with
Oklab harmony scoring and generation from a seed color behind `/theme`.

### Planning and follow-through

`/plan` runs a planning turn and renders the answer as a plan card. The `todo` tool
records confidence, intent and feedback-loop scores per item, and their histories are
tool-owned, so a model cannot author its own evidence trail.

When work is marked finished with nothing to show for it, the harness says so and asks
again. Every path that can re-prompt has a circuit breaker, because the ones that did
not have looped in production. A todo write is atomic: it stages every file and commits
them together, so a failure leaves the prior state on disk and in memory rather than
half of each.

### Memory

Durable facts outlive the session. Incoming messages are embedded and matched against
the bank; anything close enough goes in as a single `<memories>` note, shown in the
transcript as a tile listing exactly what was injected. An injection you cannot see is
one you cannot correct.

Ambient extraction mines facts every eight turns through the `smol` role, and a session
summary is written on exit, which is what makes the session picker searchable by what a
session was about rather than by its name. A dead embedder degrades recall to substring
matching rather than switching it off.

### Daemon and swarms

`evilcode serve` holds any number of sessions in one process and speaks NDJSON over a
unix socket. `attach` is the ordinary TUI with the socket as its event source, so two
terminals can follow one conversation and a closed terminal loses nothing. Per-session
ring buffers cover reconnects.

Agents inside the daemon coordinate. A shared file registry tells an agent when another
rewrote a file it had read, delivered between turns rather than mid-thought.
`send_message`, `broadcast` and `peers` route through the daemon. `spawn_worker` and
`/summon` start headless workers whose results are validated against a JSON Schema the
spawner supplies, so nobody has to parse prose.

The shared plan and todo list span the swarm: one store per namespace, held by
reference, so every worker sees the same items rather than a private half that the
last writer overwrites. A session runs one turn at a time — a second client that
arrives mid-turn interleaves into the running turn rather than launching a second run
against one conversation — and worker counts stay within their caps under concurrent
`/summon`. Repo overrides are applied to a per-build copy of the config, so one
repository's pinned model or roles never leak into another session's.

### Tools

`read`, `write`, `edit`, `glob`, `grep`, `bash`, `ask`, `todo`, git helpers, and MCP
servers adapted into the same interface. Batches dispatch through a fixed worker pool
with a cap on both concurrency and total size.

`edit` has a hash-anchored mode: `read` prints a short content hash beside each line,
and an edit names the anchor instead of reproducing surrounding context. Stale anchors
are refused rather than fuzzily matched.

The `lsp` tool covers diagnostics, definition, references, hover, symbols and rename,
with gopls preconfigured. One server is launched per language and shared by every
caller, so concurrent calls do not spawn duplicates. A rename computes every touched
file in memory before anything reaches disk, so it cannot half-apply.

`write` and `edit` replace a file through a same-directory temp file and a rename, so
nothing else ever reads a half-written version, and two edits to one file in the same
batch are serialized rather than both computing against the original.

### Graphics

Mermaid fences render through `mmdc` and display inline via the kitty graphics protocol
on kitty, ghostty and WezTerm, or `img2sixel` elsewhere. Without a renderer the source
is shown with highlighting and a line naming what is missing. `Alt+Shift+I` switches
between pictures and placeholders.

### Unattended and self-hosted work

`/overnight` works the todo list with nobody watching, bounded by turns, tokens, wall
clock, and consecutive turns that fail to move the list. The token budget accumulates
across turns rather than resetting, so it stops on real cost. It stops on its own and
says which limit stopped it.

`/selfdev` opens a session on this repository with a skill describing the development
loop. `/rebuild` builds, tests and restarts into the new binary. It runs the tests
before restarting, because restarting into a binary that fails its own tests is how a
self-modifying program locks itself out.

## Keys

| Key | Action |
|---|---|
| `Enter` | send; interleave into a running turn; queue in queue mode |
| `Ctrl+Enter` | the opposite of the current send mode |
| `Shift+Enter`, `Alt+Enter`, trailing `\` | newline |
| `Esc` | layered cancel: overlays, then interrupt, then clear input |
| `Ctrl+C` | interrupt; twice when idle to quit |
| `Ctrl+R` | history search |
| `Ctrl+T` | toggle queue mode |
| `Ctrl+G` | toggle scroll bookmark |
| `Alt+B` | send a running tool to the background |
| `PgUp`, `PgDn` | scroll a page |
| `↑`, `↓` on empty input | scroll a line |

Readline bindings (`Ctrl+U/K/W/A/E/B/F/Z/S`) work in the composer. Run
`/terminal-setup` for help making `Shift+Enter` distinguishable in your terminal.

## Testing

```sh
go test ./...                                     # unit tests
go test -tags probe ./probe/...                   # golden frame tests, needs tmux
UPDATE_GOLDENS=1 go test -tags probe ./probe/...  # rewrite goldens
```

The probe rig drives a real binary in a tmux pane, captures frames and renders them to
PNG, so changes to the interface can be checked without a person watching:

```sh
go build -o evilcode ./
probe/probe.sh boot                 # 140x40 pane
probe/probe.sh keys "/help" Enter
probe/probe.sh png help             # probe/frames/help.png
probe/probe.sh kill
```

Goldens catch regressions; the PNGs are for looking at. A golden only proves a frame
has not changed, never that it is right.

## Project layout

```
internal/agent       loop, events, hooks — never imports the TUI
internal/tui         everything on screen
internal/provider    ollama, openai, mock
internal/tools       the tool set
internal/daemon      serve, attach, swarm coordination
internal/memory      the semantic memory bank
internal/lsp         language server client
internal/ansirender  ANSI to PNG, for the probe rig
probe/               tmux driver, scenarios, goldens
```

`internal/agent` does not import bubbletea, and a test enforces it. That separation is
what lets the headless runner, the daemon and the probe rig share one implementation.

`plan.md` holds the specification. `DEVIATIONS.md` records where the build differs from
it and why. `LOOPS.md` is the build log.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
