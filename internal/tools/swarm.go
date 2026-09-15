package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Spawner is what spawn_worker needs from the daemon.
//
// It is an interface so `internal/tools` never imports the daemon — the same
// separation that keeps the agent core free of the TUI, and the reason a
// swarm-less session can simply be handed no spawner at all.
type Spawner interface {
	// Self is the calling session's name, which is where the worker's result
	// gets reported.
	Self() string

	// SpawnWorker starts a headless worker and returns its session name.
	// Model is a model@provider ref, or "" for the D3 chain default
	// (per-call → default_worker_model → session model).
	SpawnWorker(task string, files []string, schema json.RawMessage, model string) (string, error)
}

// ForegroundSpawner is implemented by runtimes that can wait for a worker's
// answer. OpenCode's task tool is foreground by default: delegation is a tool
// call with a result, not an instruction for the parent to keep exploring in
// parallel. Keeping this as an optional interface preserves the small async
// Spawner contract for other runtimes and tests.
type ForegroundSpawner interface {
	Spawner

	// SpawnWorkerForeground starts a worker and waits for its finished result.
	// The name is the same stable worker name as SpawnWorker.
	SpawnWorkerForeground(ctx context.Context, task string, files []string, schema json.RawMessage, model string) (SpawnResult, error)
}

// SpawnResult is one finished worker: the validated answer plus the audit
// the orchestrator needs to trust it (orchestrator D3/D4).
type SpawnResult struct {
	// Name is the stable worker session name.
	Name string

	// Output is the worker's final text (validated when a schema was given).
	Output string

	// Status is complete, failed, or partial. Failed means the output never
	// validated (or the worker errored with salvageable text); partial means
	// a timeout or cancel cut the worker off and Output is what it had said
	// so far. Empty from runtimes that predate the contract reads as
	// complete.
	Status string
}

// Spawn outcome statuses. Every spawn_worker result carries one (D4): the
// orchestrator treats worker failure as data and re-spawns with accumulated
// knowledge, which needs partial output, not just an error.
const (
	StatusComplete = "complete"
	StatusFailed   = "failed"
	StatusPartial  = "partial"
)

// NewSpawn returns the spawn_worker tool (plan.md §20).
//
// Registered only inside the daemon: outside one there is nothing to spawn
// into, and a tool that is present and always fails is worse than absent.
func NewSpawn(s Spawner) Set {
	return Set{spawnWorkerTool(s)}
}

const spawnDesc = `Start a worker agent on a self-contained task.

Use this only when the task is genuinely separable — a survey of unfamiliar
code, a mechanical change across files you are not editing, an investigation
whose answer you need before continuing. Work that depends on what you are doing
right now belongs in this conversation, where you can see it.

The worker does NOT share your conversation. The task text is all it gets, so
write it as a complete brief.

In the daemon this call waits for the worker's validated answer and returns it
here. Continue only after you have that result; do not duplicate the worker's
task while it is running.

Supply result_schema and the worker's answer is validated against it before you
see it, so you can rely on its shape instead of parsing prose. Do that whenever
you intend to act on the answer rather than read it.

The worker shares your working directory. Handing it files you are mid-edit on
means you will both be told about the conflict, and someone still has to
untangle the edit.

Fan-out trigger: reach for this in parallel only when 3+ independent briefs
exist — batch them in one round and wait only for the batch. Load the
"orchestrate" skill for the decomposition and briefing playbook before
fanning out.`

func spawnWorkerTool(s Spawner) Tool {
	return Tool{
		Name: "spawn_worker",
		Desc: spawnDesc,
		// EffectSpawn is the fan-out guarantee (orchestrator D1): a maximal run
		// of consecutive spawns shares the bounded pool instead of running as
		// a barrier. Overlapping files_hint batches still serialize via D9.
		Effect: EffectSpawn,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "task": {"type": "string",
             "description": "The complete brief. The worker sees only this."},
    "files_hint": {"type": "array", "items": {"type": "string"},
                   "description": "Files to start from. A hint, not a boundary."},
    "result_schema": {"type": "object",
                      "description": "JSON Schema the worker's final answer must validate against."},
    "model": {"type": "string",
              "description": "Model for this worker as model@provider (default: the session model). A bad ref fails fast before any tokens are spent."},
    "wait": {"type": "boolean",
             "description": "Wait for the worker's validated result (default true). False returns immediately with the worker name; the result arrives as a message."}
  },
  "required": ["task"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args struct {
				Task         string          `json:"task"`
				FilesHint    []string        `json:"files_hint"`
				ResultSchema json.RawMessage `json:"result_schema"`
				Model        string          `json:"model"`
				Wait         *bool           `json:"wait"`
			}
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Task) == "" {
				return Result{}, fmt.Errorf("spawn_worker needs a task")
			}
			wait := true
			if args.Wait != nil {
				wait = *args.Wait
			}
			if wait {
				if foreground, ok := s.(ForegroundSpawner); ok {
					res, err := foreground.SpawnWorkerForeground(
						ctx, args.Task, args.FilesHint, args.ResultSchema, args.Model)
					if err != nil {
						return Result{}, err
					}
					status := res.Status
					if status == "" {
						status = StatusComplete
					}
					output := fmt.Sprintf("Worker %s completed:\n%s", res.Name, res.Output)
					if status != StatusComplete {
						output = fmt.Sprintf("Worker %s completed [status: %s]:\n%s", res.Name, status, res.Output)
					}
					return Result{
						Output: output,
						Intent: fmt.Sprintf("%s · %s", res.Name, shortTask(args.Task)),
						Status: status,
					}, nil
				}
			}

			name, err := s.SpawnWorker(args.Task, args.FilesHint, args.ResultSchema, args.Model)
			if err != nil {
				return Result{}, err
			}
			return Result{
				Output: fmt.Sprintf("Worker %s started. Its result will arrive as a message; "+
					"carry on with something else meanwhile.", name),
				Intent: fmt.Sprintf("%s · %s", name, shortTask(args.Task)),
			}, nil
		},
	}
}

// shortTask trims a brief to something a tool row can carry.
func shortTask(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 40
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:backToRuneBoundary(s, max)]) + "…"
}

// Canceller is what cancel_worker needs from the daemon: abort a worker by
// name. It is separate from Spawner so a runtime can offer one without the
// other, and so tools never imports the daemon.
type Canceller interface {
	// Self is the calling session's name, for attribution.
	Self() string

	// CancelWorker aborts the named worker's in-flight work, releases its
	// live slot, and reports what was salvaged.
	CancelWorker(worker string) (string, error)
}

// NewCancel returns the cancel_worker tool (orchestrator D6).
//
// Registered wherever spawn_worker is: an orchestrator that can start work
// can stop it. Undeclared Effect (the serialized default) on purpose —
// cancelling mid-batch must not race a spawn in the same round.
func NewCancel(c Canceller) Set {
	return Set{{
		Name: "cancel_worker",
		Desc: `Abort a worker you started, by the name spawn_worker returned.

Use this when the worker is stuck, redundant, or its brief was wrong. The
worker's live slot is released immediately and whatever it last said is
salvaged into the result that arrives as a message — a cancelled worker
reports partial output, not nothing. Cancelling a finished or unknown
worker is an error, not a silent no-op.`,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "worker": {"type": "string",
               "description": "The worker session name to abort."}
  },
  "required": ["worker"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args struct {
				Worker string `json:"worker"`
			}
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Worker) == "" {
				return Result{}, fmt.Errorf("cancel_worker needs a worker name")
			}
			summary, err := c.CancelWorker(strings.TrimSpace(args.Worker))
			if err != nil {
				return Result{}, err
			}
			return Result{
				Output: summary,
				Intent: "cancelled " + strings.TrimSpace(args.Worker),
			}, nil
		},
	}}
}
