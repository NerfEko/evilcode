#!/usr/bin/env bash
# probe.sh — tmux driver for the evilcode self-test rig (plan.md §14).
#
#   probe.sh boot [cmd...]     start a 140x40 pane running evilcode (default: probe hello)
#   probe.sh serve             start a daemon on a private socket (no pane)
#   probe.sh attach [session]  open a client pane against it; splits if one exists
#                              PROBE_SCENARIO picks the mock provider's script
#   probe.sh keys <k>...       send keys to the pane (tmux send-keys syntax)
#   probe.sh frame <name>      capture probe/frames/<name>.txt (plain) and .ansi (styled)
#   probe.sh png <name> [size] capture, then render probe/frames/<name>.png
#   probe.sh kill              tear the session down
#
# The binary is built by the caller: go build -o evilcode ./cmd/evilcode

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${EVILCODE_BIN:-$REPO/evilcode}"
# Captured frames land here. It is overridable for the same reason the socket
# is: two concurrent runs capture the same golden names, and the second write
# would land between the first run's capture and its read.
FRAMES="${PROBE_FRAMES:-$REPO/probe/frames}"

# PROBE_ID separates concurrent runs. Every step of a scenario is a separate
# probe.sh invocation against one long-lived pane, so two runs sharing a socket
# interleave: one run's `boot` tears down the other's session, and the keys and
# captures that follow land in the wrong pane. That does not fail loudly — it
# produces a golden containing another scenario's transcript, which is exactly
# the bug this rig exists to catch. The test harness sets it to its own pid.
PROBE_ID="${PROBE_ID:-manual}"
SOCKET="evilprobe-$PROBE_ID"
COLS=${PROBE_COLS:-140}
ROWS=${PROBE_ROWS:-40}

# The tmux server socket is a unix socket, and unix socket paths cap at 108
# bytes. A long TMPDIR silently breaks tmux, so pin a short one.
export TMUX_TMPDIR="${TMUX_TMPDIR:-/tmp/evilprobe-$UID-$PROBE_ID}"
mkdir -p "$TMUX_TMPDIR"
chmod 700 "$TMUX_TMPDIR"

# A throwaway HOME keeps the probe away from the real config and session store,
# so a probe run can never scribble on the user's actual state.
#
# HOME alone is not enough: XDG_DATA_HOME and friends are absolute paths that
# are commonly exported in a login shell, and they take precedence over HOME in
# the XDG lookup. Leaving them set silently defeats the whole isolation, so they
# are pinned under the fake home rather than merely inherited.
FAKEHOME="${PROBE_HOME:-$TMUX_TMPDIR/home}"

tm() { tmux -L "$SOCKET" "$@"; }

# settle waits for the pane to stop changing, so a capture never lands mid-frame.
# Bubble Tea's synchronized output makes each frame atomic; this just makes sure
# we are not sampling between two of them.
# STABLE_SAMPLES is how many identical captures in a row count as settled.
#
# One repeat is not enough: a keypress the app has not started reacting to yet
# looks exactly like a finished frame, so a scenario that sends two keys in
# quick succession can capture the state after only the first. That failure is
# intermittent and produces a golden that is simply wrong, which is the worst
# kind of flake a golden rig can have.
STABLE_SAMPLES=3

settle() {
    local prev="" cur="" i stable=0
    for ((i = 0; i < 60; i++)); do
        cur="$(tm capture-pane -p -t evil 2>/dev/null || true)"
        if [[ "$cur" == "$prev" && -n "$cur" ]]; then
            stable=$((stable + 1))
            [[ $stable -ge $STABLE_SAMPLES ]] && return 0
        else
            stable=0
        fi
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

# reset_fixtures restores everything a scenario mutates, so a probe run is
# repeatable rather than succeeding only the first time. Both halves matter:
# the git-tracked files a scenario edits, and the throwaway HOME where sessions,
# prompt history, and todo state accumulate. Stale todo state in particular
# makes a write produce no delta, so the rows it should draw silently vanish.
reset_fixtures() {
    [[ -d "$REPO/testdata" ]] && git -C "$REPO" checkout -- testdata 2>/dev/null || true
    rm -rf "$FAKEHOME/.local/share/evilcode"
}

cmd_boot() {
    [[ -x "$BIN" ]] || {
        echo "probe: $BIN missing; run: go build -o evilcode ./cmd/evilcode" >&2
        exit 1
    }
    cmd_kill
    reset_fixtures
    mkdir -p "$FAKEHOME" "$FRAMES"

    # The scenario is an explicit argument rather than inherited environment.
    # Relying on the env let one golden run leak a scenario into the next,
    # which produced goldens containing another scenario's transcript.
    # The graphics protocol is forced the same way, because the pane's TERM says
    # xterm-256color and a scenario about images has nothing to show under a
    # terminal that has none. tmux swallows the payload either way; what the
    # frame proves is the rows the image block reserves and where the text after
    # it lands.
    local scenario="${PROBE_SCENARIO:-chat}"
    local gfx="${EVILCODE_GRAPHICS:-}"
    while true; do
        case "${1:-}" in
        --scenario=*) scenario="${1#--scenario=}" ;;
        --graphics=*) gfx="${1#--graphics=}" ;;
        *) break ;;
        esac
        shift
    done

    local app=("$BIN" probe hello)
    [[ $# -gt 0 ]] && app=("$BIN" "$@")

    tm new-session -d -s evil -x "$COLS" -y "$ROWS" \
        "env HOME='$FAKEHOME' \
             XDG_DATA_HOME='$FAKEHOME/.local/share' \
             XDG_CONFIG_HOME='$FAKEHOME/.config' \
             XDG_CACHE_HOME='$FAKEHOME/.cache' \
             XDG_STATE_HOME='$FAKEHOME/.local/state' \
             XDG_RUNTIME_DIR='$TMUX_TMPDIR' \
             TERM=xterm-256color COLORTERM=truecolor \
             EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock \
             EVILCODE_SKILL_DIRS= \
             EVILCODE_GRAPHICS='$gfx' \
             EVILCODE_SCENARIO='$scenario' ${app[*]}"
    settle
}

cmd_keys() {
    require_session
    local pane=0
    if [[ "${1:-}" == --pane=* ]]; then
        pane="${1#--pane=}"
        shift
    fi
    tm send-keys -t "evil.$pane" "$@"
    settle
}

# The daemon runs outside tmux: it has no terminal to own, and putting it in a
# pane would mean its startup line and any stderr landed in a golden.
SOCKET_PATH="$TMUX_TMPDIR/e.sock"
AUTO_SOCKET="$TMUX_TMPDIR/evilcode.sock"
SERVE_PID="$TMUX_TMPDIR/serve.pid"

probe_env() {
    printf "HOME='%s' XDG_DATA_HOME='%s/.local/share' XDG_CONFIG_HOME='%s/.config' " \
        "$FAKEHOME" "$FAKEHOME" "$FAKEHOME"
    printf "XDG_CACHE_HOME='%s/.cache' XDG_STATE_HOME='%s/.local/state' " \
        "$FAKEHOME" "$FAKEHOME"
    printf "XDG_RUNTIME_DIR='%s' TERM=xterm-256color COLORTERM=truecolor " "$TMUX_TMPDIR"
    printf "EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock EVILCODE_SKILL_DIRS= "
}

stop_auto() {
    env HOME="$FAKEHOME" \
        XDG_DATA_HOME="$FAKEHOME/.local/share" \
        XDG_CONFIG_HOME="$FAKEHOME/.config" \
        XDG_CACHE_HOME="$FAKEHOME/.cache" \
        XDG_STATE_HOME="$FAKEHOME/.local/state" \
        XDG_RUNTIME_DIR="$TMUX_TMPDIR" \
        "$BIN" serve -stop -socket "$AUTO_SOCKET" >/dev/null 2>&1 || true
}

cmd_serve() {
    local scenario="chat"
    if [[ "${1:-}" == --scenario=* ]]; then
        scenario="${1#--scenario=}"
        shift
    fi
    # A scenario starts from nothing: any pane left over from a previous run
    # would be split into rather than replaced, and its transcript would show
    # up in this run's golden.
    tm kill-session -t evil 2>/dev/null || true
    cmd_unserve
    stop_auto
    reset_fixtures
    mkdir -p "$FAKEHOME"

    env HOME="$FAKEHOME" \
        XDG_DATA_HOME="$FAKEHOME/.local/share" \
        XDG_CONFIG_HOME="$FAKEHOME/.config" \
        XDG_CACHE_HOME="$FAKEHOME/.cache" \
        XDG_STATE_HOME="$FAKEHOME/.local/state" \
        XDG_RUNTIME_DIR="$TMUX_TMPDIR" \
        EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock EVILCODE_SKILL_DIRS= \
        EVILCODE_SCENARIO="$scenario" \
        "$BIN" serve -socket "$SOCKET_PATH" -q >"$TMUX_TMPDIR/serve.log" 2>&1 &
    echo $! >"$SERVE_PID"

    # Wait for the socket rather than sleeping a guessed interval, so the rig
    # is neither slow nor flaky on a loaded machine.
    local i
    for ((i = 0; i < 100; i++)); do
        [[ -S "$SOCKET_PATH" ]] && return 0
        sleep 0.05
    done
    echo "probe: the daemon never bound $SOCKET_PATH" >&2
    cat "$TMUX_TMPDIR/serve.log" >&2
    exit 1
}

cmd_unserve() {
    [[ -f "$SERVE_PID" ]] && kill "$(cat "$SERVE_PID")" 2>/dev/null || true
    rm -f "$SERVE_PID" "$SOCKET_PATH"
}

# cmd_attach opens a client. The first call creates the window; each later one
# splits it, so a golden of two clients is one frame rather than two files.
cmd_attach() {
    local session="${1:-}"
    local client="${2:-client}"
    local cmd
    cmd="env $(probe_env) EVILCODE_CLIENT='$client' EVILCODE_SCENARIO='' \
         $BIN attach -socket '$SOCKET_PATH' $session"

    if tm has-session -t evil 2>/dev/null; then
        tm split-window -h -t evil "$cmd"
        tm select-layout -t evil even-horizontal
    else
        tm new-session -d -s evil -x "$COLS" -y "$ROWS" "$cmd"
    fi
    settle
}

cmd_frame() {
    require_session
    local name="${1:?usage: probe.sh frame <name>}"
    mkdir -p "$FRAMES"

    # Every pane, in order, separated by a rule. A two-client golden is one
    # frame: the whole point is what the two show at the same instant.
    local panes
    panes="$(tm list-panes -t evil -F '#{pane_index}')"
    : >"$FRAMES/$name.txt"
    : >"$FRAMES/$name.ansi"
    local first=1
    local p
    for p in $panes; do
        if [[ $first -eq 0 ]]; then
            printf -- '--- pane %s ---\n' "$p" >>"$FRAMES/$name.txt"
            printf -- '--- pane %s ---\n' "$p" >>"$FRAMES/$name.ansi"
        fi
        first=0
        tm capture-pane -p -t "evil.$p" >>"$FRAMES/$name.txt"
        tm capture-pane -e -p -N -t "evil.$p" >>"$FRAMES/$name.ansi"
    done
    echo "$FRAMES/$name.txt"
}

cmd_png() {
    local name="${1:?usage: probe.sh png <name> [size]}"
    local size="${2:-16}"
    cmd_frame "$name" >/dev/null
    "$BIN" probe render -size "$size" "$FRAMES/$name.ansi" "$FRAMES/$name.png"
    echo "$FRAMES/$name.png"
}

# cmd_wait blocks until a file contains a pattern.
#
# settle watches the pane, which is the wrong thing when what a scenario is
# waiting for is a background worker writing to disk: the pane is perfectly
# still while the edit is still in flight. Polling the actual condition is
# deterministic where a sleep is a guess.
cmd_wait() {
    local file="${1:?usage: probe.sh wait <file> <pattern>}"
    local pattern="${2:?usage: probe.sh wait <file> <pattern>}"
    local i
    for ((i = 0; i < 200; i++)); do
        if grep -q -- "$pattern" "$file" 2>/dev/null; then
            settle
            return 0
        fi
        sleep 0.05
    done
    echo "probe: $file never matched $pattern" >&2
    exit 1
}

# cmd_wait_pane blocks until the visible transcript contains a pattern. A pane
# can be stable while the remote event stream is still draining, so scenarios
# that care about a particular streamed sentence should wait for that sentence
# rather than relying on a short quiet window.
cmd_wait_pane() {
    local pattern="${1:?usage: probe.sh wait-pane <pattern>}"
    local i frame
    for ((i = 0; i < 200; i++)); do
        frame="$(tm capture-pane -p -t evil 2>/dev/null || true)"
        if grep -Fq -- "$pattern" <<<"$frame"; then
            settle
            return 0
        fi
        sleep 0.05
    done
    echo "probe: pane never matched $pattern" >&2
    exit 1
}

cmd_sleep() {
    sleep "${1:?usage: probe.sh sleep <seconds>}"
    settle
}

cmd_kill() {
    tm kill-session -t evil 2>/dev/null || true
    cmd_unserve
    stop_auto
}

case "${1:-}" in
    boot)   shift; cmd_boot "$@" ;;
    serve)  shift; cmd_serve "$@" ;;
    attach) shift; cmd_attach "$@" ;;
    keys)  shift; cmd_keys "$@" ;;
    frame) shift; cmd_frame "$@" ;;
    png)   shift; cmd_png "$@" ;;
    wait)   shift; cmd_wait "$@" ;;
    wait-pane) shift; cmd_wait_pane "$@" ;;
    sleep)  shift; cmd_sleep "$@" ;;
    kill)  shift; cmd_kill ;;
    *)
        sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 1
        ;;
esac
