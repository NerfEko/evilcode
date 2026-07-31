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

	built, err := wiring.Build(s.Cfg, wiring.Options{Model: s.Model, Cwd: s.Cwd})
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
		subs:    map[chan ServerMsg]struct{}{},
	}

	s.mu.Lock()
	s.sessions[sess.Name] = sess
	s.mu.Unlock()

	go sess.pump()

	ctx, cancel := context.WithTimeout(context.Background(), WorkerTimeout)
	sess.cancel = cancel
	go func() {
		defer cancel()
		_ = sess.built.Agent.Run(ctx, WorkerPrompt(task, files, schema))
	}()
	return sess, nil
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
