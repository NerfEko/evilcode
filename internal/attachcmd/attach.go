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
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/buildinfo"
	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/daemon"
	"evilcode/internal/graphics"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
	"evilcode/internal/tui"
)

// Run attaches to a daemon session, or lists what the daemon is holding.
func Run(args []string) error {
	return run(args, false)
}

// RunDefault is the normal `evilcode`/`ec` entrypoint. It starts the detached
// per-user server when needed before attaching the TUI.
func RunDefault(args []string) error {
	return run(args, true)
}

func run(args []string, autoStart bool) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	socket := fs.String("socket", "", "socket path (default $XDG_RUNTIME_DIR/evilcode.sock)")
	list := fs.Bool("l", false, "list the daemon's sessions and exit")
	model := fs.String("m", "", "model reference for a new session")
	resume := fs.String("resume", "", "resume a named session")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *socket
	if path == "" {
		path = daemon.SocketPath()
	}
	var client *daemon.Client
	var err error
	if autoStart {
		client, err = daemon.EnsureRunningPath(context.Background(), path)
	} else {
		client, err = daemon.DialPath(path)
	}
	if err != nil {
		return err
	}
	defer client.Close()

	if *list {
		return printSessions(client)
	}

	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *resume != "" {
		name = strings.TrimSpace(*resume)
	}
	// There is no resume window on launch: the start page inside the TUI is
	// the one place you pick a session. Attaching with no name opens a fresh
	// session, and its empty-transcript start page offers the recent ones to
	// resume from there.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	snap, err := client.AttachAt(name, 0, cwd, *model)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pc := agent.LoadProjectContext(snap.Cwd, config.ConfigDir())
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return err
	}
	// The attached session is served by the daemon, but the client still
	// renders provider readiness. Keep a locally logged-in Codex account visible
	// here as it is in the standalone TUI and headless paths.
	cfg.AddDiscoveredCodex()

	// Record the model this session resolved to, so the next launch remembers
	// it. The daemon decides the model for a fresh session (wiring.Build reads
	// last_model and falls back to the configured default); the client sees the
	// result in the snapshot and persists it here, mirroring the standalone
	// TUI path. Without this the daemon path never wrote last_model, so it sat
	// at whatever value it booted with forever.
	if last := config.ModelRef(snap.Model, snap.Provider); last != cfg.LastModel {
		if saveErr := config.SaveLastModel(last); saveErr != nil {
			fmt.Fprintln(os.Stderr, "evilcode: could not remember model:", saveErr)
		} else {
			cfg.LastModel = last
		}
	}

	// A local agent with no provider: the TUI drives it exactly as it drives a
	// real one, but Forward sends turns down the socket and the receive loop
	// pushes the daemon's events back into the same stream (invariant 1).
	conv := agent.NewConversation("")
	conv.Sync(snapshotMessages(snap), snap.Epoch)
	a := agent.New(snap.Session, nil, snap.Model, nil, conv)
	// The session identity is mutable: /rename republishes a snapshot with the
	// new name, and every closure below must follow it rather than capture the
	// boot-time value (D3).
	self := &sessionIdentity{name: snap.Session}
	a.Session = self.get()
	// The daemon resolved the model's real context window at session start
	// (config.ContextWindowFor). Mirror it here so the context meter renders
	// the actual window rather than the 200k fallback behind contextMax.
	a.NumCtx = snap.ContextWindow
	var inputSeq atomic.Uint64
	inputID := func() string { return fmt.Sprintf("attach-%d", inputSeq.Add(1)) }
	a.ForwardHidden = func(_ context.Context, text string, images [][]byte, hidden bool) error {
		return client.Send(daemon.ClientMsg{
			Kind: daemon.MsgInput, Session: self.get(), Text: text,
			RequestID: inputID(), Images: images, Hidden: hidden,
		})
	}
	a.OnInterject = func(in agent.Interrupt) bool {
		// Queuing locally would strand the message: nothing on this side ever
		// drains it, because the loop that would is in the daemon.
		err := client.Send(daemon.ClientMsg{
			Kind: daemon.MsgInterrupt, Session: self.get(),
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
	m := tui.NewModel(a, header(cfg, snap, path)).WithVision(snap.Vision)
	dataDir := config.DataDir()
	skills := tools.LoadSkills(tools.SkillDirs(pc.Root, config.ConfigDir()))
	var todos *todo.Store
	if sessionTodos, todoErr := todo.NewStore(dataDir, snap.Session); todoErr == nil {
		todos = sessionTodos
	}
	prompts, _ := session.OpenHistory(dataDir)
	keymap, keymapProblems := tui.NewKeymap(cfg.Keybindings)
	for _, problem := range keymapProblems {
		fmt.Fprintln(os.Stderr, "evilcode: "+problem)
	}
	fsTools := tools.NewFS(snap.Cwd).
		WithConfine(cfg.Features.ConfineToWorkspace).
		WithVision(cfg.ModelOverrides(config.ModelRef(snap.Model, snap.Provider)).Vision)
	braveSearch := tools.NewBraveSearch(cfg.BraveSearchAPIKey())
	m.WithSkills(skills, pc).
		WithTodos(todos, nil).
		WithHistory(prompts).
		WithKeymap(keymap, tui.LoadHotkeyUsage(dataDir), cfg.Display.KeybindingHints).
		WithDisplay(cfg.Display).
		WithProviders(cfg.Providers).
		WithSessions(dataDir, snap.Cwd, nil).
		WithRemoteSessions(func() ([]tui.SessionDescriptor, error) {
			roster, err := daemon.DialPath(path)
			if err != nil {
				return nil, err
			}
			defer roster.Close()
			rows, err := roster.List()
			if err != nil {
				return nil, err
			}
			return sessionDescriptors(rows), nil
		}).
		WithBraveSearch(braveSearch).
		WithVisionFor(func(ref string) bool { return cfg.ModelOverrides(ref).Vision }, fsTools).
		WithPersistentModelState(cfg.LastModel, cfg.ReasoningEfforts,
			config.SaveLastModel, config.SaveReasoningEffort)
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
				Kind: daemon.MsgReasoningEffort, Session: self.get(),
				ReasoningEffort: string(effort),
			})
		})
	}
	m.WithSwarm(swarm, func(task string) (string, error) {
		return summon(path, self.get(), task)
	}).
		WithRemoteModelEffort(func(ref string, effort provider.ReasoningEffort) error {
			return client.Send(daemon.ClientMsg{
				Kind: daemon.MsgModel, Session: self.get(), Model: ref,
				ReasoningEffort: string(effort),
			})
		}).
		WithRemoteInterrupt(func(_ bool) error {
			return client.Send(daemon.ClientMsg{
				Kind: daemon.MsgInterrupt, Session: self.get(),
			})
		}).
		WithRemoteCommand(func(kind, arg, secret string) error {
			return client.Send(daemon.ClientMsg{
				Kind: daemon.MsgCommand, Session: self.get(),
				Arg: arg, Secret: secret, Text: kind,
			})
		}).
		WithRemoteAskAnswer(func(id string, labels []string) error {
			return client.Send(daemon.ClientMsg{
				Kind: daemon.MsgAnswer, Session: self.get(),
				RequestID: id, Answers: labels,
			})
		}).
		WithModelPrefs(cfg.DefaultModel, cfg.FavoriteModels, config.SaveModelPrefs).
		WithGraphics(graphics.Detect(), filepath.Join(config.DataDir(), "diagrams"))
	m.SetRemoteBackground(snapshotBackground(snap))
	for _, req := range snap.Pending {
		m.SetRemoteAsk(req)
	}
	m.RebuildFrom(conv.Messages())
	pollerCtx, stopPoller := context.WithCancel(context.Background())
	defer stopPoller()
	go pollRoster(pollerCtx, path, self.get, swarm)

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
					if msg.Event.Kind == agent.EventTurnEnd && msg.Event.SnapshotMessages != nil {
						a.Conv.Sync(msg.Event.SnapshotMessages, msg.Event.SnapshotEpoch)
					}
					a.Inject(*msg.Event)
					switch msg.Event.Kind {
					case agent.EventTurnStart:
						a.SetRunning(true)
					case agent.EventTurnEnd:
						a.SetRunning(false)
					}
				}
			case daemon.MsgSnapshot:
				if msg.Snapshot != nil {
					// Follow a /rename: the new identity is authoritative for every
					// closure and the roster self-filter from here on (D3).
					self.set(msg.Snapshot.Session)
					a.Conv.Sync(snapshotMessages(msg.Snapshot), msg.Snapshot.Epoch)
					a.Inject(agent.Event{
						Kind:               agent.EventSnapshot,
						Session:            msg.Snapshot.Session,
						SnapshotSession:    msg.Snapshot.Session,
						SnapshotModel:      msg.Snapshot.Model,
						SnapshotProvider:   msg.Snapshot.Provider,
						SnapshotRunning:    msg.Snapshot.Running,
						SnapshotMessages:   snapshotMessages(msg.Snapshot),
						SnapshotPending:    append([]agent.AskEvent(nil), msg.Snapshot.Pending...),
						SnapshotBackground: snapshotBackground(msg.Snapshot),
					})
				}
			case daemon.MsgError:
				a.Notice(agent.LevelError, "daemon: %s", msg.Err)
			}
		}
	}()

	err = tui.RunModel(m)
	if err != nil {
		return err
	}
	if target := m.ReloadTarget(); target != "" {
		_ = client.Close()
		return run([]string{"-socket", path, "-resume", target}, autoStart)
	}
	// `/resume` deliberately exits the current Bubble Tea program so all
	// session-bound UI state is rebuilt against the selected daemon session.
	// Re-enter through the same attach path, which closes this connection first
	// and never creates a second live agent for the session.
	if target := m.ResumeTarget(); target != "" {
		_ = client.Close()
		return run([]string{"-socket", path, "-resume", target}, autoStart)
	}
	return nil
}

func snapshotBackground(snap *daemon.Snapshot) []agent.BackgroundState {
	if snap == nil {
		return nil
	}
	states := make([]agent.BackgroundState, 0, len(snap.Background))
	for _, task := range snap.Background {
		states = append(states, agent.BackgroundState{
			ID: task.ID, Label: task.Label, Done: task.Done,
			Failed: task.Failed, Progress: task.Progress,
		})
	}
	return states
}

func snapshotMessages(snap *daemon.Snapshot) []provider.Message {
	msgs := make([]provider.Message, 0, len(snap.Messages))
	for _, msg := range snap.Messages {
		msgs = append(msgs, provider.Message{
			Role:          provider.Role(msg.Role),
			Content:       msg.Content,
			Reasoning:     msg.Reasoning,
			ToolCalls:     msg.ToolCalls,
			ProviderItems: msg.ProviderItems,
			ToolCallID:    msg.ToolCallID,
			ToolName:      msg.ToolName,
			IsError:       msg.IsError,
			Held:          msg.Held,
			Images:        msg.Images,
			Hidden:        msg.Hidden,
			Repairs:       msg.Repairs,
		})
	}
	return msgs
}

func sessionDescriptors(rows []daemon.SessionInfo) []tui.SessionDescriptor {
	out := make([]tui.SessionDescriptor, 0, len(rows))
	for _, row := range rows {
		modified := row.Modified
		if modified.IsZero() {
			modified = row.Started
		}
		out = append(out, tui.SessionDescriptor{
			Name: row.Name, Model: row.Model, Cwd: row.Cwd, Title: row.Title,
			Modified: modified, Crashed: row.Crashed, Live: row.Live,
			Running: row.Running, Clients: row.Clients, Task: row.Task,
			Pending: row.Pending, Messages: row.Messages,
		})
	}
	return out
}

// SummonTimeout bounds a /summon round trip. A daemon that accepts the
// connection and then stalls must not hang the caller forever — see H5.23.
const SummonTimeout = 30 * time.Second

// summon opens its own connection to spawn a worker.
//
// A second connection rather than the attached one: the attached connection is
// mid-stream with events, and interleaving a request/response exchange into it
// would mean the reply could arrive behind a hundred deltas.
func summon(path, sessionName, task string) (string, error) {
	c, err := daemon.DialPath(path)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := c.SetDeadline(SummonTimeout); err != nil {
		return "", err
	}

	if err := c.Send(daemon.ClientMsg{Kind: daemon.MsgSpawn, Session: sessionName, Task: task}); err != nil {
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

// sessionIdentity is the mutable session name of an attached window. The
// daemon republishes a snapshot after /rename; closures and pollers must
// follow the new identity instead of holding the boot-time name forever,
// or the window sends to a dead session and lists itself as another agent
// (D3, D7).
type sessionIdentity struct {
	mu   sync.Mutex
	name string
}

func (s *sessionIdentity) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func (s *sessionIdentity) set(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

// RosterInterval is how often the swarm roster is refreshed. Seconds, not
// milliseconds: the roster changes when an agent starts or stops, and polling
// faster would spend a round trip per frame to redraw the same list.
const RosterInterval = 2 * time.Second

// pollRoster keeps the swarm widget current until ctx is cancelled. The
// identity is read dynamically on every cycle, so a renamed session stops
// filtering itself out under its old name; the context lets the caller join
// the poller instead of accumulating one dial loop per resume (D7).
func pollRoster(ctx context.Context, path string, self func() string, swarm *tui.SwarmState) {
	ticker := time.NewTicker(RosterInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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

		me := self()
		agents := make([]tui.SwarmAgent, 0, len(sessions))
		for _, s := range sessions {
			if s.Name == me {
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
		Version:         buildinfo.Version,
		Model:           snap.Model,
		ReasoningEffort: provider.ReasoningEffort(snap.ReasoningEffort),
		Provider:        snap.Provider,
		AuthKind:        "socket",
		Cwd:             snap.Cwd,
		Attached:        path,
		Skills:          append([]string(nil), snap.Skills...),
		// Seeded by pid, so two terminals on one session get different names
		// and each keeps its own across a repaint.
		ClientName: core.PickName(core.Creatures, core.SeedFrom(clientSeed()), nil),
	}
	if h.Provider == "" {
		h.Provider = "daemon"
	}
	for _, s := range snap.MCP {
		h.MCP = append(h.MCP, tui.MCPStatus{Name: s.Name, Tools: s.Tools})
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
