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

## What works today

Phase 0 — bootstrap and the probe rig.

- `internal/ansirender`: ANSI → PNG renderer (SGR 0/1/2/3/7/22/23/27, 30–37, 90–97,
  40–47, 100–107, 38;5;n / 48;5;n, 38;2;r;g;b / 48;2;r;g;b, 39/49), wide-glyph aware.
- `evilcode probe`: `render` (ANSI → PNG), `text` (ANSI → plain), `fonts` (glyph
  coverage diagnostic), `hello` (bubbletea smoke app).
- `probe/probe.sh`: tmux driver — `boot / keys / frame / png / kill`.
- Golden-frame tests behind the `probe` build tag.

Nothing else is wired yet. See `plan.md` for the full spec and phase list.

## Config reference

Not implemented yet (Phase 1). Paths will be `~/.config/evilcode/config.toml` and
`~/.local/share/evilcode/`.

Environment variables in use today:

| var | meaning |
|---|---|
| `EVILCODE_DETERMINISTIC=1` | fixed session name, frozen animations, no wall-clock text |
| `EVILCODE_PROVIDER=mock` | use the canned deterministic provider |
| `EVILCODE_GLYPH_SAFE_MODE=on\|off` | override for terminals whose glyph atlas chokes on animation |
| `EVILCODE_PROBE_FONT` | probe PNG primary font: four colon-separated paths (regular:bold:italic:bolditalic) |
| `EVILCODE_PROBE_FONTS` | probe PNG fallback fonts, colon-separated; point at a monochrome emoji font for emoji artwork |

## Keymap

Not implemented yet (Phase 1). See `plan.md` §11 for the target keymap.

## Probe rig

The probe rig is how an agent verifies the TUI without a human watching.

```sh
go build -o evilcode ./            # the binary probe.sh drives
probe/probe.sh boot                # start a 140x40 tmux pane running evilcode
probe/probe.sh keys "/help" Enter  # send keys
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
