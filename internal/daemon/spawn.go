package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"evilcode/internal/wiring"
)

// Spawn starts a headless worker session on a task.
//
// A worker is an ordinary daemon session with no client attached (plan.md §20),
// which is the whole trick: it needs no separate execution path, and attaching
// to one later to see what it is doing works for free.
func (s *Server) Spawn(task string, files []string, schema json.RawMessage) (*Session, error) {
	// Reserved here too, with no spawner to charge it to: a worker started
	// through this path is as live as any other, and a counter that cannot see
	// it is a counter that admits one worker too many.
	if err := s.swarm.reserve(""); err != nil {
		return nil, err
	}
	sess, err := s.spawn(task, files, schema, nil)
	if err != nil {
		s.swarm.release("")
	}
	return sess, err
}

// spawn builds a worker and runs it. register, if set, is called after the
// session exists but before its turn starts.
//
// The ordering matters and was wrong once: a mock worker finishes in
// microseconds, so recording who spawned it *after* starting it meant the turn
// could end before the spawner was known, and the result was dropped on the
// floor. Anything the report path needs has to be in place before the goroutine
// starts.
func (s *Server) spawn(task string, files []string, schema json.RawMessage, register func(*Session)) (*Session, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, fmt.Errorf("a worker needs a task")
	}

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("the daemon is shutting down")
	}

	todos, bank := s.shared()
	built, err := wiring.Build(s.Cfg, wiring.Options{
		Model: s.Model, Cwd: s.Cwd,
		TodoNamespace: SwarmTodoNamespace, Todos: todos, Bank: bank,
	})
	if err != nil {
		return nil, err
	}

	sess := &Session{
		Name:    built.Store.Name,
		Model:   built.Model,
		Task:    task,
		Worker:  true,
		Started: time.Now(),
		built:   built,
		ring:    NewRing(),
		srv:     s,
		done:    make(chan struct{}),
		subs:    map[chan ServerMsg]struct{}{},
	}

	// A worker can message and spawn like any other session. Bounded, though:
	// MaxLiveWorkers and MaxWorkersPerSession are what stop a worker that
	// decides delegation is going well from recursing (§12.6).
	sess.built.Agent.Tools = append(sess.built.Agent.Tools, s.AgentTools(sess.Name)...)

	s.mu.Lock()
	// Names collide: EVILCODE_DETERMINISTIC pins every new session to the same
	// one (invariant 5), and even without it the creature table is finite. An
	// unqualified insert here silently replaced the session a client was
	// attached to with a worker, which is how `/summon` reported summoning the
	// session that summoned it.
	sess.Name = s.uniqueName(sess.Name)
	s.sessions[sess.Name] = sess
	s.mu.Unlock()

	if register != nil {
		register(sess)
	}

	go sess.pump()

	ctx, cancel := context.WithTimeout(context.Background(), WorkerTimeout)
	done, ok := sess.beginTurn(cancel)
	if !ok {
		cancel()
		return nil, fmt.Errorf("the session is shutting down")
	}
	go func() {
		defer close(done)
		defer sess.endTurn()
		defer cancel()
		_ = sess.built.Agent.Run(ctx, WorkerPrompt(task, files, schema))
		// A worker that errors out never emits a turn end, so the breaker's
		// count would never come back down. Closing here too makes "the Run
		// returned" the definition of finished.
		sess.markFinished()
	}()
	return sess, nil
}

// uniqueName returns a name no live session holds. The caller must hold s.mu.
//
// The suffix rather than a rename of the file on disk: the JSONL session keeps
// the name it was created with, and only the daemon's live map needs the two to
// be distinguishable.
func (s *Server) uniqueName(name string) string {
	if _, taken := s.sessions[name]; !taken {
		return name
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", name, n)
		if _, taken := s.sessions[candidate]; !taken {
			return candidate
		}
	}
}

// WorkerTimeout bounds a spawned worker. Every auto-started loop needs a
// breaker (plan.md §12.6), and a worker nobody is watching is the one most
// likely to run until the machine notices.
const WorkerTimeout = 30 * time.Minute

// WorkerPrompt is the brief a spawned worker receives.
//
// The file hints are advisory rather than a confinement: they say where to look
// first, which saves a worker several searches, but a task that turns out to
// live elsewhere should still get done.
func WorkerPrompt(task string, files []string, schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString("You are a worker agent. Do this task and stop:\n\n")
	b.WriteString(task)
	b.WriteString("\n")

	if len(files) > 0 {
		b.WriteString("\nStart with these files; they are a hint, not a boundary:\n")
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
	}
	if len(schema) > 0 {
		// Prose parsing is exactly what §20 rules out: the spawner supplies a
		// schema and the worker's last message must validate against it.
		b.WriteString("\nYour final message must be JSON matching this schema, " +
			"and nothing else — no prose, no code fence:\n")
		b.Write(schema)
		b.WriteString("\n")
	}
	return b.String()
}
