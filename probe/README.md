# probe — how an agent sees the TUI

evilcode is built by an agent, so every visual claim has to be checkable without
a human watching. The rig boots evilcode in a headless tmux pane, drives it with
keystrokes, and captures each frame twice: plain text for goldens, ANSI for PNGs.

    go build -o evilcode ./
    probe/probe.sh boot                 # 140x40 pane, deterministic mode
    probe/probe.sh keys "/help" Enter   # tmux send-keys syntax
    probe/probe.sh frame welcome        # -> frames/welcome.{txt,ansi}
    probe/probe.sh png welcome [size]   # also -> frames/welcome.png
    probe/probe.sh kill

Goldens catch regressions; PNGs catch ugliness. Look at the PNG — that is the
step that cannot be skipped.

    go test -tags probe ./probe/...              # diff frames vs goldens/
    UPDATE_GOLDENS=1 go test -tags probe ./probe/...

`scenarios/*.txt` are line-based: `boot [args]`, `keys <tmux keys>`,
`capture <golden name>`, `kill`. Blank lines and `#` comments are ignored.

Frames are rendered with a Nerd Font when one is installed, falling back to the
embedded Go Mono. `evilcode probe fonts` lists the loaded faces and every design
glyph none of them can draw — that is the explanation for any tofu box in a PNG.
Raise `-size` on `probe render` when detail is too small to judge.

The pane runs with `EVILCODE_DETERMINISTIC=1` (frozen animations, fixed session
name, no wall-clock text) and a throwaway `HOME`, so probing never touches real
config or session state and frames are reproducible.
