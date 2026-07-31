package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/todo"
	"evilcode/internal/wiring"
)

// Server holds N sessions and serves clients over a unix socket.
type Server struct {
	Cfg  *config.Config
	Cwd  string
	Path string

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

	// done is closed when a worker's task ends. The spawn breaker counts what
	// is not yet closed, which is why it exists at all: a worker's turn starts
	// on a goroutine, so Running() is false for the first instants after Spawn.
	done       chan struct{}
	closedDone bool

	mu sync.Mutex

	// pending holds conflicts waiting for this session's next safe point. It
	// lives on the reader, not the writer: a conflict queued on the writer
	// would wait for the writer's safe point and reach the wrong conversation.
	// Written by whichever session did the writing, so it is under mu.
	pending []Conflict

	subs map[chan ServerMsg]struct{}

	cancel context.CancelFunc

	// turnDone is closed when the in-flight turn's goroutine returns. Closing a
	// session cancels the turn and then waits on this: a cancelled turn still
	// writes — the partial answer, the stubs for the tools it abandoned — and
	// closing the store first sends all of it nowhere.
	turnDone chan struct{}

	// closing refuses new turns once close has begun. Without it a turn
	// starting while close waits replaces the channel close is waiting on, and
	// the store shuts under a turn that is still running.
	closing bool
}

// NewServer builds a server. It does not listen until Serve is called.
func NewServer(cfg *config.Config, cwd, model string) *Server {
	return &Server{
		Cfg:      cfg,
		Cwd:      cwd,
		Model:    model,
		Path:     SocketPath(),
		Files:    newRegistryAt(cwd),
		swarm:    newSwarmState(),
		sessions: map[string]*Session{},
	}
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
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	if conn, err := net.Dial("unix", s.Path); err == nil {
		conn.Close()
		return fmt.Errorf("a daemon is already listening on %s", s.Path)
	}
	os.Remove(s.Path)

	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		return err
	}
	// The socket carries a live shell: anything that can connect can run
	// commands as this user. Owner-only is not defense in depth here, it is the
	// whole access control.
	if err := os.Chmod(s.Path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.listener = ln
	return nil
}

// Serve accepts clients until the context is cancelled or Close is called.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
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

	if ln != nil {
		ln.Close()
	}
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
	defer s.mu.Unlock()

	out := make([]SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sess.mu.Lock()
		clients := len(sess.subs)
		sess.mu.Unlock()
		out = append(out, SessionInfo{
			Name:    sess.Name,
			Model:   sess.Model,
			Running: sess.built.Agent.Running(),
			Clients: clients,
			Worker:  sess.Worker,
			Task:    sess.Task,
			Started: sess.Started,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
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

// Open returns a session, building it if the name is new.
//
// An empty name creates a fresh session, which is what a client attaching
// without arguments wants.
func (s *Server) Open(name string) (*Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("the daemon is shutting down")
	}
	if name != "" {
		if sess, ok := s.sessions[name]; ok {
			s.mu.Unlock()
			return sess, nil
		}
	}
	s.mu.Unlock()

	// Built outside the lock: resolving a model can touch the network, and
	// holding the server lock across that would stall `list` for every client.
	todos, bank := s.shared()
	built, err := wiring.Build(s.Cfg, wiring.Options{
		Model: s.Model, Resume: name, Cwd: s.Cwd, Extract: true,
		TodoNamespace: SwarmTodoNamespace, Todos: todos, Bank: bank,
	})
	if err != nil {
		return nil, err
	}

	sess := &Session{
		Name:    built.Store.Name,
		Model:   built.Model,
		Started: time.Now(),
		built:   built,
		ring:    NewRing(),
		srv:     s,
		subs:    map[chan ServerMsg]struct{}{},
	}

	s.mu.Lock()
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
		sess.ring.Add(e)
		sess.broadcast(ServerMsg{Kind: MsgEvent, Event: &e})
	}
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
	if sess.srv == nil || sess.srv.Files == nil {
		return
	}
	sess.mu.Lock()
	if sess.turn == 0 {
		sess.turn = 1
	}
	turn := sess.turn
	sess.mu.Unlock()
	switch e.Kind {
	case agent.EventToolResult:
		if e.Call == nil || e.IsError() {
			return
		}
		path := ToolPath(e.Call.Name, e.Call.Args)
		if path == "" {
			return
		}
		if WritesFiles(e.Call.Name) {
			// Queued on the *readers*, not on the writer. Keeping them here was
			// the bug: the writer then filtered out every conflict as belonging
			// to someone else and dropped it, so nobody was ever told.
			sess.srv.queueConflicts(sess.srv.Files.Write(sess.Name, path, turn))
			return
		}
		sess.srv.Files.Read(sess.Name, path, turn)

	case agent.EventTurnEnd:
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
			if sess.srv.reportWorkerResult(sess) {
				sess.markFinished()
			}
		}
	}
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
	defer sess.mu.Unlock()
	if sess.done != nil && !sess.closedDone {
		sess.closedDone = true
		close(sess.done)
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
	sess.built.Close()
}

// turnUnwindTimeout bounds how long closing a session waits for a cancelled
// turn to finish writing.
const turnUnwindTimeout = 5 * time.Second

// snapshot describes the session to a client that has just attached.
func (sess *Session) snapshot(cwd string) *Snapshot {
	msgs := sess.built.Agent.Conv.Messages()
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == provider.RoleSystem || m.Content == "" {
			continue
		}
		out = append(out, Message{Role: string(m.Role), Content: m.Content})
	}
	return &Snapshot{
		Session:  sess.Name,
		Model:    sess.Model,
		Cwd:      cwd,
		Running:  sess.built.Agent.Running(),
		Seq:      sess.ring.Seq(),
		Messages: out,
	}
}

// Input starts a turn. A turn already in flight is interjected into instead,
// which is what makes two attached clients usable at once rather than a race.
func (sess *Session) Input(text string) {
	a := sess.built.Agent
	if a.Running() {
		a.Interject(agent.Interrupt{Source: agent.SourceUser, Text: text})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, ok := sess.beginTurn(cancel)
	if !ok {
		cancel()
		return
	}
	go func() {
		defer close(done)
		defer cancel()
		_ = a.Run(ctx, text)
	}()
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
	if sess.closing {
		return nil, false
	}
	// Cancel whatever this replaces. Normally already finished — Input checks
	// Running first — and cancelling a finished turn only releases its context.
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.cancel, sess.turnDone = cancel, done
	return done, true
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

	enc := json.NewEncoder(conn)
	var (
		sess *Session
		sub  chan ServerMsg
		done = make(chan struct{})
	)
	defer func() {
		close(done)
		if sess != nil && sub != nil {
			sess.unsubscribe(sub)
		}
	}()

	// One writer goroutine owns the connection, so a broadcast and a reply can
	// never interleave halfway through a JSON frame.
	out := make(chan ServerMsg, 256)
	go func() {
		for {
			select {
			case msg := <-out:
				if err := enc.Encode(msg); err != nil {
					return
				}
			case <-done:
				return
			case <-ctx.Done():
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
		var msg ClientMsg
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			send(ServerMsg{Kind: MsgError, Err: "malformed frame: " + err.Error()})
			continue
		}

		switch msg.Kind {
		case MsgList:
			send(ServerMsg{Kind: MsgSessions, Sessions: s.Sessions()})

		case MsgAttach:
			opened, err := s.Open(msg.Session)
			if err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
				continue
			}
			if sess != nil && sub != nil {
				sess.unsubscribe(sub)
			}
			sess = opened
			// Subscribe before replaying, so an event arriving mid-replay is
			// queued rather than dropped into the gap between the two.
			sub = sess.subscribe()
			send(ServerMsg{Kind: MsgSnapshot, Snapshot: sess.snapshot(s.Cwd)})
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
			go func(sess *Session, sub chan ServerMsg) {
				for {
					select {
					case msg := <-sub:
						send(msg)
					case <-done:
						return
					}
				}
			}(sess, sub)

		case MsgInput:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "input before attach"})
				continue
			}
			sess.Input(msg.Text)

		case MsgInterrupt:
			if sess == nil {
				send(ServerMsg{Kind: MsgError, Err: "interrupt before attach"})
				continue
			}
			sess.Interrupt(msg.Text, msg.Urgent)

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
			if sess != nil && sub != nil {
				sess.unsubscribe(sub)
				sess, sub = nil, nil
			}

		default:
			send(ServerMsg{Kind: MsgError, Err: "unknown kind " + msg.Kind})
		}
	}
}
