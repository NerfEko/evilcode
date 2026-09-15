package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/tools"
)

// The swarm hub: the daemon's implementation of the coordination tools
// (plan.md §20). Everything is in-process maps and mutexes, because every agent
// in a swarm lives in this one process.

// MaxLiveWorkers is the default bound on how many workers run at once. The
// live value is [features] max_live_workers (zero means this default).
//
// Every auto-started loop needs a breaker (§12.6), and this is the one that
// matters most: a model that decides delegation is going well can spawn workers
// that spawn workers, and each of them costs real tokens against the same key.
const MaxLiveWorkers = 4

// MaxWorkersPerSession bounds one session's total spawns for the same reason,
// so a model cannot walk around the live cap by waiting for each worker to
// finish. It stays a constant: it is the anti-recursion breaker. Only live
// workers count against either cap — the root session is never charged.
const MaxWorkersPerSession = 12

// swarmState is the per-server coordination state.
type swarmState struct {
	mu sync.Mutex

	// spawnedBy maps a worker session to the session that summoned it, which is
	// where its result gets reported.
	spawnedBy map[string]string

	// schemas holds the JSON Schema a worker's final output must satisfy.
	schemas map[string]json.RawMessage

	// spawnCount is per-spawner, for the breaker. Cumulative on purpose: it
	// bounds one session's total spawns, so waiting for workers to finish does
	// not buy more of them.
	spawnCount map[string]int

	// live counts workers reserved and not yet finished. It is the admission
	// counter rather than a scan of the session map, because a scan cannot see
	// the workers other goroutines are in the middle of starting — which is
	// exactly the window concurrent spawns exploit.
	live int

	// tokens accumulates per-worker token usage by worker name (orchestrator
	// D7). Recorded at spawn registration (zero) and updated as usage events
	// land, so the roster can show what each worker spent. Display-only: the
	// model never sees it unless the parent asks.
	tokens map[string]int

	// inbox holds messages waiting for a session's next safe point.
	inbox map[string][]Message
}

func newSwarmState() *swarmState {
	return &swarmState{
		spawnedBy:  map[string]string{},
		schemas:    map[string]json.RawMessage{},
		spawnCount: map[string]int{},
		tokens:     map[string]int{},
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

// reserve admits one worker against both breakers, or refuses.
//
// Both counters move under one lock and before anything is built. Checking them
// and then acting on the answer was two operations: concurrent spawns all read
// the same count, all passed, and all started, so the breakers held only when
// nothing was racing them — which is not when a swarm needs them.
func (w *swarmState) reserve(spawner string, maxLive int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if maxLive <= 0 {
		maxLive = MaxLiveWorkers
	}
	if w.live >= maxLive {
		return fmt.Errorf(
			"%d workers are already running, which is the limit; wait for one to finish", w.live)
	}
	if used := w.spawnCount[spawner]; spawner != "" && used >= MaxWorkersPerSession {
		return fmt.Errorf(
			"this session has spawned %d workers, which is the limit; do the rest yourself", used)
	}
	w.live++
	if spawner != "" {
		w.spawnCount[spawner]++
	}
	return nil
}

// release returns a reservation whose worker never started.
func (w *swarmState) release(spawner string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live > 0 {
		w.live--
	}
	// spawnCount is cumulative, but a spawn that failed did not happen.
	if spawner != "" && w.spawnCount[spawner] > 0 {
		w.spawnCount[spawner]--
	}
}

// finished returns the live half of a reservation when a worker ends.
func (w *swarmState) finished() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live > 0 {
		w.live--
	}
}

// addTokens accumulates usage for a worker. The entry is created at spawn
// registration so a worker that never reports usage still reads zero.
func (w *swarmState) addTokens(worker string, n int) {
	if worker == "" || n <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tokens == nil {
		w.tokens = map[string]int{}
	}
	w.tokens[worker] += n
}

// workerTokens reads one worker's accumulated total.
func (w *swarmState) workerTokens(worker string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tokens[worker]
}

// SpawnFor starts an asynchronous worker on behalf of a session and records
// where its result should be reported. The model-facing tool uses
// SpawnForForeground below so delegation has the same blocking result semantics
// as OpenCode's task tool.
func (s *Server) SpawnFor(spawner, task string, files []string, schema json.RawMessage) (string, error) {
	return s.SpawnForWithModel(spawner, task, files, schema, "")
}

// SpawnForWithModel starts an asynchronous worker with a per-call model
// override (orchestrator D3). Model is a model@provider ref, or "" for the
// chain default (per-call → default_worker_model → session model).
func (s *Server) SpawnForWithModel(spawner, task string, files []string, schema json.RawMessage, model string) (string, error) {
	name, _, err := s.SpawnForWithSession(spawner, task, files, schema, model)
	return name, err
}

// SpawnForWithSession is the non-blocking path with its session handle: the
// caller gets the worker name immediately and the result arrives as a message
// (spawn_worker wait:false, /summon). The handle lets later phases roll up
// per-worker tokens without another lookup.
func (s *Server) SpawnForWithSession(spawner, task string, files []string, schema json.RawMessage, model string) (string, *Session, error) {
	return s.spawnForSession(spawner, task, files, schema, model, false)
}

// SpawnForForeground starts a worker on behalf of a session and waits for its
// validated result. Explicit asynchronous callers keep the old SpawnFor API;
// only the model-facing tool takes this path.
func (s *Server) SpawnForForeground(ctx context.Context, spawner, task string, files []string, schema json.RawMessage) (string, string, error) {
	return s.SpawnForForegroundWithModel(ctx, spawner, task, files, schema, "")
}

// SpawnForForegroundWithModel is the blocking path with a per-call model
// override (spawn_worker wait:true + model).
func (s *Server) SpawnForForegroundWithModel(ctx context.Context, spawner, task string, files []string, schema json.RawMessage, model string) (string, string, error) {
	name, output, _, err := s.SpawnForForegroundResult(ctx, spawner, task, files, schema, model)
	return name, output, err
}

// SpawnForForegroundResult waits for a worker and reports its outcome with
// status (orchestrator D4): complete, failed (never validated, or errored),
// or partial (timeout/cancel with salvaged text). A failed schema still
// returns its last text — the orchestrator gets the content AND the fact it
// was unvalidated, instead of nothing.
func (s *Server) SpawnForForegroundResult(ctx context.Context, spawner, task string, files []string, schema json.RawMessage, model string) (name, output, status string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	name, sess, err := s.spawnForSession(spawner, task, files, schema, model, true)
	if err != nil {
		return "", "", "", err
	}

	select {
	case <-sess.done:
	case <-ctx.Done():
		sess.cancelTurn()
		// A provider should honor cancellation promptly. Give the worker a
		// short cleanup window, but do not turn a cancelled parent turn into an
		// unbounded wait on a broken provider.
		select {
		case <-sess.done:
		case <-time.After(5 * time.Second):
		}
		return name, "", "", ctx.Err()
	}
	if err := sess.failure(); err != nil {
		return name, "", tools.StatusFailed, err
	}
	output = lastAssistantText(sess)
	status = tools.StatusComplete
	sess.mu.Lock()
	if sess.schemaFailed {
		status = tools.StatusFailed
	} else if sess.partial != "" {
		status = tools.StatusPartial
	}
	sess.mu.Unlock()
	if resolved := workerResolvedModel(sess); resolved != "" {
		output = fmt.Sprintf("[model %s]\n%s", resolved, output)
	}
	return name, output, status, nil
}

// effectiveMaxLiveWorkers resolves the live cap: [features]
// max_live_workers wins, zero means the MaxLiveWorkers default.
func (s *Server) effectiveMaxLiveWorkers() int {
	s.mu.Lock()
	cfg := s.Cfg
	s.mu.Unlock()
	if cfg != nil && cfg.Features.MaxLiveWorkers > 0 {
		return cfg.Features.MaxLiveWorkers
	}
	return MaxLiveWorkers
}

func (s *Server) spawnForSession(spawner, task string, files []string, schema json.RawMessage, model string, foreground bool) (string, *Session, error) {
	// A schema that does not compile is refused up front. Sending a worker off
	// with an unusable contract means discovering it only when the worker
	// finishes, after the tokens are already spent.
	if len(schema) > 0 {
		if _, err := compileSchema(schema); err != nil {
			return "", nil, fmt.Errorf("the result schema is not valid JSON Schema: %w", err)
		}
	}

	// D3: resolve the worker model before reserving a slot, so a bad ref fails
	// in milliseconds with a readable error the model can correct — before any
	// session exists and before any tokens are spent. Precedence: per-call
	// model → default_worker_model → the daemon session model. D3b: a
	// resolution failure fails the spawn; never silently retry on another
	// model, which would change worker capability without saying so.
	workerModel, resolvedRef, err := s.resolveWorkerModel(model)
	if err != nil {
		return "", nil, err
	}

	if err := s.swarm.reserve(spawner, s.effectiveMaxLiveWorkers()); err != nil {
		return "", nil, err
	}
	cwd := s.Cwd
	s.mu.Lock()
	if owner, ok := s.sessions[spawner]; ok && owner.Cwd != "" {
		cwd = owner.Cwd
	}
	s.mu.Unlock()

	sess, err := s.spawn(task, files, schema, workerModel, func(sess *Session) {
		sess.mu.Lock()
		sess.foreground = foreground
		sess.mu.Unlock()
		if resolvedRef != "" {
			sess.ResolvedModel = resolvedRef
		}
		s.swarm.mu.Lock()
		s.swarm.spawnedBy[sess.Name] = spawner
		if len(schema) > 0 {
			s.swarm.schemas[sess.Name] = schema
		}
		if s.swarm.tokens == nil {
			s.swarm.tokens = map[string]int{}
		}
		s.swarm.tokens[sess.Name] = 0
		s.swarm.mu.Unlock()
	}, cwd)
	if err != nil {
		// The worker was published but shutdown won the race with its first
		// turn: spawn returned a nil session, and markFinished already
		// returned its live slot, so the reservation is balanced — release
		// would double-count it. There is no name to report; the caller learns
		// the daemon is shutting down.
		if errors.Is(err, errPublishedWorker) {
			return "", nil, err
		}
		s.swarm.release(spawner)
		return "", nil, err
	}
	return sess.Name, sess, nil
}

// maxSchemaRetries bounds schema-fix attempts: the initial answer plus this
// many retries, 3 total prompts (orchestrator D4). A worker that cannot
// produce schema-valid output in 3 tries reports its last text as a failed
// result instead of nothing.
const maxSchemaRetries = 2

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

	// A cancelled worker never reports a completion: whichever path gets here
	// first salvages, the other stands down (partialDelivered).
	worker.mu.Lock()
	diverted := worker.partialDelivered
	cancelled := worker.cancelRequested
	worker.mu.Unlock()
	if diverted {
		return true
	}
	if cancelled {
		return s.settleCancel(worker)
	}

	output := lastAssistantText(worker)
	if len(raw) > 0 {
		validated, err := ValidateResult(output, raw)
		if err != nil {
			// Up to maxSchemaRetries retries, then report the failure with
			// the last text attached. Re-asking forever is the loop §12.6
			// exists to prevent.
			// Only worth asking again if the worker can still answer. Its Run
			// has usually already returned by the time this fires, and
			// interjecting into a session that will never take another turn
			// loses the result entirely — the spawner waits forever for a
			// message that cannot come.
			worker.mu.Lock()
			retries := worker.retries
			if retries < maxSchemaRetries {
				worker.retries++
			}
			worker.mu.Unlock()
			if retries < maxSchemaRetries {
				// Interjecting alone does nothing here: this runs at the turn's
				// end, so nothing would ever drain the queue. The retry has to
				// drive another loop itself — and exactly maxSchemaRetries of
				// them, which is what `retries` bounds (plan.md §12.6).
				worker.mu.Lock()
				worker.retrying = true
				originalDone := worker.turnDone
				worker.mu.Unlock()
				worker.built.Agent.Interject(agent.Interrupt{
					Source: agent.SourceSystem,
					Text: fmt.Sprintf("Your final message did not match the requested schema: %v\n"+
						"Reply with only the JSON, no prose and no code fence.", err),
				})
				go func() {
					// The original Run still owns the session reservation until its
					// deferred cleanup runs after TurnEnd is observed. Waiting here
					// prevents the retry from sharing the conversation with that tail.
					if originalDone != nil {
						<-originalDone
					}
					ctx, cancel := context.WithTimeout(context.Background(), WorkerTimeout)
					done, ok := worker.beginRetryTurn(cancel)
					if !ok {
						cancel()
						worker.mu.Lock()
						worker.retrying = false
						worker.mu.Unlock()
						worker.markFinished()
						return
					}
					err := func() error {
						defer close(done)
						defer worker.endTurn()
						defer cancel()
						return worker.built.Agent.Loop(ctx)
					}()
					worker.mu.Lock()
					worker.retrying = false
					worker.mu.Unlock()
					// The turn end is what normally finishes a worker. A retry
					// that could not run at all emits none, so it finishes here
					// rather than leaving a slot held for the daemon's life.
					if err != nil {
						worker.notifyWorkerFailure(err)
						worker.markFinished()
					}
				}()
				return false
			}
			// Retries spent: report the last text as a failed result, not an
			// error. The orchestrator gets the content AND the fact it was
			// unvalidated (D4) — a foreground waiter reads schemaFailed for
			// status, an async spawner reads the marker below.
			worker.mu.Lock()
			worker.schemaFailed = true
			foreground := worker.foreground
			worker.mu.Unlock()
			if !foreground {
				s.deliver(spawner, fmt.Sprintf(
					"⚠ worker %s finished but its result did not match the schema [status: %s]: %v\nIt said:\n%s",
					worker.Name, tools.StatusFailed, err, tools.Truncate(output)))
			}
			return true
		}
		output = validated
	}

	if !worker.isForeground() {
		// The batch aggregates per delivery: each wait:false result arrives
		// with its worker's model and token total, so N deliveries sum to the
		// batch cost without a cross-call accumulator in RunBatch.
		tokens := s.swarm.workerTokens(worker.Name)
		cost := ""
		if tokens > 0 {
			cost = fmt.Sprintf(" (%d tokens)", tokens)
		}
		if resolved := workerResolvedModel(worker); resolved != "" {
			s.deliver(spawner, fmt.Sprintf("✓ worker %s finished %q [model %s]%s:\n%s",
				worker.Name, worker.Task, resolved, cost, tools.Truncate(output)))
		} else {
			s.deliver(spawner, fmt.Sprintf("✓ worker %s finished %q%s:\n%s",
				worker.Name, worker.Task, cost, tools.Truncate(output)))
		}
	}
	return true
}

// settleCancel salvages a timed-out or cancelled worker exactly once and
// reports it, so the orchestrator gets partial output instead of nothing
// (orchestrator D6). Whichever path gets here first — the turn error or a
// turn-end race — reports; the other stands down via partialDelivered.
// Callers finish the worker (return true to observe, or markFinished).
func (s *Server) settleCancel(worker *Session) bool {
	text := lastAssistantText(worker)
	worker.mu.Lock()
	if worker.partialDelivered || worker.closedDone {
		worker.mu.Unlock()
		return false
	}
	worker.partialDelivered = true
	if text != "" {
		worker.partial = text
	}
	if text == "" && worker.workerFailure == nil {
		worker.workerFailure = fmt.Errorf("worker %s was stopped before producing output", worker.Name)
	}
	foreground := worker.foreground
	worker.mu.Unlock()

	if foreground {
		// The foreground waiter reads partial/workerFailure itself.
		return true
	}
	s.swarm.mu.Lock()
	spawner := s.swarm.spawnedBy[worker.Name]
	s.swarm.mu.Unlock()
	if spawner == "" {
		return true
	}
	if text != "" {
		s.deliver(spawner, fmt.Sprintf(
			"✓ worker %s stopped %q [status: %s]:\n%s",
			worker.Name, worker.Task, tools.StatusPartial, tools.Truncate(text)))
	} else {
		s.deliver(spawner, fmt.Sprintf(
			"⚠ worker %s was stopped before saying anything [status: %s]",
			worker.Name, tools.StatusFailed))
	}
	return true
}

// CancelWorker aborts a worker on behalf of its orchestrator (orchestrator
// D6): cancelTurn drops the in-flight provider request — the same context
// cancel the foreground-abandon path relies on, which subsumes the urgent
// next-safe-point plumbing — and the turn's unwind salvages partial output
// and releases the live slot. Cancelling a finished or unknown worker, or a
// session that is not a worker, is an error, not a silent no-op.
func (s *Server) CancelWorker(spawner, worker string) (string, error) {
	s.mu.Lock()
	sess := s.sessions[worker]
	s.mu.Unlock()
	if sess == nil {
		return "", fmt.Errorf("no worker named %q; use the peers list to see who is here", worker)
	}
	sess.mu.Lock()
	isWorker := sess.Worker
	finished := sess.closedDone
	sess.mu.Unlock()
	if !isWorker {
		return "", fmt.Errorf("%q is a session, not a worker", worker)
	}
	if finished || sess.finished() {
		return "", fmt.Errorf("worker %s already finished", worker)
	}
	sess.mu.Lock()
	sess.cancelRequested = true
	sess.mu.Unlock()
	sess.cancelTurn()
	return fmt.Sprintf("Worker %s cancellation requested. Its salvaged output will arrive as a message.", worker), nil
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
	s.refreshWorkerStaleness(time.Now())
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
			Stale:   info.Stale,
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
	return s.agentToolsForWorker(session, true)
}

// agentToolsForWorker builds the swarm tools with the depth gate applied:
// workers get messaging plus spawn only when worker_spawning allows depth 2+
// (orchestrator D8). Messaging stays in both cases — a worker that cannot
// reach its peers cannot coordinate at all.
func (s *Server) agentToolsForWorker(session string, allowSpawn bool) tools.Set {
	v := &agentView{srv: s, self: session}
	if !allowSpawn {
		return tools.NewMessaging(v)
	}
	return append(append(tools.NewMessaging(v), tools.NewSpawn(v)...), tools.NewCancel(v)...)
}

func (v *agentView) Self() string { return v.self }

func (v *agentView) SendMessage(to, text string) error {
	return v.srv.SendMessage(v.self, to, text)
}

func (v *agentView) Broadcast(text string) int {
	return v.srv.Broadcast(v.self, text)
}

func (v *agentView) Peers() []tools.Peer { return v.srv.Peers(v.self) }

func (v *agentView) SpawnWorker(task string, files []string, schema json.RawMessage, model string) (string, error) {
	return v.srv.SpawnForWithModel(v.self, task, files, schema, model)
}

func (v *agentView) SpawnWorkerForeground(ctx context.Context, task string, files []string, schema json.RawMessage, model string) (tools.SpawnResult, error) {
	name, output, status, err := v.srv.SpawnForForegroundResult(ctx, v.self, task, files, schema, model)
	if err != nil {
		return tools.SpawnResult{}, err
	}
	return tools.SpawnResult{Name: name, Output: output, Status: status}, nil
}

func (v *agentView) CancelWorker(worker string) (string, error) {
	return v.srv.CancelWorker(v.self, worker)
}

// resolveWorkerModel applies the D3 precedence chain: per-call model →
// default_worker_model → the daemon session model. It resolves the winning
// ref before any reservation, so a bad ref fails fast with a readable error
// and no session ever exists for it. It returns the ref to build with and
// the canonical resolved ref for audit.
func (s *Server) resolveWorkerModel(model string) (buildRef, resolvedRef string, err error) {
	ref := model
	if ref == "" && s.Cfg != nil {
		ref = s.Cfg.Features.DefaultWorkerModel
	}
	if ref == "" {
		ref = s.Model
	}
	if ref == "" || s.Cfg == nil {
		return "", "", nil
	}
	prov, modelName, resolveErr := s.Cfg.Resolve(ref)
	if resolveErr != nil {
		return "", "", fmt.Errorf("worker model %q: %w", ref, resolveErr)
	}
	return ref, config.ModelRef(modelName, prov.Name()), nil
}

// workerResolvedModel reads the audit ref without holding the swarm lock over
// the session map.
func workerResolvedModel(worker *Session) string {
	if worker == nil {
		return ""
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.ResolvedModel
}
