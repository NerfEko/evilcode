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
	"sync"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/wiring"
)

// Server holds N sessions and serves clients over a unix socket.
type Server struct {
	Cfg  *config.Config
	Cwd  string
	Path string

	// Model is the default model reference for sessions this server creates.
	Model string

	mu       sync.Mutex
	sessions map[string]*Session
	listener net.Listener
	closed   bool
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

	mu   sync.Mutex
	subs map[chan ServerMsg]struct{}

	cancel context.CancelFunc
}

// NewServer builds a server. It does not listen until Serve is called.
func NewServer(cfg *config.Config, cwd, model string) *Server {
	return &Server{
		Cfg:      cfg,
		Cwd:      cwd,
		Model:    model,
		Path:     SocketPath(),
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
	built, err := wiring.Build(s.Cfg, wiring.Options{
		Model: s.Model, Resume: name, Cwd: s.Cwd, Extract: true,
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

	go sess.pump()
	return sess, nil
}

// pump moves the agent's events into the ring and out to every subscriber. It
// is the only reader of the agent's channel, which is what lets N clients
// watch one session.
func (sess *Session) pump() {
	for e := range sess.built.Agent.Events() {
		sess.ring.Add(e)
		sess.broadcast(ServerMsg{Kind: MsgEvent, Event: &e})
	}
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
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.built.Close()
}

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
	sess.cancel = cancel
	go func() {
		defer cancel()
		_ = a.Run(ctx, text)
	}()
}

// Interrupt injects a message into a live turn, or cancels it when there is no
// text to inject.
func (sess *Session) Interrupt(text string, urgent bool) {
	if text == "" {
		if sess.cancel != nil {
			sess.cancel()
		}
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
			worker, err := s.Spawn(msg.Task, msg.Files, msg.Schema)
			if err != nil {
				send(ServerMsg{Kind: MsgError, Err: err.Error()})
				continue
			}
			send(ServerMsg{Kind: MsgSessions, Sessions: []SessionInfo{{
				Name: worker.Name, Model: worker.Model, Worker: true,
				Task: worker.Task, Started: worker.Started, Running: true,
			}}})

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
