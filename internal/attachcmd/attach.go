// Package attachcmd implements `evilcode attach`: the ordinary TUI driven by a
// session living in the daemon (plan.md §20).
package attachcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/daemon"
	"evilcode/internal/provider"
	"evilcode/internal/tui"
)

// Run attaches to a daemon session, or lists what the daemon is holding.
func Run(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	socket := fs.String("socket", "", "socket path (default $XDG_RUNTIME_DIR/evilcode.sock)")
	list := fs.Bool("l", false, "list the daemon's sessions and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *socket
	if path == "" {
		path = daemon.SocketPath()
	}
	client, err := daemon.DialPath(path)
	if err != nil {
		return err
	}
	defer client.Close()

	if *list {
		return printSessions(client)
	}

	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	snap, err := client.Attach(name, 0)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// A local agent with no provider: the TUI drives it exactly as it drives a
	// real one, but Forward sends turns down the socket and the receive loop
	// pushes the daemon's events back into the same stream (invariant 1).
	conv := agent.NewConversation("")
	for _, m := range snap.Messages {
		conv.Append(provider.Message{Role: provider.Role(m.Role), Content: m.Content})
	}
	a := agent.New(snap.Session, nil, snap.Model, nil, conv)
	a.Forward = func(_ context.Context, text string) error {
		return client.Send(daemon.ClientMsg{Kind: daemon.MsgInput, Session: snap.Session, Text: text})
	}
	a.OnInterject = func(in agent.Interrupt) bool {
		// Queuing locally would strand the message: nothing on this side ever
		// drains it, because the loop that would is in the daemon.
		err := client.Send(daemon.ClientMsg{
			Kind: daemon.MsgInterrupt, Session: snap.Session,
			Text: in.Text, Urgent: in.Urgent,
		})
		return err == nil
	}
	a.SetRunning(snap.Running)
	defer a.Close()

	m := tui.NewModel(a, header(cfg, snap, path))
	m.RebuildFrom(conv.Messages())

	// The receive loop owns the event stream. It stops on EOF, which is what a
	// daemon shutdown looks like from here.
	go func() {
		for {
			msg, err := client.Recv()
			if err != nil {
				if !errors.Is(err, daemon.ErrClosed) {
					a.Notice(agent.LevelError, "daemon: %v", err)
				} else {
					a.Notice(agent.LevelWarning, "the daemon closed the connection")
				}
				return
			}
			switch msg.Kind {
			case daemon.MsgEvent:
				if msg.Event != nil {
					a.Inject(*msg.Event)
					switch msg.Event.Kind {
					case agent.EventTurnStart:
						a.SetRunning(true)
					case agent.EventTurnEnd:
						a.SetRunning(false)
					}
				}
			case daemon.MsgError:
				a.Notice(agent.LevelError, "daemon: %s", msg.Err)
			}
		}
	}()

	return tui.RunModel(m)
}

// header names both ends. Seeing which session is on the server and which
// client is looking at it is the whole point of a header in attached mode
// (plan.md §20).
func header(cfg *config.Config, snap *daemon.Snapshot, path string) tui.HeaderState {
	h := tui.HeaderState{
		SessionName: snap.Session,
		Version:     Version,
		Model:       snap.Model,
		Provider:    "daemon",
		AuthKind:    "socket",
		Cwd:         snap.Cwd,
		Attached:    path,
		// Seeded by pid, so two terminals on one session get different names
		// and each keeps its own across a repaint.
		ClientName: core.PickName(core.Creatures, core.SeedFrom(clientSeed()), nil),
	}
	for _, p := range cfg.Providers {
		h.Providers = append(h.Providers, tui.ProviderStatus{
			Name:  p.Name,
			Ready: p.APIKeyValue() != "" || p.APIKeyEnv == "",
		})
	}
	return h
}

// clientSeed names this client. It is the pid unless EVILCODE_DETERMINISTIC is
// set, which pins it so a probe with two attached clients produces the same two
// names every run (invariant 5).
func clientSeed() string {
	if os.Getenv("EVILCODE_DETERMINISTIC") == "1" {
		if n := os.Getenv("EVILCODE_CLIENT"); n != "" {
			return n
		}
		return "client"
	}
	return fmt.Sprint(os.Getpid())
}

// Version matches the TUI's, so a client and server built from different
// commits are visibly mismatched rather than subtly incompatible.
const Version = "v0.1.0"

func printSessions(client *daemon.Client) error {
	sessions, err := client.List()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("the daemon is holding no sessions")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tMODEL\tSTATE\tCLIENTS\tTASK")
	for _, s := range sessions {
		state := "idle"
		if s.Running {
			state = "running"
		}
		if s.Worker {
			state = "worker/" + state
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", s.Name, s.Model, state, s.Clients, s.Task)
	}
	return w.Flush()
}

// Usage prints the subcommand's flags.
func Usage() string {
	return "evilcode attach [-socket path] [-l] [session]"
}
