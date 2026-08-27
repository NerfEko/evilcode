# evilcode

![License](https://img.shields.io/badge/license-GPL--3.0-blue?style=for-the-badge)
![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![Built with Charm](https://img.shields.io/badge/built%20with-Charm-FF5F87?style=for-the-badge)
![Ollama](https://img.shields.io/badge/ollama-cloud%20%26%20local-000000?style=for-the-badge&logo=ollama&logoColor=white)
![Linux](https://img.shields.io/badge/linux-only-FCC624?style=for-the-badge&logo=linux&logoColor=black)

> **A note before you use it:** evilcode was built with AI. I have extensively
> cross-verified it against other open-source coding agents wherever the features
> overlap, but it is still an active personal project and should be treated that way.
> Pull requests and feature requests are welcome.

evilcode is a coding agent for the terminal. It gives a model a real tool set — files,
shell commands, git, LSP, memory, and more — and puts the conversation in a fast,
keyboard-first TUI.

I originally built evilcode for myself, so it has opinions. Some of them may not match
how you would design a coding agent, and the project is not especially extensible yet.
That is part of the current state of the project, not a promise that every edge case has
been solved. If you want it to work differently, open an issue or send a PR.

![evilcode auditing a real codebase for panic-shaped bugs](demo/demo-search.gif)

## What it feels like

evilcode is single-user and local by default. There is no account, telemetry, or hosted
workspace. A small per-user daemon owns the live sessions, while terminal windows are
just clients that can connect, disconnect, and reconnect. Conversations are stored as
JSONL files on your machine so a session can be resumed later.

The main command opens the TUI and starts the daemon when needed:

```sh
evilcode
```

From there you can type a prompt, resume a session, inspect files, run commands, review
diffs, switch models, and keep working while tools run in the background.

## Install

The quickest install is the latest Linux/amd64 release:

```sh
curl -fsSL https://evileko.dev/evilcode | sh
```

This installs `evilcode` and an `ec` symlink in `~/.local/bin`. The installer prints what
it is doing and warns if that directory is not on your `PATH`.

Already installed? Update in place with:

```sh
evilcode update
```

The installer also offers update, reinstall, removal, and config reset options instead
of silently replacing an existing install.

### Build from source

You need Go 1.26 or newer:

```sh
git clone https://git.evileko.dev/evileko/evilcode
cd evilcode
go build -o evilcode ./cmd/evilcode
ln -sf "$PWD/evilcode" ~/.local/bin/evilcode
ln -sf "$PWD/evilcode" ~/.local/bin/ec
```

### Shell completions

```sh
evilcode completions zsh  > ~/.zfunc/_evilcode
evilcode completions bash > /etc/bash_completion.d/evilcode
evilcode completions fish > ~/.config/fish/completions/evilcode.fish
```

## Usage

```sh
evilcode                              # open the TUI
evilcode run "fix the parser"         # submit a prompt and return
evilcode run --wait "explain this"    # submit and stream the answer
evilcode serve                        # run the daemon in the foreground
evilcode serve -status                # inspect the daemon
evilcode serve -stop                  # stop it cleanly
evilcode attach [session]             # attach to an existing daemon session
evilcode attach -l                    # list sessions
evilcode resume --from claude <id-or-path>
```

`evilcode run` hands the prompt to the daemon and exits. The agent, tools, background
tasks, and session can keep working after that shell closes. Use `--wait` when you need
the answer streamed back, or `--local` when you want the old in-process one-shot mode.

`evilcode attach` needs a daemon that is already running. A TUI window can close without
ending the session; another window can reconnect to it later. A session with no window
is kept hydrated for ten minutes after its last turn/window activity, then it is cleanly
closed and unloaded. Its transcript remains available for resume.

## Requirements

Linux only.

At runtime, `rg` powers the `grep` tool and `tmux` is used by the probe rig. These are
optional when you do not use the features that need them:

- `gopls` for LSP features
- `mmdc` for Mermaid diagrams
- `img2sixel` for terminals without Kitty graphics support

## Models and configuration

The config file is `~/.config/evilcode/config.toml`, unless `EVILCODE_CONFIG` points
somewhere else. A missing config is okay; evilcode starts with sensible defaults. An
invalid one fails at startup with a single error listing every problem's TOML path,
so a misconfigured file can be fixed in one edit instead of one restart per field.

Ollama Cloud is the easiest route to try. With `OLLAMA_API_KEY`, the default model is
`deepseek-v4-flash:0731@ollama-cloud` (reasoning effort high); without a key, the local Ollama route is used when it is
available. You can also use OpenAI-compatible providers, DeepSeek, Codex, Ollama Local,
or the deterministic mock provider used by tests.

For example:

```toml
default_model = "deepseek-v4-flash:0731@ollama-cloud"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"

[[model]]
name = "glm-5.2:cloud"
context_window = 262144

[display]
theme = "catppuccin-frappe"  # catppuccin-frappe | dracula | nosferatu | gloom | daywalker
inline_diffs = true
centered = false

[features]
memory = true
advisor = false
max_steps = 0            # 0 means unlimited tool rounds
```

If the Codex CLI is installed and logged in, evilcode can discover its OAuth account
from the normal Codex auth file. `/model` and `-m` select a model; `/reasoning` changes
the advertised reasoning effort when a provider supports it. The model catalog is
fetched live from each provider once per session and then cached, so newly released
models will not appear until you run `/refresh-model-list` (or restart) — it drops the
cache and re-runs discovery against every configured provider.

Use `/connect brave` to enable the optional Brave-backed `web_search` tool. Credentials
are masked and stored in the user-only config, or can be supplied through
`BRAVE_SEARCH_API_KEY` / `BRAVE_API_KEY`.

Ollama Cloud exposes no usage API, so the Cloud Usage widget reads
`https://ollama.com/settings` with your browser session. Paste the
`__Secure-session` cookie value (DevTools → Network → any ollama.com request →
Cookies) with `/connect ollama-usage` — it is stored masked in the user-only
config, like `/connect brave` stores its key — or set `OLLAMA_SESSION_COOKIE`
(env beats the saved value; a value with a `;` or a cookie-name prefix such as
`__Secure-session=` is treated as a full `Cookie:` header, anything else as the
bare `__Secure-session` value, so base64 `=` padding inside the token is fine).
`/connect ollama-usage status` reports presence without printing it. The
widget then shows Session and Weekly quota bars, each slice colored per model.
Treat the cookie like a password — it is a live session credential. The scrape
is unofficial; if the page changes, the widget reports that instead of
guessing.

Repository-specific defaults can live in `.evilcode.toml` at the repository root. Model
and role overrides are supported there; credentials are deliberately not.

## The useful bits

### Terminal UI

The TUI is built on the Charm stack and is designed for a real terminal rather than a
browser imitation. It includes:

- streaming Markdown and syntax-highlighted code
- clickable shell code blocks that copy clean commands to the clipboard
- inline diffs, a pinned diff view, and whole-file change gutters
- a split view that follows the agent's file activity: `Ctrl+L` opens a live
  pane on each touched file scrolled to the change, `Ctrl+Q` closes it, and the
  wheel scrolls whichever side the mouse is over
- model and reasoning pickers
- `/help`, `/theme`, `/diff`, `/compact`, `/rewind`, and history search with `Ctrl+R`
- Kitty and sixel image display when the terminal supports it

The start page shows existing sessions and a live preview so resuming one does not feel
like guessing which creature name you meant.

### Tools

The built-in tools include `read`, `write`, `edit`, `glob`, `grep`, `bash`, `bg`, `ask`,
`todo`, git helpers, optional web search, LSP, and MCP servers.

Long shell commands can move into the daemon's background task manager. Use `bg status`,
`bg output`, `bg wait`, or `bg cancel` to manage them. A command copied from a `bash` or
`fish` code block is cleaned of comments and trailing whitespace before it reaches the
clipboard.

`grep` can include the surrounding function, method, or type. `edit` supports hash-
anchored changes so stale context is refused instead of being applied fuzzily.

### Sessions and memory

Every message is written as it arrives. `/compact` and `/rewind` rewrite logs atomically,
and resumed sessions restore their working directory and last model. The daemon marks
idle shutdowns cleanly; if it is stopped while a turn is genuinely active, that run is
left crash-detectable so it cannot be mistaken for a completed answer.

Memory is optional and best-effort. Relevant facts can be recalled into a turn, and a
summary is written when a session is actually torn down. If the embedding provider is
unavailable, lexical matching remains available.

### Unattended work and swarms

`/overnight` works through a todo list without a window attached. It is bounded by turns,
tokens, wall clock, and stalled progress, and writes a small report when it stops.

Sessions can delegate work to headless workers through `spawn_worker` or `/summon`.
`spawn_worker` runs in the foreground: the turn waits, and the worker's result —
validated against the JSON Schema the parent supplies — comes back directly as the
tool result, so delegation reads like a blocking call. `/summon` starts a worker
without waiting; its result arrives as a message when it finishes. Heartbeats make a
silent worker visible as stale instead of leaving everyone waiting forever. Shared file
and todo state lets a small swarm coordinate without each worker keeping a private copy.

## Keys

| Key | Action |
|---|---|
| `Enter` | send; queue while a turn is running |
| `Shift+Enter`, `Alt+Enter`, trailing `\` | newline |
| `Esc` | cancel overlays, interrupt, then clear input |
| `Ctrl+C` | interrupt; press twice while idle to quit |
| `Ctrl+R` | search prompt history |
| `Ctrl+G` | toggle a scroll bookmark |
| `Alt+B` | send a running tool to the background |
| `Ctrl+L` | toggle the live split view |
| `Ctrl+Q` | close the split |
| `PgUp`, `PgDn` | scroll one page |
| `↑`, `↓` on empty input | scroll one line |

Run `/terminal-setup` if your terminal does not distinguish `Shift+Enter` from Enter.

## Data and privacy

evilcode does not send telemetry and does not require an evilcode account. Local state
lives under `~/.local/share/evilcode/`:

- `sessions/*.jsonl` — conversations and tool history
- `prompt-history.jsonl` — local prompt history
- `memory.jsonl` — optional durable memory
- session blobs and detached overnight reports

Treat those files like private work logs. They can contain prompts, code, command output,
and anything a model was shown.

## Contributing

This project began as a tool for one person, so the edges are opinionated and some seams
are tighter than they should be. That is exactly why outside feedback is useful.

Feature requests, bug reports, documentation fixes, and pull requests are welcome. If
you are changing behavior, please include a focused test where practical. Before opening
a PR, run:

```sh
go test ./...
go test -race ./...
go vet ./...
```

The probe rig can exercise a real binary in a tmux pane and capture PNG frames:

```sh
go build -o evilcode ./cmd/evilcode
go test -tags probe ./probe/...
probe/probe.sh boot
probe/probe.sh keys "/help" Enter
probe/probe.sh png help
probe/probe.sh kill
```

## Project layout

```text
cmd/evilcode        executable entrypoint and updater
internal/agent       model loop, events, and hooks
internal/tui         terminal UI and attach mirror
internal/attachcmd   socket client and remote TUI wiring
internal/tuicmd      default TUI entrypoint
internal/runcmd      local and daemon-backed headless runs
internal/servecmd    daemon entrypoint
internal/provider    Ollama, OpenAI-compatible, DeepSeek, Codex, and mock providers
internal/tools       built-in tools
internal/daemon      server, sessions, reconnects, and swarms
internal/wiring      shared provider/tool/session construction
internal/memory      semantic memory bank
```

evilcode is GPL-3.0. If you try it, find something rough, or have an idea that would
make it more useful, let me know.
