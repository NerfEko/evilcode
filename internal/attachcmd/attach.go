// Package attachcmd implements `evilcode attach`: the ordinary TUI driven by a
// session living in the daemon (plan.md §20).
package attachcmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/daemon"
	"evilcode/internal/graphics"
	"evilcode/internal/provider"
	"evilcode/internal/tui"
	"evilcode/internal/tuicmd"
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
	// The attached session is served by the daemon, but the client still
	// renders provider readiness. Keep a locally logged-in Codex account visible
	// here as it is in the standalone TUI and headless paths.
	cfg.AddDiscoveredCodex()

	// A local agent with no provider: the TUI drives it exactly as it drives a
	// real one, but Forward sends turns down the socket and the receive loop
	// pushes the daemon's events back into the same stream (invariant 1).
	conv := agent.NewConversation("")
	for _, m := range snap.Messages {
		conv.Append(provider.Message{Role: provider.Role(m.Role), Content: m.Content, Repairs: m.Repairs})
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
		if err != nil {
			a.Notice(agent.LevelError, "could not send interrupt to daemon: %v", err)
		}
		// Even on failure, do not queue this in the proxy agent: no local loop
		// exists to drain it, so it would remain pending forever.
		return true
	}
	a.SetRunning(snap.Running)
	defer a.Close()

	// The swarm is whatever else the daemon is holding. `list` is polled rather
	// than pushed because the roster changes on the scale of seconds, and a
	// push channel would mean a second stream to keep ordered against the
	// event one.
	swarm := &tui.SwarmState{}
	m := tui.NewModel(a, header(cfg, snap, path))
	if len(snap.ReasoningEfforts) > 0 {
		levels := make([]provider.ReasoningEffort, 0, len(snap.ReasoningEfforts))
		for _, level := range snap.ReasoningEfforts {
			if parsed, ok := provider.ParseReasoningEffort(level); ok {
				levels = append(levels, parsed)
			}
		}
		m.WithReasoningEfforts(levels)
	}
	if snap.ReasoningEffort != "" || len(snap.ReasoningEfforts) > 0 {
		m.WithReasoningEffort(provider.ReasoningEffort(snap.ReasoningEffort), func(effort provider.ReasoningEffort) error {
			return client.Send(daemon.ClientMsg{
				Kind: daemon.MsgReasoningEffort, Session: snap.Session,
				ReasoningEffort: string(effort),
			})
		})
	}
	m.WithSwarm(swarm, func(task string) (string, error) {
		return summon(path, snap.Session, task)
	}).
		WithModelPrefs(cfg.DefaultModel, cfg.FavoriteModels, config.SaveModelPrefs).
		WithPersistentModelState(cfg.LastModel, cfg.ReasoningEfforts, nil, nil).
		WithGraphics(graphics.Detect(), filepath.Join(config.DataDir(), "diagrams"))
	m.RebuildFrom(conv.Messages())
	go pollRoster(path, snap.Session, swarm)

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

// SummonTimeout bounds a /summon round trip. A daemon that accepts the
// connection and then stalls must not hang the caller forever — see H5.23.
const SummonTimeout = 30 * time.Second

// summon opens its own connection to spawn a worker.
//
// A second connection rather than the attached one: the attached connection is
// mid-stream with events, and interleaving a request/response exchange into it
// would mean the reply could arrive behind a hundred deltas.
func summon(path, spawner, task string) (string, error) {
	c, err := daemon.DialPath(path)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := c.SetDeadline(SummonTimeout); err != nil {
		return "", err
	}

	if err := c.Send(daemon.ClientMsg{Kind: daemon.MsgSpawn, Session: spawner, Task: task}); err != nil {
		return "", err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return "", err
		}
		switch msg.Kind {
		case daemon.MsgSessions:
			if len(msg.Sessions) == 0 {
				return "", fmt.Errorf("the daemon started no worker")
			}
			return msg.Sessions[0].Name, nil
		case daemon.MsgError:
			return "", fmt.Errorf("%s", msg.Err)
		}
	}
}

// RosterInterval is how often the swarm roster is refreshed. Seconds, not
// milliseconds: the roster changes when an agent starts or stops, and polling
// faster would spend a round trip per frame to redraw the same list.
const RosterInterval = 2 * time.Second

// pollRoster keeps the swarm widget current.
func pollRoster(path, self string, swarm *tui.SwarmState) {
	for {
		time.Sleep(RosterInterval)
		c, err := daemon.DialPath(path)
		if err != nil {
			return
		}
		if err := c.SetDeadline(5 * time.Second); err != nil {
			c.Close()
			return
		}
		sessions, err := c.List()
		c.Close()
		if err != nil {
			return
		}

		agents := make([]tui.SwarmAgent, 0, len(sessions))
		for _, s := range sessions {
			if s.Name == self {
				continue
			}
			agents = append(agents, tui.SwarmAgent{
				Name: s.Name, Task: s.Task, Worker: s.Worker,
				Running: s.Running, Since: time.Since(s.Started),
			})
		}
		swarm.Publish(agents)
	}
}

// header names both ends. Seeing which session is on the server and which
// client is looking at it is the whole point of a header in attached mode
// (plan.md §20).
func header(cfg *config.Config, snap *daemon.Snapshot, path string) tui.HeaderState {
	h := tui.HeaderState{
		SessionName:     snap.Session,
		Version:         tuicmd.Version,
		Model:           snap.Model,
		ReasoningEffort: provider.ReasoningEffort(snap.ReasoningEffort),
		Provider:        snap.Provider,
		AuthKind:        "socket",
		Cwd:             snap.Cwd,
		Attached:        path,
		// Seeded by pid, so two terminals on one session get different names
		// and each keeps its own across a repaint.
		ClientName: core.PickName(core.Creatures, core.SeedFrom(clientSeed()), nil),
	}
	if h.Provider == "" {
		h.Provider = "daemon"
	}
	for _, level := range snap.ReasoningEfforts {
		if parsed, ok := provider.ParseReasoningEffort(level); ok {
			h.ReasoningEfforts = append(h.ReasoningEfforts, parsed)
		}
	}
	for _, p := range cfg.Providers {
		ready := p.APIKeyValue() != "" || p.APIKeyEnv == ""
		if p.Kind == config.KindCodex {
			_, buildErr := p.Build()
			ready = buildErr == nil
		}
		h.Providers = append(h.Providers, tui.ProviderStatus{
			Name:  p.Name,
			Ready: ready,
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
