#!/usr/bin/env bash
# probe.sh — tmux driver for the evilcode self-test rig (plan.md §14).
#
#   probe.sh boot [cmd...]     start a 140x40 pane running evilcode (default: probe hello)
#   probe.sh keys <k>...       send keys to the pane (tmux send-keys syntax)
#   probe.sh frame <name>      capture probe/frames/<name>.txt (plain) and .ansi (styled)
#   probe.sh png <name> [size] capture, then render probe/frames/<name>.png
#   probe.sh kill              tear the session down
#
# The binary is built by the caller: go build -o evilcode ./

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$REPO/evilcode"
FRAMES="$REPO/probe/frames"

SOCKET=evilprobe
COLS=${PROBE_COLS:-140}
ROWS=${PROBE_ROWS:-40}

# The tmux server socket is a unix socket, and unix socket paths cap at 108
# bytes. A long TMPDIR silently breaks tmux, so pin a short one.
export TMUX_TMPDIR="${TMUX_TMPDIR:-/tmp/evilprobe-$UID}"
mkdir -p "$TMUX_TMPDIR"

# A throwaway HOME keeps the probe away from the real config and session store,
# so a probe run can never scribble on the user's actual state.
FAKEHOME="${PROBE_HOME:-$TMUX_TMPDIR/home}"

tm() { tmux -L "$SOCKET" "$@"; }

# settle waits for the pane to stop changing, so a capture never lands mid-frame.
# Bubble Tea's synchronized output makes each frame atomic; this just makes sure
# we are not sampling between two of them.
settle() {
    local prev="" cur="" i
    for ((i = 0; i < 40; i++)); do
        cur="$(tm capture-pane -p -t evil 2>/dev/null || true)"
        [[ "$cur" == "$prev" && -n "$cur" ]] && return 0
        prev="$cur"
        sleep 0.05
    done
    return 0
}

require_session() {
    tm has-session -t evil 2>/dev/null || {
        echo "probe: no session; run 'probe.sh boot' first" >&2
        exit 1
    }
}

cmd_boot() {
    [[ -x "$BIN" ]] || {
        echo "probe: $BIN missing; run: go build -o evilcode ./" >&2
        exit 1
    }
    cmd_kill
    mkdir -p "$FAKEHOME" "$FRAMES"

    local app=("$BIN" probe hello)
    [[ $# -gt 0 ]] && app=("$BIN" "$@")

    tm new-session -d -s evil -x "$COLS" -y "$ROWS" \
        "env HOME='$FAKEHOME' TERM=xterm-256color COLORTERM=truecolor \
             EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock ${app[*]}"
    settle
}

cmd_keys() {
    require_session
    tm send-keys -t evil "$@"
    settle
}

cmd_frame() {
    require_session
    local name="${1:?usage: probe.sh frame <name>}"
    mkdir -p "$FRAMES"
    tm capture-pane -p -t evil >"$FRAMES/$name.txt"
    tm capture-pane -e -p -t evil >"$FRAMES/$name.ansi"
    echo "$FRAMES/$name.txt"
}

cmd_png() {
    local name="${1:?usage: probe.sh png <name> [size]}"
    local size="${2:-16}"
    cmd_frame "$name" >/dev/null
    "$BIN" probe render -size "$size" "$FRAMES/$name.ansi" "$FRAMES/$name.png"
    echo "$FRAMES/$name.png"
}

cmd_kill() {
    tm kill-session -t evil 2>/dev/null || true
}

case "${1:-}" in
    boot)  shift; cmd_boot "$@" ;;
    keys)  shift; cmd_keys "$@" ;;
    frame) shift; cmd_frame "$@" ;;
    png)   shift; cmd_png "$@" ;;
    kill)  shift; cmd_kill ;;
    *)
        sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 1
        ;;
esac
