package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/tools"
)

// The swarm hub: the daemon's implementation of the coordination tools
// (plan.md §20). Everything is in-process maps and mutexes, because every agent
// in a swarm lives in this one process.

// MaxLiveWorkers bounds how many workers can run at once.
//
// Every auto-started loop needs a breaker (§12.6), and this is the one that
// matters most: a model that decides delegation is going well can spawn workers
// that spawn workers, and each of them costs real tokens against the same key.
const MaxLiveWorkers = 4

// MaxWorkersPerSession bounds one session's total spawns for the same reason,
// so a model cannot walk around MaxLiveWorkers by waiting for each to finish.
const MaxWorkersPerSession = 12

// swarmState is the per-server coordination state.
type swarmState struct {
	mu sync.Mutex

	// spawnedBy maps a worker session to the session that summoned it, which is
	// where its result gets reported.
	spawnedBy map[string]string

	// schemas holds the JSON Schema a worker's final output must satisfy.
	schemas map[string]json.RawMessage

	// spawnCount is per-spawner, for the breaker.
	spawnCount map[string]int

	// inbox holds messages waiting for a session's next safe point.
	inbox map[string][]Message
}

func newSwarmState() *swarmState {
	return &swarmState{
		spawnedBy:  map[string]string{},
		schemas:    map[string]json.RawMessage{},
		spawnCount: map[string]int{},
		inbox:      map[string][]Message{},
	}
}

// liveWorkers counts workers that have not finished.
//
// Counting `finished` rather than `Running` on purpose: a worker's turn starts
// on a goroutine, so for the first instants after Spawn it is neither running
// nor done. Counting Running there lets a model spawn straight past the limit
// in one turn, which is the exact loop the breaker exists to stop.
func (s *Server) liveWorkers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.Worker && !sess.finished() {
			n++
		}
	}
	return n
}

// SpawnFor starts a worker on behalf of a session and records where its result
// should be reported.
func (s *Server) SpawnFor(spawner, task string, files []string, schema json.RawMessage) (string, error) {
	if n := s.liveWorkers(); n >= MaxLiveWorkers {
		return "", fmt.Errorf(
			"%d workers are already running, which is the limit; wait for one to finish", n)
	}

	s.swarm.mu.Lock()
	used := s.swarm.spawnCount[spawner]
	s.swarm.mu.Unlock()
	if used >= MaxWorkersPerSession {
		return "", fmt.Errorf(
			"this session has spawned %d workers, which is the limit; do the rest yourself", used)
	}

	// A schema that does not compile is refused up front. Sending a worker off
	// with an unusable contract means discovering it only when the worker
	// finishes, after the tokens are already spent.
	if len(schema) > 0 {
		if _, err := compileSchema(schema); err != nil {
			return "", fmt.Errorf("the result schema is not valid JSON Schema: %w", err)
		}
	}

	sess, err := s.spawn(task, files, schema, func(sess *Session) {
		s.swarm.mu.Lock()
		s.swarm.spawnedBy[sess.Name] = spawner
		s.swarm.spawnCount[spawner]++
		if len(schema) > 0 {
			s.swarm.schemas[sess.Name] = schema
		}
		s.swarm.mu.Unlock()
	})
	if err != nil {
		return "", err
	}
	return sess.Name, nil
}

// reportWorkerResult routes a finished worker's output back to its spawner.
//
// The result is validated against the spawner's schema before delivery, and a
// worker that answered in prose is sent back to try again rather than having
// its prose parsed — parsing prose is exactly what §20 rules out.
func (s *Server) reportWorkerResult(worker *Session) bool {
	s.swarm.mu.Lock()
	spawner := s.swarm.spawnedBy[worker.Name]
	raw := s.swarm.schemas[worker.Name]
	s.swarm.mu.Unlock()
	if spawner == "" {
		return true
	}

	output := lastAssistantText(worker)
	if len(raw) > 0 {
		validated, err := ValidateResult(output, raw)
		if err != nil {
			// One retry, then give up and report the failure. Re-asking forever
			// is the loop §12.6 exists to prevent.
			// Only worth asking again if the worker can still answer. Its Run
			// has usually already returned by the time this fires, and
			// interjecting into a session that will never take another turn
			// loses the result entirely — the spawner waits forever for a
			// message that cannot come.
			if !worker.retried {
				// Interjecting alone does nothing here: this runs at the turn's
				// end, so nothing would ever drain the queue. The retry has to
				// drive another loop itself — and exactly one, which is what
				// `retried` bounds (plan.md §12.6).
				worker.retried = true
				worker.built.Agent.Interject(agent.Interrupt{
					Source: agent.SourceSystem,
					Text: fmt.Sprintf("Your final message did not match the requested schema: %v\n"+
						"Reply with only the JSON, no prose and no code fence.", err),
				})
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), WorkerTimeout)
					defer cancel()
					_ = worker.built.Agent.Loop(ctx)
				}()
				return false
			}
			s.deliver(spawner, fmt.Sprintf(
				"⚠ worker %s finished but its result did not match the schema: %v\nIt said:\n%s",
				worker.Name, err, tools.Truncate(output)))
			return true
		}
		output = validated
	}

	s.deliver(spawner, fmt.Sprintf("✓ worker %s finished %q:\n%s",
		worker.Name, worker.Task, tools.Truncate(output)))
	return true
}

// lastAssistantText is the worker's final message.
func lastAssistantText(sess *Session) string {
	msgs := sess.built.Agent.Conv.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// deliver queues a message for a session, injecting it at its next safe point.
func (s *Server) deliver(session, text string) {
	s.mu.Lock()
	sess := s.sessions[session]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.built.Agent.Interject(agent.Interrupt{Source: agent.SourceSystem, Text: text})
}

// SendMessage routes a message from one agent to another.
func (s *Server) SendMessage(from, to, text string) error {
	s.mu.Lock()
	_, ok := s.sessions[to]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no agent named %q; use the peers list to see who is here", to)
	}
	s.deliver(to, fmt.Sprintf("✉ %s says: %s", from, text))
	return nil
}

// Broadcast sends to everyone but the sender, and reports how many heard it.
func (s *Server) Broadcast(from, text string) int {
	s.mu.Lock()
	names := make([]string, 0, len(s.sessions))
	for name := range s.sessions {
		if name != from {
			names = append(names, name)
		}
	}
	s.mu.Unlock()

	sort.Strings(names)
	for _, name := range names {
		s.deliver(name, fmt.Sprintf("📣 %s to everyone: %s", from, text))
	}
	return len(names)
}

// Peers describes the other agents in the swarm.
func (s *Server) Peers(self string) []tools.Peer {
	var out []tools.Peer
	for _, info := range s.Sessions() {
		if info.Name == self {
			continue
		}
		out = append(out, tools.Peer{
			Name:    info.Name,
			Task:    info.Task,
			Worker:  info.Worker,
			Running: info.Running,
			Files:   s.Files.Files(info.Name),
			Since:   time.Since(info.Started),
		})
	}
	return out
}

// agentView adapts the server to the coordination tools for one session.
//
// The tools take narrow interfaces rather than the server itself, so a session
// can be reachable without being able to spawn, and so `internal/tools` never
// learns what a daemon is.
type agentView struct {
	srv  *Server
	self string
}

// AgentTools builds the swarm tools for a session inside the daemon.
func (s *Server) AgentTools(session string) tools.Set {
	v := &agentView{srv: s, self: session}
	return append(tools.NewMessaging(v), tools.NewSpawn(v)...)
}

func (v *agentView) Self() string { return v.self }

func (v *agentView) SendMessage(to, text string) error {
	return v.srv.SendMessage(v.self, to, text)
}

func (v *agentView) Broadcast(text string) int {
	return v.srv.Broadcast(v.self, text)
}

func (v *agentView) Peers() []tools.Peer { return v.srv.Peers(v.self) }

func (v *agentView) SpawnWorker(task string, files []string, schema json.RawMessage) (string, error) {
	return v.srv.SpawnFor(v.self, task, files, schema)
}

// SwarmTodoNamespace is the todo store every session in one daemon shares.
//
// A shared namespace is what makes "the auth flow" mean the same group to a
// spawner and its worker (plan.md §20). Without it each agent keeps a private
// list that happens to use the same words, and a worker closing its group tells
// the spawner nothing.
const SwarmTodoNamespace = "swarm"
