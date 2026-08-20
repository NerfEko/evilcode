package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/mcp"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
	"evilcode/internal/wiring"
)

// Server holds N sessions and serves clients over a unix socket.
type Server struct {
	Cfg  *config.Config
	Cwd  string
	Path string

	// IdleTimeout controls automatic shutdown after all clients disconnect and
	// all sessions become idle. Zero disables automatic shutdown.
	IdleTimeout time.Duration

	// Model is the default model reference for sessions this server creates.
	Model string

	// Files is the swarm's shared view of who touched what (plan.md §20).
	Files *Registry

	// CompactNotices folds a burst of conflicts into one line. A worker
	// rewriting twenty files otherwise buries the coordination it is meant to
	// provide under twenty near-identical warnings.
	CompactNotices bool

	// swarm is the coordination state: who spawned whom, result schemas, and
	// the per-session inbox (plan.md §20).
	swarm *swarmState

	mu       sync.Mutex
	sessions map[string]*Session
	listener net.Listener
	closed   bool

	workerWatchMu   sync.Mutex
	workerWatchStop chan struct{}
	workerWatchDone chan struct{}

	idleStop     chan struct{}
	idleDone     chan struct{}
	lastActivity time.Time

	// reserved holds names claimed by a session still being built, so a
	// concurrent spawn does not settle on the same one.
	reserved map[string]bool

	// opening holds one channel per session being built, closed when the build
	// finishes. Concurrent opens of one name wait on it instead of each
	// building their own — the losers used to be discarded, and discarding a
	// session closes its store, which appends a clean-exit marker to the log
	// the winner is still writing to.
	opening map[string]chan struct{}

	// builds keeps shared stores alive while a session is being assembled. A
	// close racing a slow provider resolve used to close the shared memory bank
	// underneath the build, then let the finished session publish into the
	// already-closed server.
	builds sync.WaitGroup

	// todos and bank are the swarm's shared state, owned here and handed to
	// every session by reference. Opened per session they were N copies of one
	// set of files, each writing the whole file back over the others.
	todos *todo.Store
	bank  *memory.Store
}

// shared returns the swarm's todo store and memory bank, opening them once.
//
// A failure to open is not fatal — both are coordination and enhancement, and
// a session builds without them the same way a solo one does.
func (s *Server) shared() (*todo.Store, *memory.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.todos == nil {
		s.todos, _ = todo.NewStore(config.DataDir(), SwarmTodoNamespace)
	}
	if s.bank == nil {
		s.bank, _ = memory.Open(config.DataDir())
	}
	return s.todos, s.bank
}

// Session is one live conversation inside the daemon.
type Session struct {
	Name    string
	Model   string
	Cwd     string
	Task    string
	Worker  bool
	Started time.Time

	built *wiring.Session
	ring  *Ring
	srv   *Server

	// turn counts completed turns, which is what a conflict notice cites when
	// it says how stale a reader's copy is.
	// turn counts this session's turns, 1-based: it is shown to a human in a
	// conflict notice, and "you read it at turn 0" reads as a bug rather than
	// as the first turn.
	turn int

	// retried marks a worker that has already been asked once to fix its output
	// against the schema. Asking forever is the loop §12.6 exists to prevent.
	retried bool

	// retrying is true while that second attempt is in flight. The spawn
	// goroutine used to call markFinished as soon as Run returned, which freed
	// the worker's slot under MaxLiveWorkers while the retry was still
	// spending tokens — and let the retry overlap the tail of the original run.
	retrying bool

	// done is closed when a worker's task ends. The spawn breaker counts what
	// is not yet closed, which is why it exists at all: a worker's turn starts
	// on a goroutine, so Running() is false for the first instants after Spawn.
	done       chan struct{}
	closedDone bool

	// lastHeartbeat advances when the worker's event stream advances. A worker
	// whose provider or pump has gone silent is still present in sessions, but
	// peers need to see that it is no longer trustworthy as a live worker.
	lastHeartbeat   time.Time
	stale           bool
	staleNotified   bool
	failureNotified bool

	mu sync.Mutex

	// pending holds conflicts waiting for this session's next safe point. It
	// lives on the reader, not the writer: a conflict queued on the writer
	// would wait for the writer's safe point and reach the wrong conversation.
	// Written by whichever session did the writing, so it is under mu.
	pending   []Conflict
	asks      *askBroker
	mcp       *mcp.Client
	poke      *agent.PokeHook
	advisor   *agent.Advisor
	overnight *overnightState

	subs map[chan ServerMsg]struct{}

	cancel context.CancelFunc

	// turnDone is closed when the in-flight turn's goroutine returns. Closing a
	// session cancels the turn and then waits on this: a cancelled turn still
	// writes — the partial answer, the stubs for the tools it abandoned — and
	// closing the store first sends all of it nowhere.
	turnDone  chan struct{}
	queued    []queuedInput
	requestID string

	// closing refuses new turns once close has begun. Without it a turn
	// starting while close waits replaces the channel close is waiting on, and
	// the store shuts under a turn that is still running.
	closing bool

	// running is the turn reservation. Agent.Running() is set by the turn
	// goroutine and so is false for the first instants after a start, which let
	// two clients both see an idle session and both launch against one
	// conversation. Taken here, before the goroutine exists.
	running bool
}

type queuedInput struct {
	requestID string
	text      string
	images    [][]byte
}

func (sess *Session) busy() bool {
	sess.mu.Lock()
	reserved := sess.running
	sess.mu.Unlock()
	return reserved || sess.built.Agent.Running()
}

// NewServer builds a server. It does not listen until Serve is called.
func NewServer(cfg *config.Config, cwd, model string) *Server {
	return &Server{
		Cfg:          cfg,
		Cwd:          cwd,
		Model:        model,
		Path:         SocketPath(),
		IdleTimeout:  DefaultIdleTimeout,
		lastActivity: time.Now(),
		Files:        newRegistryAt(cwd),
		swarm:        newSwarmState(),
		sessions:     map[string]*Session{},
	}
}

// DefaultIdleTimeout keeps the persistent server alive until it is explicitly
// stopped. Operators that want automatic cleanup can opt in with `serve -idle`.
const DefaultIdleTimeout time.Duration = 0

// WorkerHeartbeatInterval controls how often the daemon checks workers that
// have stopped producing events. The check is cheap; the longer threshold
// below is what prevents normal provider/tool gaps from looking like failure.
const WorkerHeartbeatInterval = 5 * time.Second

// WorkerStaleAfter is the silence window after which a live worker is shown as
// stale to its peers and its spawner receives one warning.
const WorkerStaleAfter = 2 * time.Minute

func (s *Server) startWorkerWatchdog() {
	s.workerWatchMu.Lock()
	if s.workerWatchStop != nil {
		s.workerWatchMu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.workerWatchStop = stop
	s.workerWatchDone = done
	s.workerWatchMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(WorkerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.refreshWorkerStaleness(time.Now())
			case <-stop:
				return
			}
		}
	}()
}

func (s *Server) stopWorkerWatchdog() {
	s.workerWatchMu.Lock()
	stop, done := s.workerWatchStop, s.workerWatchDone
	s.workerWatchStop, s.workerWatchDone = nil, nil
	s.workerWatchMu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *Server) startIdleWatchdog() {
	if s.IdleTimeout <= 0 {
		return
	}
	s.mu.Lock()
	if s.idleStop != nil || s.closed {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.idleStop, s.idleDone = stop, done
	s.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				s.mu.Lock()
				last := s.lastActivity
				timeout := s.IdleTimeout
				closed := s.closed
				clients, running := 0, 0
				for _, sess := range s.sessions {
					sess.mu.Lock()
					clients += len(sess.subs)
					reserved := sess.running
					sess.mu.Unlock()
					if reserved || (sess.built != nil && sess.built.Agent.Running()) {
						running++
					}
				}
				idle := !closed && clients == 0 && running == 0 && timeout > 0 &&
					now.Sub(last) >= timeout
				s.mu.Unlock()
				if idle {
					s.mu.Lock()
					// This goroutine is the watchdog; clear its handles before
					// Close so shutdown does not wait on itself.
					s.idleStop, s.idleDone = nil, nil
					s.mu.Unlock()
					s.Close()
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

func (s *Server) stopIdleWatchdog() {
	s.mu.Lock()
	stop, done := s.idleStop, s.idleDone
	s.idleStop, s.idleDone = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// refreshWorkerStaleness is also kept as an explicit method so Verify J8 can
// force the clock forward without waiting two minutes in a test.
func (s *Server) refreshWorkerStaleness(now time.Time) {
	s.mu.Lock()
	workers := make([]*Session, 0)
	for _, sess := range s.sessions {
		if sess.Worker {
			workers = append(workers, sess)
		}
	}
	s.mu.Unlock()

	for _, worker := range workers {
		worker.mu.Lock()
		if worker.closedDone {
			worker.stale = false
			worker.staleNotified = false
			worker.mu.Unlock()
			continue
		}

		last := worker.lastHeartbeat
		age := now.Sub(last)
		stale := last.IsZero() || age >= WorkerStaleAfter
		becameStale := stale && !worker.staleNotified
		worker.stale = stale
		if !stale {
			worker.staleNotified = false
		} else if becameStale {
			worker.staleNotified = true
		}
		name, task := worker.Name, worker.Task
		worker.mu.Unlock()

		if becameStale {
			s.notifyWorkerStale(name, task, age)
		}
	}
}

func (s *Server) notifyWorkerStale(worker, task string, age time.Duration) {
	s.swarm.mu.Lock()
	spawner := s.swarm.spawnedBy[worker]
	s.swarm.mu.Unlock()
	if spawner == "" {
		return
	}

	ago := "an unknown interval"
	if age >= 0 {
		ago = age.Round(time.Second).String()
	}
	text := fmt.Sprintf("⚠ worker %s is stale: no heartbeat for %s; it remains in peers as stale", worker, ago)
	if task != "" {
		text += fmt.Sprintf(" while working on %q", task)
	}
	s.deliver(spawner, text+". Inspect or restart it if it is stuck.")
}

// Listen binds the socket.
//
// A stale socket file from a crashed daemon is removed, but only after dialing
// it proves nothing is listening — unlinking a live daemon's socket would leave
// two servers fighting over one path, with clients silently reaching whichever
// bound last.
func (s *Server) Listen() error {
	if err := CheckSocketPath(s.Path); err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := CheckRuntimeDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Check again after creation. The fallback directory is predictable, so a
	// competing process can create or swap it between the first check and
	// MkdirAll; accepting that replacement would put the shell-bearing socket
	// in a directory somebody else controls.
	if err := CheckRuntimeDir(dir); err != nil {
		return err
	}

	// Serialized against other starting daemons. Binding first fixed the
	// original race but not its sibling: two daemons finding the *same stale*
	// socket both fail the dial, and the second removes the socket the first
	// has by then bound. The window between "this is stale" and "I have bound
	// it" cannot be closed by ordering alone, so it is held under a lock.
	unlock, err := lockSocketPath(s.Path)
	if err != nil {
		return err
	}
	defer unlock()

	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		if !errors.Is(err, syscall.EADDRINUSE) {
			return err
		}
		if conn, derr := net.Dial("unix", s.Path); derr == nil {
			conn.Close()
			return fmt.Errorf("a daemon is already listening on %s", s.Path)
		}
		// Nothing answered: the socket is a leftover from a daemon that died.
		if rerr := os.Remove(s.Path); rerr != nil {
			return fmt.Errorf("removing the stale socket %s: %w", s.Path, rerr)
		}
		if ln, err = net.Listen("unix", s.Path); err != nil {
			return err
		}
	}
	// The socket carries a live shell: anything that can connect can run
	// commands as this user. Owner-only is not defense in depth here, it is the
	// whole access control.
	if err := os.Chmod(s.Path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.listener = ln
	s.startWorkerWatchdog()
	return nil
}

// Serve accepts clients until the context is cancelled or Close is called.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	s.startIdleWatchdog()
	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := checkPeer(conn); err != nil {
			fmt.Fprintln(os.Stderr, "evilcode: refusing connection:", err)
			conn.Close()
			continue
		}
		go s.handle(ctx, conn)
	}
}

// Close stops listening and tears down every session.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ln := s.listener
	bank := s.bank
	s.bank = nil
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = map[string]*Session{}
	s.mu.Unlock()
	s.stopIdleWatchdog()
	s.stopWorkerWatchdog()

	if ln != nil {
		ln.Close()
	}
	// A build may have opened the shared bank before Close took the lock. Let it
	// finish (or observe the closed flag and clean itself up) before closing the
	// bank it borrows.
	s.builds.Wait()
	os.Remove(s.Path)
	for _, sess := range sessions {
		sess.close()
	}
	// The bank is the server's, so it outlives every session and is closed last:
	// a session's own Close must not take the swarm's memory down with it.
	if bank != nil {
		bank.Close()
	}
}

// Sessions lists what the server is holding.
func (s *Server) Sessions() []SessionInfo {
	s.mu.Lock()
	out := make([]SessionInfo, 0, len(s.sessions))
	seen := make(map[string]bool, len(s.sessions))
	for _, sess := range s.sessions {
		sess.mu.Lock()
		name, model, task, cwd := sess.Name, sess.Model, sess.Task, sess.Cwd
		worker, started := sess.Worker, sess.Started
		clients := len(sess.subs)
		running := sess.running
		stale := worker && sess.stale && !sess.closedDone
		sess.mu.Unlock()
		seen[name] = true
		out = append(out, SessionInfo{
			Name:    name,
			Model:   model,
			Running: running || sess.built.Agent.Running(),
			Clients: clients,
			Worker:  worker,
			Task:    task,
			Started: started,
			Stale:   stale,
			Cwd:     cwd,
			Stored:  true,
			Live:    true,
		})
	}
	s.mu.Unlock()

	// The daemon may restart independently of every TUI. Include durable
	// sessions that are not hydrated so a picker can offer them and a later
	// attach can reopen them in their recorded workspace.
	if stored, err := session.List(config.DataDir()); err == nil {
		for _, info := range stored {
			if seen[info.Name] {
				continue
			}
			out = append(out, SessionInfo{
				Name: info.Name, Model: info.Model, Cwd: info.Cwd,
				Started: info.Modified, Modified: info.Modified,
				Title: info.Title, Crashed: info.Crashed, Stored: true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Live != out[j].Live {
			return out[i].Live
		}
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		left, right := out[i].Modified, out[j].Modified
		if left.IsZero() {
			left = out[i].Started
		}
		if right.IsZero() {
			right = out[j].Started
		}
		return left.After(right)
	})
	return out
}

// sessionInfo returns one session's row, or nothing when the name is unknown.
func (s *Server) sessionInfo(name string) []SessionInfo {
	for _, info := range s.Sessions() {
		if info.Name == name {
			return []SessionInfo{info}
		}
	}
	return nil
}

// Status reports the live server without hydrating stored sessions.
func (s *Server) Status() *ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := &ServerStatus{
		PID: s.processID(), Socket: s.Path, IdleTimeout: s.IdleTimeout,
		LastActivity: s.lastActivity,
	}
	status.Sessions = len(s.sessions)
	for _, sess := range s.sessions {
		sess.mu.Lock()
		status.Clients += len(sess.subs)
		reserved := sess.running
		sess.mu.Unlock()
		if reserved || (sess.built != nil && sess.built.Agent.Running()) {
			status.Running++
		}
	}
	return status
}

func (s *Server) processID() int { return os.Getpid() }

// Open returns a session, building it if the name is new.
//
// An empty name creates a fresh session, which is what a client attaching
// without arguments wants.
func (s *Server) Open(name string) (*Session, error) {
	return s.OpenWithOptions(name, OpenOptions{Cwd: s.Cwd, Model: s.Model})
}

// OpenAt returns or builds a session in cwd. Existing sessions recover their
// original workspace from durable metadata when the caller omits it.
func (s *Server) OpenAt(name, cwd, model string) (*Session, error) {
	return s.OpenWithOptions(name, OpenOptions{Cwd: cwd, Model: model})
}

// OpenOptions describes how a new session should be built.
type OpenOptions struct {
	Cwd     string
	Model   string
	NoTools bool
}

// OpenWithOptions returns or builds a session with explicit creation options.
func (s *Server) OpenWithOptions(name string, opts OpenOptions) (*Session, error) {
	s.touch()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, fmt.Errorf("the daemon is shutting down")
		}
		if name == "" {
			// A fresh session has no name to collide on.
			s.builds.Add(1)
			s.mu.Unlock()
			defer s.builds.Done()
			break
		}
		if sess, ok := s.sessions[name]; ok {
			s.mu.Unlock()
			return sess, nil
		}
		if wait, building := s.opening[name]; building {
			s.mu.Unlock()
			<-wait
			// Round again: the builder has either published it or failed, and
			// this caller should see whichever happened.
			continue
		}
		if s.opening == nil {
			s.opening = map[string]chan struct{}{}
		}
		built := make(chan struct{})
		s.opening[name] = built
		s.builds.Add(1)
		s.mu.Unlock()
		defer s.builds.Done()
		defer func() {
			s.mu.Lock()
			delete(s.opening, name)
			s.mu.Unlock()
			close(built)
		}()
		break
	}

	cwd := opts.Cwd
	if name != "" {
		if info, err := session.Describe(config.DataDir(), name); err == nil {
			// A durable session owns its workspace. The directory of the
			// window that happens to resume it must not silently retarget file
			// tools after a restart.
			if info.Cwd != "" {
				cwd = info.Cwd
			}
		}
	}
	if cwd == "" {
		cwd = s.Cwd
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Built outside the lock: resolving a model can touch the network, and
	// holding the server lock across that would stall `list` for every client.
	s.mu.Lock()
	var cfg *config.Config
	if s.Cfg != nil {
		cfg = s.Cfg.Clone()
	}
	s.mu.Unlock()
	if cfg == nil {
		return nil, fmt.Errorf("daemon: no configuration")
	}
	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return nil, err
	}
	cfg.AddDiscoveredCodex()
	todos, bank := s.shared()
	asks := newAskBroker()
	var mcpClient *mcp.Client
	var extraTools tools.Set
	var extraClosers []func()
	if !opts.NoTools && len(cfg.MCP) > 0 {
		mcpClient = mcp.New()
		var servers []mcp.ServerConfig
		for _, srv := range cfg.MCP {
			servers = append(servers, mcp.ServerConfig{
				Name: srv.Name, Command: srv.Command, Args: srv.Args, Env: srv.Env,
			})
		}
		for _, mcpErr := range mcpClient.Connect(context.Background(), servers) {
			fmt.Fprintln(os.Stderr, "evilcode:", mcpErr)
		}
		extraTools, extraClosers = mcpClient.Tools(), []func(){mcpClient.Close}
	}
	built, err := wiring.Build(cfg, wiring.Options{
		Model: opts.Model, Resume: name, Cwd: cwd, Extract: true, NoTools: opts.NoTools,
		TodoNamespace: SwarmTodoNamespace, Todos: todos, Bank: bank, Asker: asks,
		ExtraTools: extraTools, ExtraClosers: extraClosers,
	})
	if err != nil {
		return nil, err
	}
	// These hooks used to exist only in the local TUI wiring. They belong to
	// the agent runtime, so keeping them here preserves auto-poke and advisor
	// behavior while every client is disconnected.
	var hooks agent.Chain
	if built.Agent.Hooks != nil {
		if existing, ok := built.Agent.Hooks.(agent.Chain); ok {
			hooks = append(hooks, existing...)
		} else {
			hooks = append(hooks, built.Agent.Hooks)
		}
	}
	var poke *agent.PokeHook
	if built.Todos != nil {
		poke = agent.NewPokeHook(built.Todos, built.Config.Features.AutoPoke)
		hooks = append(hooks, poke)
	}
	advisor := agent.NewAdvisor(func(ctx context.Context, system, user string) (string, error) {
		return built.Config.Router().SideCall(ctx, config.RoleSmol, system, user)
	}, built.Config.Features.Advisor)
	if built.Todos != nil {
		advisor.TodoState = built.Todos.Summary
	}
	hooks = append(hooks, advisor)
	if len(hooks) > 0 {
		built.Agent.Hooks = hooks
	}

	sess := &Session{
		Name:      built.Store.Name,
		Model:     built.Model,
		Cwd:       cwd,
		Started:   time.Now(),
		built:     built,
		ring:      NewRing(),
		srv:       s,
		asks:      asks,
		mcp:       mcpClient,
		poke:      poke,
		advisor:   advisor,
		overnight: newOvernightState(),
		subs:      map[chan ServerMsg]struct{}{},
	}
	if built.Exec != nil && built.Exec.Bg != nil {
		built.Exec.Bg.OnDone = func(task *tools.BackgroundTask) {
			done, failed, _ := task.Snapshot()
			if !done {
				return
			}
			sess.publishEvent(agent.Event{
				Kind: agent.EventBackground,
				Background: &agent.BackgroundState{
					ID: task.ID, Label: task.Label, Done: done,
					Failed: failed, Progress: task.Progress().String(),
				},
			})
			if failed {
				sess.notice(fmt.Sprintf("▣ Background task %d failed: %s", task.ID, task.Label))
			}
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		built.Close()
		return nil, fmt.Errorf("the daemon is shutting down")
	}
	// Another client may have opened the same session while this one was being
	// built. The first one in wins and the loser is discarded, so two clients
	// naming one session never end up talking to two agents over one file.
	if existing, ok := s.sessions[sess.Name]; ok {
		s.mu.Unlock()
		built.Close()
		return existing, nil
	}
	s.sessions[sess.Name] = sess
	s.mu.Unlock()
	asks.SetPublisher(sess.publishEvent)

	// Coordination tools exist only inside the daemon, where there is something
	// to coordinate with.
	sess.built.Agent.Tools = append(sess.built.Agent.Tools, s.AgentTools(sess.Name)...)

	go sess.pump()
	return sess, nil
}

// pump moves the agent's events into the ring and out to every subscriber. It
// is the only reader of the agent's channel, which is what lets N clients
// watch one session.
func (sess *Session) pump() {
	events := sess.built.Agent.Events()
	for {
		var e agent.Event
		select {
		case e = <-events:
		case <-sess.built.Agent.Done():
			return
		}
		sess.observe(e)
		sess.publishEvent(e)
	}
}

// publishEvent is shared by the agent pump and server-owned interaction
// brokers. Every event follows the same ring/broadcast path, so a client that
// reconnects cannot miss an ask request that arrived between snapshot and
// subscription.
func (sess *Session) publishEvent(e agent.Event) {
	sess.mu.Lock()
	if e.Session == "" {
		e.Session = sess.Name
	}
	if e.Kind == agent.EventTurnStart {
		e.RequestID = sess.requestID
		if isOvernightRequest(e.RequestID) {
			// The unattended prompt remains in the durable conversation, but it
			// is an implementation detail rather than a fake user message for
			// every attached window.
			e.Text = ""
		}
		sess.requestID = ""
	}
	sess.mu.Unlock()
	e.Seq = sess.ring.Add(e)
	sess.broadcast(ServerMsg{Kind: MsgEvent, Event: &e})
}

// publishSnapshot broadcasts durable state after a command that rewrites or
// renames the conversation. Attached clients use it to replace their local
// render/conversation mirror; the server remains the sole owner of the live
// runtime.
func (sess *Session) publishSnapshot() {
	sess.broadcast(ServerMsg{Kind: MsgSnapshot, Snapshot: sess.snapshot()})
}

// renameSession changes the live map key and the durable store together. The
// server lock is acquired before the session lock, matching Sessions and
// Status, so a picker cannot observe a half-renamed live entry.
func (s *Server) renameSession(sess *Session, name string) error {
	name = strings.TrimSpace(name)
	if err := session.ValidName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[name]; ok && existing != sess {
		return fmt.Errorf("session %q already exists", name)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closing {
		return fmt.Errorf("session is closing")
	}
	if sess.running || sess.built.Agent.Running() {
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	old := sess.Name
	if old == name {
		return nil
	}
	if err := sess.built.Store.Rename(config.DataDir(), name); err != nil {
		return err
	}
	delete(s.sessions, old)
	s.sessions[name] = sess
	sess.Name = name
	sess.built.Agent.Session = name
	return nil
}

// observe feeds the shared file registry from the event stream.
//
// Reading the stream rather than instrumenting the tools is what keeps this out
// of `internal/tools`, which knows nothing about swarms and should not start
// to: the events already say which file was touched and whether it changed.
// In production only pump calls this, so the turn counter would be safe
// unguarded — but it is cheap to guard and a field that is only accidentally
// single-threaded is a field that stops being safe the first time something
// else touches it.
func (sess *Session) observe(e agent.Event) {
	if sess.srv != nil {
		sess.srv.touch()
	}
	if sess.Worker {
		sess.heartbeat(time.Now())
	}
	if sess.srv == nil {
		return
	}
	sess.mu.Lock()
	if sess.turn == 0 {
		sess.turn = 1
	}
	turn, name, cwd := sess.turn, sess.Name, sess.Cwd
	sess.mu.Unlock()
	switch e.Kind {
	case agent.EventTokenUsage:
		if e.Usage != nil && sess.overnight != nil {
			sess.overnight.addTokens(e.Usage.In + e.Usage.Out)
		}
	case agent.EventAsk:
		if sess.overnight != nil && sess.overnight.isActive() {
			if sess.overnight.stop("the unattended run asked a question") {
				sess.cancelTurn()
				sess.notice("⏳ Overnight stopped: the agent asked a question")
			}
		}

	case agent.EventToolResult:
		if sess.srv.Files == nil || e.Call == nil || e.IsError() {
			return
		}
		path := ToolPath(e.Call.Name, e.Call.Args)
		if path == "" {
			return
		}
		path = sessionToolPath(cwd, path)
		if WritesFiles(e.Call.Name) && !e.NoWrite {
			// Queued on the *readers*, not on the writer. Keeping them here was
			// the bug: the writer then filtered out every conflict as belonging
			// to someone else and dropped it, so nobody was ever told.
			sess.srv.queueConflicts(sess.srv.Files.WriteWithDetails(
				name, path, turn, e.Intent, DiffPreview(e.Diff)))
			return
		}
		sess.srv.Files.Read(name, path, turn)

	case agent.EventTurnEnd:
		if sess.overnight != nil {
			sess.overnightTurnEnd()
		}
		// Safe point D: everything the turn asked for has come back, so a
		// notice now lands between turns rather than mid-thought (§6.3).
		sess.deliverConflicts()
		sess.mu.Lock()
		sess.turn++
		sess.mu.Unlock()
		if sess.Worker {
			// A worker's turn ending is the worker finishing. Its result goes
			// back to whoever summoned it as a message, so the spawner never
			// had to block on it.
			// A worker asked to retry its output is not finished: its next turn
			// end is the one that reports.
			sess.mu.Lock()
			failed := sess.failureNotified
			sess.mu.Unlock()
			if failed {
				sess.markFinished()
			} else if sess.srv.reportWorkerResult(sess) {
				sess.markFinished()
			}
		}
	}
}

// notifyWorkerFailure covers a Run that returns before its TurnEnd reaches the
// daemon pump. Without this handoff a spawner waits for a result that can never
// arrive; the one-shot flag also prevents the late TurnEnd from reporting a
// second, contradictory result.
func (sess *Session) notifyWorkerFailure(err error) {
	if !sess.Worker || sess.srv == nil {
		return
	}
	sess.mu.Lock()
	if sess.closedDone || sess.failureNotified {
		sess.mu.Unlock()
		return
	}
	sess.failureNotified = true
	sess.mu.Unlock()

	sess.srv.swarm.mu.Lock()
	spawner := sess.srv.swarm.spawnedBy[sess.Name]
	sess.srv.swarm.mu.Unlock()
	if spawner != "" {
		sess.srv.deliver(spawner, fmt.Sprintf(
			"⚠ worker %s stopped before reporting a result: %v", sess.Name, err))
	}
}

func (sess *Session) heartbeat(now time.Time) {
	sess.mu.Lock()
	sess.lastHeartbeat = now
	if sess.stale {
		sess.stale = false
		sess.staleNotified = false
	}
	sess.mu.Unlock()
}

// finished reports whether a worker has completed.
func (sess *Session) finished() bool {
	if sess.done == nil {
		return true
	}
	select {
	case <-sess.done:
		return true
	default:
		return false
	}
}

// markFinished closes the done channel exactly once.
func (sess *Session) markFinished() {
	sess.mu.Lock()
	first := sess.done != nil && !sess.closedDone
	if first {
		sess.closedDone = true
		close(sess.done)
	}
	sess.mu.Unlock()

	// Exactly once, and outside the session lock: the reservation this worker
	// holds against MaxLiveWorkers is what lets the next one start.
	if first && sess.Worker && sess.srv != nil {
		sess.srv.swarm.finished()
	}
	if first && sess.srv != nil {
		sess.srv.touch()
	}
}

// queueConflicts routes each conflict to the session that needs to hear it.
func (s *Server) queueConflicts(conflicts []Conflict) {
	for _, c := range conflicts {
		s.mu.Lock()
		target := s.sessions[c.Session]
		s.mu.Unlock()
		if target == nil {
			continue
		}
		target.mu.Lock()
		target.pending = append(target.pending, c)
		target.mu.Unlock()
	}
}

// deliverConflicts injects the notices this session has earned.
func (sess *Session) deliverConflicts() {
	sess.mu.Lock()
	pending := sess.pending
	sess.pending = nil
	sess.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	fresh := sess.srv.Files.Pending(sess.Name, pending)
	if len(fresh) == 0 {
		return
	}

	var lines []string
	if sess.srv.CompactNotices {
		lines = []string{CompactNotice(fresh)}
	} else {
		for _, c := range fresh {
			lines = append(lines, c.Notice())
		}
	}
	text := strings.Join(lines, "\n")
	sess.built.Agent.Interject(agent.Interrupt{
		Source: agent.SourceSystem,
		Text:   text,
	})
	// And as an event, so every attached client shows it. An interjection alone
	// only reaches the model: it becomes a conversation message, and a message
	// is not an event, so the humans watching would never learn that the file
	// under them had moved.
	sess.built.Agent.Notice(agent.LevelWarning, "%s", text)
}

func (sess *Session) broadcast(msg ServerMsg) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for ch := range sess.subs {
		select {
		case ch <- msg:
		default:
			// A client that cannot keep up is skipped rather than blocking the
			// agent. It reconnects with `since` and the ring fills the gap,
			// which is exactly what the ring is for.
		}
	}
}

func (sess *Session) subscribe() chan ServerMsg {
	ch := make(chan ServerMsg, 256)
	sess.mu.Lock()
	sess.subs[ch] = struct{}{}
	sess.mu.Unlock()
	return ch
}

func (sess *Session) unsubscribe(ch chan ServerMsg) {
	sess.mu.Lock()
	delete(sess.subs, ch)
	sess.mu.Unlock()
}

func (sess *Session) close() {
	sess.mu.Lock()
	sess.closing = true
	cancel, done := sess.cancel, sess.turnDone
	sess.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(turnUnwindTimeout):
			// A turn that will not unwind must not wedge shutdown. Losing its
			// tail is the same outcome as before this wait existed, and it is
			// reached only when something is already stuck.
		}
	}
	if sess.asks != nil {
		sess.asks.Cancel()
	}
	sess.consolidateMemory()
	sess.built.Close()
}

// consolidateMemory preserves the old TUI's exit behavior at the new
// lifecycle boundary. Closing a window is not session exit anymore; the
// daemon performs the summary only when its session is actually being torn
// down, such as an explicit stop or idle shutdown.
func (sess *Session) consolidateMemory() {
	if sess.built == nil || sess.built.Memory == nil ||
		!sess.built.Memory.Enabled() || sess.built.Agent == nil ||
		sess.built.Agent.Conv == nil || sess.built.Agent.Conv.Len() < 4 {
		return
	}
	var transcript strings.Builder
	for _, msg := range sess.built.Agent.Conv.Messages() {
		if text := strings.TrimSpace(msg.Content); text != "" {
			transcript.WriteString(string(msg.Role))
			transcript.WriteString(": ")
			transcript.WriteString(text)
			transcript.WriteByte('\n')
		}
	}
	if strings.TrimSpace(transcript.String()) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = sess.built.Memory.Consolidate(ctx, memory.Truncate(transcript.String(), 24000))
}

// turnUnwindTimeout bounds how long closing a session waits for a cancelled
// turn to finish writing.
const turnUnwindTimeout = 5 * time.Second

// snapshot describes the session to a client that has just attached.
func (sess *Session) snapshot(_ ...string) *Snapshot {
	sess.mu.Lock()
	name, model, cwd := sess.Name, sess.Model, sess.Cwd
	reserved := sess.running
	prov := sess.built.Agent.Provider
	agentModel := sess.built.Agent.Model
	sess.mu.Unlock()
	msgs := sess.built.Agent.Conv.Messages()
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == provider.RoleSystem || (m.Content == "" && len(m.ToolCalls) == 0 && m.ToolName == "") {
			continue
		}
		out = append(out, Message{
			Role:          string(m.Role),
			Content:       m.Content,
			Reasoning:     m.Reasoning,
			ToolCalls:     m.ToolCalls,
			ProviderItems: m.ProviderItems,
			ToolCallID:    m.ToolCallID,
			ToolName:      m.ToolName,
			IsError:       m.IsError,
			Held:          m.Held,
			Images:        m.Images,
			Hidden:        m.Role == provider.RoleUser && m.Content == overnightPrompt,
			Repairs:       m.Repairs,
		})
	}
	levels := provider.ReasoningEffortLevelsForProvider(
		prov, agentModel)
	levelNames := make([]string, 0, len(levels))
	for _, level := range levels {
		levelNames = append(levelNames, string(level))
	}
	effort := ""
	if len(levels) > 0 && provider.SupportsReasoningEffort(prov) {
		active := sess.built.Agent.ReasoningEffort()
		if !containsReasoningEffort(levels, active) {
			active = levels[0]
		}
		effort = string(active)
	}
	var pending []agent.AskEvent
	if sess.asks != nil {
		pending = sess.asks.Snapshot()
	}
	var mcpStatus []MCPStatus
	if sess.mcp != nil {
		for _, summary := range sess.mcp.Summaries() {
			mcpStatus = append(mcpStatus, MCPStatus{Name: summary.Name, Tools: summary.Tools})
		}
	}
	var skillNames []string
	if sess.built.Skills != nil {
		skillNames = sess.built.Skills.Names()
	}
	var background []BackgroundTask
	if sess.built.Exec != nil && sess.built.Exec.Bg != nil {
		for _, task := range sess.built.Exec.Bg.Tasks() {
			done, failed, _ := task.Snapshot()
			background = append(background, BackgroundTask{
				ID: task.ID, Label: task.Label, Done: done,
				Failed: failed, Progress: task.Progress().String(),
			})
		}
	}
	return &Snapshot{
		Session:          name,
		Model:            model,
		Provider:         prov.Name(),
		Cwd:              cwd,
		ReasoningEffort:  effort,
		ReasoningEfforts: levelNames,
		Skills:           skillNames,
		MCP:              mcpStatus,
		Running:          reserved || sess.built.Agent.Running(),
		Seq:              sess.ring.Seq(),
		Messages:         out,
		Pending:          pending,
		Background:       background,
	}
}

func containsReasoningEffort(levels []provider.ReasoningEffort, want provider.ReasoningEffort) bool {
	for _, level := range levels {
		if level == want {
			return true
		}
	}
	return false
}

// Input starts a turn. A turn already in flight is queued, which gives every
// attached client one ordered input stream instead of letting two windows race
// to mutate the same provider conversation.
func (sess *Session) Input(text string, images ...[][]byte) {
	sess.InputRequest("", text, images...)
}

// InputRequest starts or queues a prompt and carries a caller request id into
// TurnStart. Headless waiters use that id to distinguish their queued turn from
// an older turn already running in the same session.
func (sess *Session) InputRequest(requestID, text string, images ...[][]byte) {
	if sess.overnight != nil && !isOvernightRequest(requestID) && sess.overnight.isActive() {
		if sess.overnight.stop("you stopped it by sending a prompt") {
			sess.cancelTurn()
			sess.notice("⏳ Overnight stopped: you sent a prompt")
		}
	}
	a := sess.built.Agent
	ctx, cancel := context.WithCancel(context.Background())
	done, ok := sess.beginTurn(cancel)
	if !ok {
		cancel()
		sess.mu.Lock()
		closing := sess.closing
		if !closing {
			var attached [][]byte
			if len(images) > 0 {
				attached = images[0]
			}
			sess.queued = append(sess.queued, queuedInput{
				requestID: requestID, text: text, images: attached,
			})
		}
		position := len(sess.queued)
		sess.mu.Unlock()
		if !closing {
			a.Notice(agent.LevelInfo, "queued prompt %d until the current turn ends", position)
		}
		return
	}
	sess.mu.Lock()
	sess.requestID = requestID
	sess.mu.Unlock()
	var attached [][]byte
	if len(images) > 0 {
		attached = images[0]
	}
	sess.launchTurn(text, attached, ctx, cancel, done)
}

func (sess *Session) launchTurn(text string, images [][]byte, ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	go func() {
		defer close(done)
		defer sess.endTurn()
		defer cancel()
		if len(images) > 0 {
			sess.built.Agent.Attach(images)
		}
		_ = sess.built.Agent.Run(ctx, text)
	}()
}

func (sess *Session) overnightTurnEnd() {
	if sess.overnight == nil {
		return
	}
	if !sess.overnight.isActive() {
		return
	}
	state := ""
	if sess.built.Todos != nil {
		state = sess.built.Todos.Summary()
	}
	if ok, reason := sess.overnight.afterTurn(time.Now(), state); ok {
		sess.InputRequest(fmt.Sprintf("overnight-%d", sess.overnightTurns()), overnightPrompt)
	} else {
		_, _ = sess.writeOvernightReport()
		sess.notice(reason)
	}
}

func (sess *Session) overnightTurns() int {
	sess.overnight.mu.Lock()
	defer sess.overnight.mu.Unlock()
	return sess.overnight.turns + 1
}

// SetModel switches the provider behind a live session. Model selection is a
// runtime operation, not a TUI preference: every attached client must observe
// the same provider, conversation limits, and durable model metadata.
func (sess *Session) SetModel(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("model reference is required")
	}
	sess.mu.Lock()
	if sess.closing {
		sess.mu.Unlock()
		return fmt.Errorf("session is closing")
	}
	if sess.running {
		sess.mu.Unlock()
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	sess.mu.Unlock()

	cfg := sess.built.Config.Clone()
	prov, model, err := cfg.Resolve(ref)
	if err != nil {
		return err
	}
	overrides := cfg.ModelOverrides(model)
	if err := sess.built.Store.WriteModel(config.ModelRef(model, prov.Name())); err != nil {
		return err
	}

	sess.mu.Lock()
	if sess.closing || sess.running {
		sess.mu.Unlock()
		return fmt.Errorf("session became busy while switching models")
	}
	sess.Model = model
	sess.built.Config = cfg
	sess.built.Agent.Provider = prov
	sess.built.Agent.Model = model
	sess.built.Agent.NumCtx = config.ContextWindowFor(prov, model, overrides.ContextWindow)
	sess.built.Agent.MaxSteps = cfg.Features.MaxSteps
	if sess.built.FS != nil {
		sess.built.FS.WithAnchors(overrides.AnchorEdits).WithVision(overrides.Vision)
	}
	if sess.built.Memory != nil {
		sess.built.Memory.Embedder = prov
		sess.built.Memory.SetEmbeddingModel(prov.Name() + "::embedding")
	}
	if sess.built.Agent.Compactor != nil {
		sess.built.Agent.Compactor.SetEmbeddingProvider(prov)
	}
	sess.mu.Unlock()

	levels := provider.ReasoningEffortLevelsForProvider(prov, model)
	levelNames := make([]string, 0, len(levels))
	for _, level := range levels {
		levelNames = append(levelNames, string(level))
	}
	sess.publishEvent(agent.Event{
		Kind:             agent.EventModel,
		Model:            model,
		Provider:         prov.Name(),
		ReasoningEffort:  sess.built.Agent.ReasoningEffort(),
		ReasoningEfforts: levelNames,
	})
	return nil
}

// beginTurn records a turn's cancel and completion channel under the session
// lock, and reports whether it may start.
//
// Every path that touches cancel goes through here or cancelTurn. It used to be
// assigned, read and cleared from input, interrupt, close and the worker spawn
// with no lock between them: two attached clients could each overwrite the
// other's, leaving an interrupt cancelling a turn that had already ended and
// the live one unstoppable.
func (sess *Session) beginTurn(cancel context.CancelFunc) (chan struct{}, bool) {
	done := make(chan struct{})
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closing || sess.running || sess.retrying {
		return nil, false
	}
	// The previous turn is finished — running says so — and cancelling a
	// finished turn only releases its context.
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.running = true
	sess.cancel, sess.turnDone = cancel, done
	return done, true
}

// beginRetryTurn reserves the same session slot for a schema retry. The retry
// is launched after the original turn's completion channel closes, so it must
// take the reservation itself before it calls Agent.Loop.
func (sess *Session) beginRetryTurn(cancel context.CancelFunc) (chan struct{}, bool) {
	done := make(chan struct{})
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closing || sess.running {
		return nil, false
	}
	sess.running = true
	sess.cancel, sess.turnDone = cancel, done
	return done, true
}

// endTurn releases the session's turn reservation.
func (sess *Session) endTurn() {
	sess.mu.Lock()
	sess.running = false
	var next *queuedInput
	var ctx context.Context
	var cancel context.CancelFunc
	var done chan struct{}
	if !sess.closing && len(sess.queued) > 0 {
		item := sess.queued[0]
		sess.queued = sess.queued[1:]
		copyItem := item
		next = &copyItem
		ctx, cancel = context.WithCancel(context.Background())
		done = make(chan struct{})
		sess.running = true
		sess.cancel, sess.turnDone = cancel, done
		sess.requestID = item.requestID
	}
	sess.mu.Unlock()
	if next != nil {
		sess.launchTurn(next.text, next.images, ctx, cancel, done)
	}
}

// cancelTurn stops the current turn, if there is one.
func (sess *Session) cancelTurn() {
	sess.mu.Lock()
	cancel := sess.cancel
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Interrupt injects a message into a live turn, or cancels it when there is no
// text to inject.
func (sess *Session) Interrupt(text string, urgent bool) {
	if text == "" {
		sess.cancelTurn()
		return
	}
	source := agent.SourceUser
	sess.built.Agent.Interject(agent.Interrupt{Source: source, Text: text, Urgent: urgent})
}

// handle serves one client connection.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	s.touch()

	enc := json.NewEncoder(conn)
	var (
		sess *Session
		sub  chan ServerMsg
		done = make(chan struct{})

		// relayStop ends the current attachment's relay. Per subscription, not
		// per connection: every attach used to start a relay and stop none, so
		// a client switching sessions left one goroutine per switch blocked on
		// a channel it had already been unsubscribed from.
		relayStop chan struct{}
	)
	stopRelay := func() {
		if relayStop != nil {
			close(relayStop)
			relayStop = nil
		}
	}
	defer func() {
		s.touch()
		stopRelay()
		close(done)
		if sess != nil && sub != nil {
			sess.unsubscribe(sub)
		}
	}()

	// A writer failure takes the connection down with it. Returning from the
	// writer alone left the reader, the relay and every event producer running
	// against a connection nobody was draining, until their queues wedged.
	var closeOnce sync.Once
	drop := func() { closeOnce.Do(func() { conn.Close() }) }

	// One writer goroutine owns the connection, so a broadcast and a reply can
	// never interleave halfway through a JSON frame.
	out := make(chan ServerMsg, 256)
	go func() {
		for {
			select {
			case msg := <-out:
				if msg.Version == 0 {
					msg.Version = ProtocolVersion
				}
				if err := enc.Encode(msg); err != nil {
					drop()
					return
				}
			case <-done:
				return
			case <-ctx.Done():
				// The server is shutting down. The reader is blocked on the
				// connection and will not notice on its own, so drop it.
				drop()
				return
			}
		}
	}()
	send := func(msg ServerMsg) {
		select {
		case out <- msg:
		case <-done:
		}
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		s.touch()
		var msg ClientMsg
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			send(ServerMsg{Kind: MsgError, Err: "malformed frame: " + err.Error()})
			continue
		}
		if msg.Version != 0 && msg.Version != ProtocolVersion {
			send(ServerMsg{Kind: MsgError, Err: fmt.Sprintf(
				"unsupported client protocol version %d (want %d)", msg.Version, ProtocolVersion)})
			continue
		}

		switch msg.Kind {
		case MsgStatus:
			send(ServerMsg{Kind: MsgStatus, Status: s.Status()})

		case MsgStop:
			send(ServerMsg{Kind: MsgStatus, Status: s.Status()})
			go s.Close()

		case MsgList:
			send(ServerMsg{Kind: MsgSessions, Sessions: s.Sessions()})

		case MsgAttach:
			opened, err := s.OpenWithOptions(msg.Session, OpenOptions{
				Cwd: msg.Cwd, Model: msg.Model, NoTools: msg.NoTools,
			})
			if err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
				continue
			}
			stopRelay()
			if sess != nil && sub != nil {
				sess.unsubscribe(sub)
			}
			sess = opened
			// Subscribe before replaying, so an event arriving mid-replay is
			// queued rather than dropped into the gap between the two.
			sub = sess.subscribe()
			send(ServerMsg{Kind: MsgSnapshot, Snapshot: sess.snapshot()})
			// A fresh attach replays only the turn in flight: the snapshot
			// already holds every completed message, so replaying their deltas
			// too would draw the conversation twice. A reconnecting client
			// names the last sequence it saw and gets the gap instead.
			replay := sess.ring.SinceLastTurn()
			if msg.Since > 0 {
				replay, _ = sess.ring.Since(msg.Since)
			}
			for i := range replay {
				send(ServerMsg{Kind: MsgEvent, Event: &replay[i]})
			}
			relayStop = make(chan struct{})
			go func(sub chan ServerMsg, stop chan struct{}) {
				for {
					select {
					case msg := <-sub:
						send(msg)
					case <-stop:
						return
					case <-done:
						return
					}
				}
			}(sub, relayStop)

		case MsgInput:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "input before attach"})
				continue
			}
			sess.InputRequest(msg.RequestID, msg.Text, msg.Images)

		case MsgInterrupt:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "interrupt before attach"})
				continue
			}
			sess.Interrupt(msg.Text, msg.Urgent)

		case MsgAnswer:
			if sess == nil || sess.asks == nil {
				send(ServerMsg{Kind: MsgError, Err: "answer before attach"})
				continue
			}
			if err := sess.asks.Answer(msg.RequestID, msg.Answers); err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
			}

		case MsgModel:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "model switch before attach"})
				continue
			}
			if err := sess.SetModel(msg.Model); err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
			}

		case MsgCommand:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "command before attach"})
				continue
			}
			if err := sess.Command(msg.Text, msg.Arg, msg.Secret); err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
			}

		case MsgReasoningEffort:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "reasoning effort before attach"})
				continue
			}
			effort := provider.ReasoningEffort(msg.ReasoningEffort)
			if err := sess.built.Agent.SetReasoningEffort(effort); err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
				continue
			}
			ref := config.ModelRef(sess.built.Agent.Model, sess.built.Agent.Provider.Name())
			if err := config.SaveReasoningEffort(ref, effort); err != nil {
				send(ServerMsg{Kind: MsgError, Err: "could not remember reasoning effort: " + err.Error()})
				continue
			}
			s.mu.Lock()
			if s.Cfg.ReasoningEfforts == nil {
				s.Cfg.ReasoningEfforts = map[string]string{}
			}
			s.Cfg.ReasoningEfforts[ref] = string(effort)
			s.mu.Unlock()
			sess.publishEvent(agent.Event{
				Kind:            agent.EventReasoningEffort,
				ReasoningEffort: effort,
			})

		case MsgSpawn:
			// Attributed to the attached session, not spawned free-floating:
			// that is what makes the worker's result come back here when it
			// finishes rather than vanishing into the daemon.
			spawner := msg.Session
			if sess != nil {
				spawner = sess.Name
			}
			name, err := s.SpawnFor(spawner, msg.Task, msg.Files, msg.Schema)
			if err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
				continue
			}
			// Only the new worker. Replying with the whole roster made the
			// client read Sessions[0] — the oldest session, which is the one
			// that did the summoning — and report that it had summoned itself.
			send(ServerMsg{Kind: MsgSessions, Sessions: s.sessionInfo(name)})
			if spawner != "" {
				s.deliver(spawner, fmt.Sprintf(
					"👉 Worker %s started on: %s", name, msg.Task))
			}

		case MsgDetach:
			// The relay stops with the subscription. Unsubscribing alone left
			// it blocked on a channel nothing publishes to until the whole
			// connection closed, which is the H3.8 leak by a second route.
			stopRelay()
			if sess != nil && sub != nil {
				sess.unsubscribe(sub)
				sess, sub = nil, nil
			}

		default:
			send(ServerMsg{Kind: MsgError, Err: "unknown kind " + msg.Kind})
		}
	}
}
