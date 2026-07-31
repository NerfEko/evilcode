package runcmd

import (
	"errors"
	"fmt"
	"os"

	"evilcode/internal/agent"
	"evilcode/internal/daemon"
)

// runRemote submits a turn into a running daemon and streams the result back
// (plan.md §20, `run --remote`).
//
// It prints the same thing a local run does, because it is watching the same
// event stream — the socket only changes where the loop happens to live.
func runRemote(socket, session, prompt string, quiet bool) (int, error) {
	path := socket
	if path == "" {
		path = daemon.SocketPath()
	}
	client, err := daemon.DialPath(path)
	if err != nil {
		return ExitError, err
	}
	defer client.Close()

	snap, err := client.Attach(session, 0)
	if err != nil {
		return ExitError, err
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "submitted into %s on %s\n", snap.Session, path)
	}
	if err := client.Send(daemon.ClientMsg{
		Kind: daemon.MsgInput, Session: snap.Session, Text: prompt,
	}); err != nil {
		return ExitError, err
	}

	p := newPrinter(quiet)
	// The daemon may still be replaying the ring when the turn starts, so the
	// stream is followed until this turn's own TurnEnd rather than the first
	// one seen. Anything before the matching TurnStart belongs to history.
	started := false
	for {
		msg, err := client.Recv()
		if err != nil {
			if errors.Is(err, daemon.ErrClosed) {
				return ExitError, fmt.Errorf("the daemon closed the connection mid-turn")
			}
			return ExitError, err
		}
		switch msg.Kind {
		case daemon.MsgError:
			return ExitError, fmt.Errorf("%s", msg.Err)
		case daemon.MsgEvent:
			if msg.Event == nil {
				continue
			}
			if msg.Event.Kind == agent.EventTurnStart {
				started = true
				continue
			}
			if !started {
				continue
			}
			p.print(*msg.Event)
			if msg.Event.Kind == agent.EventTurnEnd {
				p.finish()
				return p.exit, nil
			}
		}
	}
}
