# probe — how an agent sees the TUI

evilcode is built by an agent, so every visual claim has to be checkable without
a human watching. The rig drives a real evilcode client in a headless tmux pane and
captures each frame twice: plain text for goldens, ANSI for PNGs. For daemon-backed
scenarios the server runs outside tmux; the pane is only an attach client, which is
the same process boundary a user gets when a terminal closes and reconnects.

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

Daemon scenarios use the additional verbs:

    probe/probe.sh serve --scenario=chat
    probe/probe.sh attach "" bat
    probe/probe.sh attach dracula spider
    probe/probe.sh frame two-clients
    probe/probe.sh kill

`serve` binds a short private socket outside tmux, and each `attach` opens another
client pane against it. `kill` tears down both the tmux clients and the daemon. Keep the
socket short: Linux limits Unix socket paths to 107 bytes. The daemon-backed probe also
uses the throwaway XDG directories and deterministic mock provider described below, so
it cannot touch a user's real sessions or credentials.

Frames are rendered with a Nerd Font when one is installed, falling back to the
embedded Go Mono. `evilcode probe fonts` lists the loaded faces and every design
glyph none of them can draw — that is the explanation for any tofu box in a PNG.
Raise `-size` on `probe render` when detail is too small to judge.

The client and daemon run with `EVILCODE_DETERMINISTIC=1` (frozen animations, fixed
session name, no wall-clock text), a mock provider, and throwaway XDG directories, so
probing never touches real config or session state and frames are reproducible. A
daemon-backed frame should be captured from the client panes; daemon startup output is
kept in the probe scratch directory rather than rendered into a golden.
