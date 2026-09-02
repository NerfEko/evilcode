package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

	// socketUnlock releases the lifetime claim on the socket path, taken by
	// Listen and held until Close. A second daemon must fail the claim while
	// this one lives, even if its socket file has been deleted from under it.
	socketUnlock func()
	// socketIno is the inode of the socket file this daemon bound. Close only
	// removes the path when it still names this inode — a by-name remove from
	// a daemon whose path was stolen would delete the successor's socket.
	socketIno uint64

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
	// already-closed server. Idle session teardown uses the same barrier: its
	// final memory consolidation must finish before the daemon closes the bank.
	builds sync.WaitGroup

	// bank is one file handle shared by the daemon, while each memory manager
	// scopes its records to the appropriate project/global view. Todo stores
	// are deliberately not held here: todo state belongs to one session.
	bank *memory.Store
}

// memoryBank returns the daemon's shared memory file handle. Memory records are
// scoped by the manager; this sharing is only to serialize concurrent appends
// to one durable bank. Todo state is intentionally per-session.
func (s *Server) memoryBank() *memory.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bank == nil {
		s.bank, _ = memory.Open(config.DataDir())
	}
	return s.bank
}

// Session is one live conversation inside the daemon.
type Session struct {
	Name    string
	Model   string
	Cwd     string
	Task    string
	Worker  bool
	NoTools bool
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

	// foreground means the worker was created by the spawn_worker tool. Those
	// calls follow OpenCode's task semantics: the parent waits for the worker's
	// result instead of continuing to spend model turns beside it.
	foreground bool

	// workerFailure is returned to a foreground spawner. Async workers still
	// receive the same failure as an inbox message, so this is not a second
	// reporting path for them.
	workerFailure error

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

	// pendingCredential names a provider whose freshly saved API key could not
	// replace this session's live provider instance because a turn was in
	// flight. applyPendingCredential consumes it at the next turn boundary
	// (R2-12).
	pendingCredential string

	mu sync.Mutex
	// controlMu serializes runtime configuration changes that must be observed
	// as one session-wide state transition by every attached client.
	controlMu sync.Mutex

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
	turnDone      chan struct{}
	queued        []queuedInput
	requestID     string
	requestHidden bool

	// closing refuses new turns once close has begun. Without it a turn
	// starting while close waits replaces the channel close is waiting on, and
	// the store shuts under a turn that is still running.
	closing bool

	// running is the turn reservation. Agent.Running() is set by the turn
	// goroutine and so is false for the first instants after a start, which let
	// two clients both see an idle session and both launch against one
	// conversation. Taken here, before the goroutine exists.
	running bool

	// idleSince is when the last attached window left (or when the session was
	// first created with no window). It is guarded by mu. The timestamp survives
	// a detached turn: the sweep separately checks that the turn has finished,
	// so a long-running background turn can be unloaded as soon as it ends if
	// its window has already been gone for the full timeout.
	idleSince time.Time
}

type queuedInput struct {
	requestID string
	text      string
	images    [][]byte
	hidden    bool
}

const (
	// MaxQueuedInputs bounds prompts waiting for a busy session. The queue is
	// the anti-starvation path for one user, not a sink for unbounded
	// automation; a full queue is rejected visibly (D2).
	MaxQueuedInputs = 64
	// MaxQueuedInputBytes bounds the queue's total payload, images included.
	// Unbounded queues let one client enqueue prompts/images faster than a
	// model finishes, growing memory without limit.
	MaxQueuedInputBytes = 16 << 20
)

func queuedInputBytes(q []queuedInput) int {
	n := 0
	for _, in := range q {
		n += len(in.text)
		for _, img := range in.images {
			n += len(img)
		}
	}
	return n
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
// have stopped producing events and unloads sessions that have been idle with
// no attached window. The check is cheap; the longer thresholds below are what
// prevent normal provider/tool gaps from looking like failure.
const WorkerHeartbeatInterval = 5 * time.Second

// SessionIdleTimeout is how long a session may have no attached window and no
// turn in flight before the daemon marks it cleanly finished and unloads its
// live runtime. Its durable log remains available for a later resume.
const SessionIdleTimeout = 10 * time.Minute

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
				now := time.Now()
				s.refreshWorkerStaleness(now)
				s.expireIdleSessions(now)
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

// expireIdleSessions unloads hydrated sessions whose last window detached at
// least SessionIdleTimeout ago and whose turn is now idle. The live map entry
// is removed before closing the runtime so a new opener waits on opening[name]
// rather than receiving a session whose store is already being torn down.
//
// The server's builds wait group also covers this teardown. Close waits on it
// before closing the shared memory bank, so an idle sweep racing daemon
// shutdown cannot consolidate against a bank that has already been closed.
func (s *Server) expireIdleSessions(now time.Time) {
	type candidate struct {
		name string
		sess *Session
		gate chan struct{}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var expired []candidate
	for name, sess := range s.sessions {
		sess.mu.Lock()
		idleSince := sess.idleSince
		agentRunning := sess.built != nil && sess.built.Agent != nil && sess.built.Agent.Running()
		idle := !sess.closing && len(sess.subs) == 0 && !sess.running && !agentRunning &&
			!idleSince.IsZero() && now.Sub(idleSince) >= SessionIdleTimeout
		if idle {
			// Recheck and mark under the session lock while the server lock is
			// held. subscribe and beginTurn therefore cannot sneak in between
			// the test and the removal from the live map.
			sess.closing = true
			gate := make(chan struct{})
			if s.opening == nil {
				s.opening = map[string]chan struct{}{}
			}
			s.opening[name] = gate
			delete(s.sessions, name)
			s.builds.Add(1)
			expired = append(expired, candidate{name: name, sess: sess, gate: gate})
		}
		sess.mu.Unlock()
	}
	s.mu.Unlock()

	for _, item := range expired {
		func() {
			defer s.builds.Done()
			item.sess.close()
			s.mu.Lock()
			if wait, ok := s.opening[item.name]; ok && wait == item.gate {
				delete(s.opening, item.name)
				close(item.gate)
			}
			s.mu.Unlock()
		}()
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

	// Serialized against other starting daemons — and, since the lock is held
	// for the daemon's lifetime, against any daemon at all: a second daemon on
	// this path fails the claim even if the first one's socket file was
	// deleted from under it.
	unlock, err := lockSocketPath(s.Path)
	if err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		unlock()
		if !errors.Is(err, syscall.EADDRINUSE) {
			return err
		}
		if conn, derr := net.Dial("unix", s.Path); derr == nil {
			conn.Close()
			return fmt.Errorf("a daemon is already listening on %s", s.Path)
		}
		// Nothing answered: the socket is a leftover from a daemon that died,
		// which is exactly when the claim above is free.
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
		unlock()
		return err
	}
	// Go's UnixListener unlinks the socket path on Close, by name and with no
	// ownership check — a daemon whose path was rebound to a successor's socket
	// would delete the successor's file on its way out, and that is precisely
	// how one daemon exit took the next daemon's reachability with it. The
	// guarded remove in Close is the only unlinker.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if ino, ok := socketInode(s.Path); ok {
		s.socketIno = ino
	}
	s.socketUnlock = unlock
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
	// bank it borrows — but not forever. wiring.Build is local I/O only, so a
	// healthy build returns in well under the budget; a build that blows the
	// budget is stuck on the filesystem (a hung NFS/FUSE mount), and a daemon
	// shutdown must not wedge on that. On timeout the orphaned build keeps the
	// bank it borrowed and cleans up via its own closed-check; the bank is not
	// closed here to avoid racing that in-flight Build. The leak is benign: this
	// only runs at shutdown.
	bankOwned := waitForBuilds(&s.builds, closeBuildBudget)
	// Only remove the path when it still names the socket this daemon bound.
	// A late-exiting daemon used to delete whatever inode the path named —
	// after an orphaning, that is the successor's live socket — which is how
	// one daemon exit took the next one's reachability with it.
	if ino, ok := socketInode(s.Path); !ok || ino == s.socketIno {
		os.Remove(s.Path)
	}
	if s.socketUnlock != nil {
		s.socketUnlock()
		s.socketUnlock = nil
	}
	for _, sess := range sessions {
		sess.close()
	}
	// The bank is the server's, so it outlives every session and is closed last:
	// a session's own Close must not take the swarm's memory down with it.
	if bank != nil && bankOwned {
		bank.Close()
	}
}

// closeBuildBudget bounds how long Close waits for an in-flight build before
// proceeding with the rest of shutdown. Generous for the local I/O a build
// does, short enough that a stuck filesystem does not wedge teardown.
const closeBuildBudget = 10 * time.Second

// waitForBuilds reports whether all builds finished within the budget. A
// sync.WaitGroup has no timeout of its own, so the wait runs in a goroutine
// and the budget is enforced with a timer.
func waitForBuilds(wg *sync.WaitGroup, budget time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
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
		// asks is the ask broker; its Snapshot is goroutine-safe, so it can be
		// read outside the session lock.
		pending := 0
		if sess.asks != nil {
			pending = len(sess.asks.Snapshot())
		}
		// Conversation length excludes the system message prepended by Conv.
		msgCount := 0
		if msgs := sess.built.Agent.Conv.Messages(); len(msgs) > 0 {
			msgCount = len(msgs)
			if msgs[0].Role == provider.RoleSystem {
				msgCount--
			}
		}
		seen[name] = true
		out = append(out, SessionInfo{
			Name:     name,
			Model:    model,
			Running:  running || sess.built.Agent.Running(),
			Clients:  clients,
			Worker:   worker,
			Task:     task,
			Started:  started,
			Stale:    stale,
			Cwd:      cwd,
			Stored:   true,
			Live:     true,
			Pending:  pending,
			Messages: msgCount,
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
				Messages: info.Messages,
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
			if opts.NoTools && !sess.NoTools {
				return nil, fmt.Errorf("session %q already has tools; --no-tools only applies when creating a new session", name)
			}
			if opts.Model != "" {
				if err := sess.SetModel(opts.Model); err != nil {
					return nil, err
				}
			}
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
	cfg.AddDiscoveredCodex()
	bank := s.memoryBank()
	asks := newAskBroker()
	var mcpClient *mcp.Client
	var extraTools tools.Set
	var extraClosers []func()
	if !opts.NoTools && len(cfg.MCP) > 0 {
		mcpClient = mcp.New()
		var servers []mcp.ServerConfig
		for _, srv := range cfg.MCP {
			servers = append(servers, mcp.ServerConfig{
				Name: srv.Name, Command: srv.Command, Args: srv.Args, Env: srv.Env, Timeout: time.Duration(srv.TimeoutSeconds) * time.Second,
			})
		}
		for _, mcpErr := range mcpClient.Connect(context.Background(), servers) {
			fmt.Fprintln(os.Stderr, "evilcode:", mcpErr)
		}
		extraTools, extraClosers = mcpClient.Tools(), []func(){mcpClient.Close}
	}
	built, err := wiring.Build(cfg, wiring.Options{
		Model: opts.Model, Resume: name, Cwd: cwd, Extract: true, NoTools: opts.NoTools,
		Bank: bank, Asker: asks,
		ExtraTools: extraTools, ExtraClosers: extraClosers,
	})
	if err != nil {
		return nil, err
	}
	if mcpClient != nil {
		// A server that announced tools/list_changed mid-session refreshes the
		// agent's definitions at the next safe point (mcp_gaps #5). `built` is
		// final here, so the closure needs no synchronization.
		ag := built.Agent
		mcpClient.OnToolsChanged(func(ts tools.Set) { ag.SetTools(ts) })
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
		idleSince: time.Now(),
	}
	sess.NoTools = opts.NoTools
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
	// to coordinate with. A --no-tools session must not receive these either.
	if !opts.NoTools {
		sess.built.Agent.Tools = append(sess.built.Agent.Tools, s.AgentTools(sess.Name)...)
	}

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
	turnEnd := e.Kind == agent.EventTurnEnd
	sess.mu.Lock()
	if e.Session == "" {
		e.Session = sess.Name
	}
	if e.Kind == agent.EventTurnStart {
		e.RequestID = sess.requestID
		e.Hidden = sess.requestHidden || isOvernightRequest(e.RequestID)
		if e.Hidden {
			// The unattended prompt remains in the durable conversation, but it
			// is an implementation detail rather than a fake user message for
			// every attached window.
			e.Text = ""
		}
		sess.requestID = ""
		sess.requestHidden = false
	}
	sess.mu.Unlock()
	if turnEnd {
		// A remote Agent is a render mirror, not a second conversation owner.
		// Include the authoritative post-turn history so /context and a client
		// that stays attached after a memory/compaction hook do not drift.
		history := sess.built.Agent.Conv.Messages()
		if len(history) > 0 && history[0].Role == provider.RoleSystem {
			history = history[1:]
		}
		// Image bytes never travel in the history copy. Nothing re-renders an
		// image from history — blocks are built from text and tool metadata —
		// and one 20 MiB read base64-expands past the client's whole frame
		// limit, disconnecting it at the exact moment a turn completed.
		for i := range history {
			history[i].Images = nil
		}
		e.SnapshotMessages = history
		e.SnapshotEpoch = sess.built.Agent.Conv.Epoch()
	}
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

// renameSession changes the live map key and the durable stores together. The
// server lock is acquired before the session locks, matching Sessions and
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
	if sess.closing {
		sess.mu.Unlock()
		return fmt.Errorf("session is closing")
	}
	if sess.running || sess.built.Agent.Running() {
		sess.mu.Unlock()
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	old := sess.Name
	if old == name {
		sess.mu.Unlock()
		return nil
	}
	if err := sess.built.Store.Rename(config.DataDir(), name); err != nil {
		sess.mu.Unlock()
		return err
	}
	if sess.built.Todos != nil {
		if err := sess.built.Todos.Rename(name); err != nil {
			// Keep the conversation and its plan together. The todo rename is
			// all-or-nothing; if it cannot move, put the session log back before
			// publishing any new identity to the server.
			rollbackErr := sess.built.Store.Rename(config.DataDir(), old)
			if rollbackErr != nil {
				sess.mu.Unlock()
				return errors.Join(err, fmt.Errorf("rolling back session rename: %w", rollbackErr))
			}
			sess.mu.Unlock()
			return err
		}
	}
	delete(s.sessions, old)
	s.sessions[name] = sess
	sess.Name = name
	sess.built.Agent.Session = name
	sess.mu.Unlock()

	// D3: identity-indexed coordination state must move with the session, or a
	// renamed agent becomes a stranger to the swarm: worker results stop
	// routing back to it, its spawn budget resets, queued messages sit under
	// an unreachable key, and file-conflict history forgets who it is.
	s.swarm.mu.Lock()
	for worker, spawner := range s.swarm.spawnedBy {
		if spawner == old {
			s.swarm.spawnedBy[worker] = name
		}
	}
	if count := s.swarm.spawnCount[old]; count > 0 {
		delete(s.swarm.spawnCount, old)
		s.swarm.spawnCount[name] = count
	}
	if queued := s.swarm.inbox[old]; len(queued) > 0 {
		delete(s.swarm.inbox, old)
		s.swarm.inbox[name] = queued
	}
	s.swarm.mu.Unlock()
	if s.Files != nil {
		s.Files.Rename(old, name)
	}

	// Publish the new identity so attached clients stop addressing the old
	// name (their pollers and closures follow the snapshot).
	sess.publishSnapshot()
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
		sess.applyPendingCredential()
		sess.mu.Lock()
		sess.turn++
		sess.mu.Unlock()
		if sess.Worker {
			// A worker's turn ending is the worker finishing. Async workers send
			// their result to whoever summoned them; foreground workers suppress
			// that duplicate message because the tool call is waiting for it.
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
	sess.workerFailure = err
	foreground := sess.foreground
	sess.mu.Unlock()

	sess.srv.swarm.mu.Lock()
	spawner := sess.srv.swarm.spawnedBy[sess.Name]
	sess.srv.swarm.mu.Unlock()
	if spawner != "" && !foreground {
		sess.srv.deliver(spawner, fmt.Sprintf(
			"⚠ worker %s stopped before reporting a result: %v", sess.Name, err))
	}
}

func (sess *Session) isForeground() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.foreground
}

func (sess *Session) failure() error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.workerFailure
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
	// An attached window keeps the hydrated runtime alive regardless of when
	// the last turn ended.
	sess.idleSince = time.Time{}
	sess.mu.Unlock()
	return ch
}

func (sess *Session) unsubscribe(ch chan ServerMsg) {
	sess.mu.Lock()
	delete(sess.subs, ch)
	if len(sess.subs) == 0 && !sess.closing {
		// Start the countdown when the last window leaves. A turn that is still
		// running is allowed to finish, but it does not reset the windowless
		// interval.
		sess.idleSince = time.Now()
	} else if len(sess.subs) > 0 {
		sess.idleSince = time.Time{}
	}
	sess.mu.Unlock()
}

// close tears down one live runtime. A session that is between turns gets the
// normal clean-exit marker; one that still owns a turn reservation is closed
// without that marker so resume can report the interrupted run as crashed.
func (sess *Session) close() {
	sess.mu.Lock()
	sess.closing = true
	// The reservation is the daemon's authoritative view of a turn. It stays
	// held through the event tail and queued-input handoff, even after the
	// provider has released its own running flag.
	active := sess.running
	cancel, done := sess.cancel, sess.turnDone
	sess.mu.Unlock()
	if sess.built != nil && sess.built.Agent != nil && sess.built.Agent.Running() {
		active = true
	}

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
	if active && sess.built != nil && sess.built.Store != nil {
		// Keep the normal wiring close order while suppressing only the clean
		// lifecycle marker. A provider turn interrupted by daemon shutdown must
		// be recoverable as a crash on the next resume.
		sess.built.Store.MarkUnclean()
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
	sess.controlMu.Lock()
	defer sess.controlMu.Unlock()
	sess.mu.Lock()
	name, model, cwd := sess.Name, sess.Model, sess.Cwd
	reserved := sess.running
	prov := sess.built.Agent.Provider
	agentModel := sess.built.Agent.Model
	cfg := sess.built.Config.Clone()
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
			// No image bytes: history is never re-rendered from its bytes,
			// and a 20 MiB image would push the attach frame far past the
			// client's 8 MiB scanner limit (R2-01).
			Images:  nil,
			Hidden:  m.Hidden,
			Repairs: m.Repairs,
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
		Vision:           cfg != nil && cfg.ModelOverrides(config.ModelRef(agentModel, prov.Name())).Vision,
		ContextWindow:    sess.built.Agent.NumCtx,
		Skills:           skillNames,
		MCP:              mcpStatus,
		Running:          reserved || sess.built.Agent.Running(),
		Seq:              sess.ring.Seq(),
		Epoch:            sess.built.Agent.Conv.Epoch(),
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
	sess.inputRequest(requestID, text, false, images...)
}

// InputRequestHidden carries the render-only visibility marker for prompts
// authored by a frontend harness. The provider still receives the full text.
func (sess *Session) InputRequestHidden(requestID, text string, hidden bool, images ...[][]byte) {
	sess.inputRequest(requestID, text, hidden, images...)
}

func (sess *Session) inputRequest(requestID, text string, hidden bool, images ...[][]byte) {
	hidden = hidden || isOvernightRequest(requestID)
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
			// D2: the queue is bounded in count and bytes. An unbounded one let
			// a client enqueue prompts/images faster than the model finishes,
			// growing daemon memory without limit; a full queue is refused
			// visibly rather than accepted and forgotten.
			newBytes := len(text)
			for _, img := range attached {
				newBytes += len(img)
			}
			if len(sess.queued) >= MaxQueuedInputs ||
				queuedInputBytes(sess.queued)+newBytes > MaxQueuedInputBytes {
				sess.mu.Unlock()
				a.Notice(agent.LevelError,
					"queued input rejected: the session's queue is full "+
						"(max %d prompts or %d MiB); wait for the current turn to end",
					MaxQueuedInputs, MaxQueuedInputBytes/(1<<20))
				return
			}
			sess.queued = append(sess.queued, queuedInput{
				requestID: requestID, text: text, images: attached, hidden: hidden,
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
	sess.requestHidden = hidden
	sess.mu.Unlock()
	var attached [][]byte
	if len(images) > 0 {
		attached = images[0]
	}
	sess.launchTurn(text, attached, hidden, ctx, cancel, done)
}

func (sess *Session) launchTurn(text string, images [][]byte, hidden bool, ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	go func() {
		defer close(done)
		defer sess.endTurn()
		defer cancel()
		if len(images) > 0 {
			sess.built.Agent.Attach(images)
		}
		if hidden {
			_ = sess.built.Agent.RunHidden(ctx, text)
		} else {
			_ = sess.built.Agent.Run(ctx, text)
		}
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
	return sess.setModel(ref, "")
}

// SetModelWithEffort applies a model switch and an optional reasoning choice as
// one transition. An attached client may have a stale catalog, so validating
// the effort after changing the model would leave the session half-switched.
func (sess *Session) SetModelWithEffort(ref string, effort provider.ReasoningEffort) error {
	return sess.setModel(ref, effort)
}

// setModel switches the session's provider and model as one transition. The
// candidate — resolved provider, reasoning levels, effort validation, context
// window — is built without holding controlMu, because resolving a reference
// and asking an Ollama endpoint for model metadata are network-shaped
// operations that used to run under the lock — one of them unbounded — and
// stall snapshots (R2-13). Committing then takes controlMu, re-reads live
// session state, and swaps every runtime field in one hold of mu: a rejected
// switch leaves the previous runtime untouched, and an accepted one never
// publishes a half-updated runtime.
func (sess *Session) setModel(ref string, requested provider.ReasoningEffort) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("model reference is required")
	}
	// Candidate phase. A turn that starts here is caught by the commit-time
	// re-check, not by these early tests.
	sess.mu.Lock()
	if sess.closing {
		sess.mu.Unlock()
		return fmt.Errorf("session is closing")
	}
	if sess.running {
		sess.mu.Unlock()
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	cfg := sess.built.Config.Clone()
	sess.mu.Unlock()
	prov, model, err := cfg.Resolve(ref)
	if err != nil {
		return err
	}
	modelRef := config.ModelRef(model, prov.Name())
	overrides := cfg.ModelOverrides(modelRef)
	limits := config.ContextLimitsFor(prov, model, overrides.ContextWindow)
	numCtx := limits.ContextWindow
	compactionWindow := agent.EffectiveCompactionWindow(
		limits.ContextWindow, limits.InputLimit, limits.OutputLimit)
	levels := provider.ReasoningEffortLevelsForProvider(prov, model)
	// The name heuristic only recognizes known thinking families (gpt-oss,
	// qwen3, glm, ...). A model that advertises a "thinking" capability over
	// /api/show but whose family is not in that list — deepseek-v4, kimi,
	// minimax, nemotron — would be rejected here as "not supported" while the
	// client picker, which derives levels from capabilities, offered them. The
	// provider's level lookup handles that itself: on a heuristic miss it asks
	// /api/show under its own bounded, memoized request. A failed lookup
	// leaves the heuristic result (nil) in place, so behavior is unchanged
	// when the endpoint is unreachable. All of this runs before controlMu:
	// the candidate phase holds no lock another operation needs, so an
	// unresponsive endpoint delays one switch instead of stalling snapshots
	// (R2-13). Normalization keeps heuristic and metadata shapes identical.
	levels = provider.NormalizeReasoningEfforts(levels)
	levelNames := make([]string, 0, len(levels))
	for _, level := range levels {
		levelNames = append(levelNames, string(level))
	}
	effortKnown := len(levels) > 0 && provider.SupportsReasoningEffort(prov)
	if requested != "" && (!effortKnown || !containsReasoningEffort(levels, requested)) {
		return fmt.Errorf("reasoning effort %q is not supported by %s", requested, model)
	}

	// Commit phase. The levels stay as the candidate computed them: they
	// describe the model, not the provider instance re-resolved below.
	sess.controlMu.Lock()
	defer sess.controlMu.Unlock()
	sess.mu.Lock()
	if sess.closing || sess.running {
		sess.mu.Unlock()
		return fmt.Errorf("session became busy while switching models")
	}
	// Re-resolve against the live config so a credential or provider edit
	// that landed while the candidate was built is not reverted by the swap.
	// A different resolved target means the config moved mid-switch; failing
	// here is cheaper than installing a model nobody asked for.
	live := sess.built.Config.Clone()
	commitProv, commitModel, rerr := live.Resolve(ref)
	if rerr != nil {
		sess.mu.Unlock()
		return rerr
	}
	if config.ModelRef(commitModel, commitProv.Name()) != modelRef {
		sess.mu.Unlock()
		return fmt.Errorf("model configuration changed while switching; try again")
	}
	prov, model, cfg = commitProv, commitModel, live
	// The candidate's overrides and context window were computed off-lock. A
	// config edit that changed them during the candidate phase re-derives
	// them here; the context re-ask is bounded and only happens in that race.
	if liveOverrides := cfg.ModelOverrides(modelRef); liveOverrides != overrides {
		overrides = liveOverrides
		limits = config.ContextLimitsFor(prov, model, overrides.ContextWindow)
		numCtx = limits.ContextWindow
		compactionWindow = agent.EffectiveCompactionWindow(
			limits.ContextWindow, limits.InputLimit, limits.OutputLimit)
	}
	effort := provider.ReasoningEffort("")
	if effortKnown {
		effort = sess.built.Agent.ReasoningEffort()
		if requested != "" {
			effort = requested
		} else if saved := cfg.ReasoningEffortFor(modelRef); saved.Valid() && containsReasoningEffort(levels, saved) {
			effort = saved
		} else if !containsReasoningEffort(levels, effort) {
			effort = levels[0]
		}
		// Validate before anything is mutated: SetReasoningEffortQuiet after
		// the swap would return an error for a runtime that already switched
		// — exactly the half-committed state this transition exists to
		// prevent. containsReasoningEffort above makes this unreachable for
		// normalized levels; the check costs one parse.
		if _, ok := provider.ParseReasoningEffort(string(effort)); !ok {
			sess.mu.Unlock()
			return fmt.Errorf("reasoning effort %q is not supported by %s", effort, model)
		}
	}
	if err := sess.built.Store.WriteModel(modelRef); err != nil {
		sess.mu.Unlock()
		return err
	}
	vision := overrides.Vision
	sess.Model = model
	sess.built.Config = cfg
	sess.built.Agent.Provider = prov
	sess.built.Agent.Model = model
	sess.built.Agent.NumCtx = numCtx
	sess.built.Agent.CompactionWindow = compactionWindow
	sess.built.Agent.MaxSteps = cfg.Features.MaxSteps
	if sess.built.Agent.Compactor != nil {
		sess.built.Agent.Compactor.ContextWindow = sess.built.Agent.EffectiveCompactionWindow()
	}
	// Build wiring stamps these per-model overrides too; the old switch left
	// LenientToolParse describing the previous model (R2-13).
	sess.built.Agent.LenientToolParse = overrides.LenientToolParse
	if sess.built.FS != nil {
		sess.built.FS.WithAnchors(overrides.AnchorEdits).WithVision(vision)
	}
	// Semantic features keep the build-time embedding decision (R2-11): a
	// dedicated embedding model keeps its own backend and vector-space
	// identity across a chat switch, and only its absence re-couples the
	// embedder to the chat provider. The compactor gets the dedicated
	// backend or nothing — never the chat provider — matching wiring.
	if sess.built.Memory != nil {
		embedder, identity := prov, prov.Name()+"::embedding"
		if eref := cfg.Features.EmbeddingModel; eref != "" {
			if ep, _, rerr := cfg.Resolve(eref); rerr == nil {
				embedder, identity = ep, eref
			}
		}
		sess.built.Memory.Embedder = embedder
		sess.built.Memory.SetEmbeddingModel(identity)
	}
	if sess.built.Agent.Compactor != nil {
		if eref := cfg.Features.EmbeddingModel; eref != "" {
			ep, _, rerr := cfg.Resolve(eref)
			if rerr != nil {
				ep = nil
			}
			sess.built.Agent.Compactor.SetEmbeddingProvider(ep)
		}
	}
	sess.mu.Unlock()
	sess.refreshSystemPrompt()

	if effortKnown {
		if err := sess.built.Agent.SetReasoningEffortQuiet(effort); err != nil {
			return err
		}
	}
	var preferenceErr error
	if requested != "" {
		if err := config.SaveReasoningEffort(modelRef, requested); err != nil {
			preferenceErr = fmt.Errorf("could not remember reasoning effort: %w", err)
		} else {
			sess.mu.Lock()
			if sess.built.Config.ReasoningEfforts == nil {
				sess.built.Config.ReasoningEfforts = map[string]string{}
			}
			sess.built.Config.ReasoningEfforts[modelRef] = string(requested)
			sess.mu.Unlock()
			sess.srv.mu.Lock()
			if sess.srv.Cfg != nil {
				if sess.srv.Cfg.ReasoningEfforts == nil {
					sess.srv.Cfg.ReasoningEfforts = map[string]string{}
				}
				sess.srv.Cfg.ReasoningEfforts[modelRef] = string(requested)
			}
			sess.srv.mu.Unlock()
		}
	}
	sess.publishEvent(agent.Event{
		Kind:                 agent.EventModel,
		Model:                model,
		Provider:             prov.Name(),
		ReasoningEffort:      effort,
		ReasoningEfforts:     levelNames,
		ReasoningEffortKnown: effortKnown,
		Vision:               vision,
		VisionKnown:          true,
		ContextWindow:        numCtx,
		ContextWindowKnown:   true,
	})
	return preferenceErr
}

// SetReasoningEffort applies one session-wide effort setting. The agent emits
// the canonical event, so every attached client converges to the same value.
func (sess *Session) SetReasoningEffort(effort provider.ReasoningEffort) error {
	sess.controlMu.Lock()
	defer sess.controlMu.Unlock()
	sess.mu.Lock()
	prov := sess.built.Agent.Provider
	model := sess.built.Agent.Model
	sess.mu.Unlock()
	levels := provider.ReasoningEffortLevelsForProvider(prov, model)
	if len(levels) == 0 || !provider.SupportsReasoningEffort(prov) || !containsReasoningEffort(levels, effort) {
		return fmt.Errorf("reasoning effort %q is not supported by %s", effort, model)
	}
	if err := sess.built.Agent.SetReasoningEffort(effort); err != nil {
		return err
	}
	ref := config.ModelRef(model, prov.Name())
	if err := config.SaveReasoningEffort(ref, effort); err != nil {
		return fmt.Errorf("could not remember reasoning effort: %w", err)
	}
	sess.mu.Lock()
	if sess.built.Config.ReasoningEfforts == nil {
		sess.built.Config.ReasoningEfforts = map[string]string{}
	}
	sess.built.Config.ReasoningEfforts[ref] = string(effort)
	sess.mu.Unlock()
	sess.srv.mu.Lock()
	if sess.srv.Cfg != nil {
		if sess.srv.Cfg.ReasoningEfforts == nil {
			sess.srv.Cfg.ReasoningEfforts = map[string]string{}
		}
		sess.srv.Cfg.ReasoningEfforts[ref] = string(effort)
	}
	sess.srv.mu.Unlock()
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
		sess.requestHidden = item.hidden
		if len(sess.subs) == 0 && sess.idleSince.IsZero() {
			sess.idleSince = time.Now()
		}
	} else if len(sess.subs) == 0 && sess.idleSince.IsZero() && !sess.closing {
		// A defensive fallback for sessions assembled by older callers that did
		// not initialize idleSince before their first detached turn.
		sess.idleSince = time.Now()
	}
	sess.mu.Unlock()
	if next != nil {
		sess.launchTurn(next.text, next.images, next.hidden, ctx, cancel, done)
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

// writeServerFrame writes one server→client frame, bounded by
// MaxServerFrameBytes. Every frame is marshalled before it is written so an
// oversized payload is downgraded by fitServerFrame instead of reaching a
// client whose scanner must reject it and hang up (R2-01).
func writeServerFrame(conn net.Conn, msg ServerMsg) bool {
	msg, buf, ok := fitServerFrame(msg)
	if !ok {
		// Unreachable in practice: the last downgrade stage shrinks any real
		// event to its scalar shell. Dropping the frame keeps the connection
		// and the stream's sequence numbering intact, which is worth more
		// than delivering a payload nothing can absorb.
		return true
	}
	buf = append(buf, '\n')
	_, err := conn.Write(buf)
	return err == nil
}

// fitServerFrame marshals msg and degrades it when the frame would exceed
// MaxServerFrameBytes. Stages are ordered by how much a client depends on
// them: the post-turn history copy goes first (image bytes are already
// stripped at the source, so only pathological text histories reach here),
// then live image and display payloads, then the snapshot's message list,
// newest half at a time. What survives every downgrade carries the event's
// identity, so sequence numbering and turn lifecycle stay intact.
func fitServerFrame(msg ServerMsg) (ServerMsg, []byte, bool) {
	if buf, ok := marshalServerFrame(msg); ok {
		return msg, buf, true
	}
	if msg.Event != nil {
		e := *msg.Event
		msg.Event = &e
		if e.SnapshotMessages != nil {
			e.SnapshotMessages = nil
			e.SnapshotIncomplete = true
			if buf, ok := marshalServerFrame(msg); ok {
				return msg, buf, true
			}
		}
		if len(e.Images) > 0 {
			e.Images = nil
			if buf, ok := marshalServerFrame(msg); ok {
				return msg, buf, true
			}
		}
		e.Display = nil
		e.SnapshotMessages = nil
		e.SnapshotIncomplete = true
		const textHead = 32 << 10
		if len(e.Text) > textHead {
			e.Text = e.Text[:textHead] + "\n\n[truncated by the daemon to fit the client frame limit]"
		}
		buf, ok := marshalServerFrame(msg)
		return msg, buf, ok
	}
	if msg.Snapshot != nil {
		s := *msg.Snapshot
		msg.Snapshot = &s
		for len(s.Messages) > 1 {
			s.Messages = s.Messages[max(1, len(s.Messages)/2):]
			s.Truncated = true
			if buf, ok := marshalServerFrame(msg); ok {
				return msg, buf, true
			}
		}
		// A single message over the frame budget: deliver the envelope so the
		// client attaches and learns the session exists, without the history.
		if len(s.Messages) > 0 {
			s.Messages, s.Truncated = nil, true
			if buf, ok := marshalServerFrame(msg); ok {
				return msg, buf, true
			}
		}
	}
	if msg.Err != "" && len(msg.Err) > MaxServerFrameBytes/2 {
		msg.Err = msg.Err[:MaxServerFrameBytes/2] + "…[truncated]"
		if buf, ok := marshalServerFrame(msg); ok {
			return msg, buf, true
		}
	}
	return msg, nil, false
}

// marshalServerFrame encodes one frame with its scanner delimiter.
func marshalServerFrame(msg ServerMsg) ([]byte, bool) {
	buf, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	if len(buf) > MaxServerFrameBytes {
		return nil, false
	}
	return buf, true
}

// handle serves one client connection.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	s.touch()

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
				if !writeServerFrame(conn, msg) {
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
	// 8 MiB matches MaxClientFrameBytes; a frame of exactly that size plus its
	// newline would otherwise trip the scanner before the daemon could parse it.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
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
			// D6: "stopped" must mean shutdown completed, not scheduled. The
			// updater renames the executable the moment Stop returns; an old
			// daemon still running tools, writing state, or owning the socket
			// would race the replacement. Teardown runs synchronously here and
			// the connection is dropped only after it finishes — the client
			// reads that EOF as completion.
			s.Close()
			drop()
			return

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
			sess.InputRequestHidden(msg.RequestID, msg.Text, msg.Hidden, msg.Images)

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
			if err := sess.SetModelWithEffort(msg.Model, provider.ReasoningEffort(msg.ReasoningEffort)); err != nil {
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
			if err := sess.SetReasoningEffort(effort); err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
			}

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
	// D11: a scanner failure is a protocol error, not a clean hangup. An
	// oversized frame used to read as an unexplained daemon disconnect; name
	// it instead, and log the connection context for the server side.
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			send(ServerMsg{Kind: MsgError, Err: "frame exceeds the daemon's 8 MiB size limit"})
			log.Println("daemon: dropping connection:", conn.RemoteAddr(), "frame exceeds the size limit")
		} else {
			send(ServerMsg{Kind: MsgError, Err: "daemon read error: " + err.Error()})
			log.Println("daemon: connection failed:", conn.RemoteAddr(), err)
		}
	}
}
