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
	SpawnWorker(task string, files []string, schema json.RawMessage) (string, error)
}

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

This returns as soon as the worker starts. Its result arrives later as a message
— keep working, do not poll.

Supply result_schema and the worker's answer is validated against it before you
see it, so you can rely on its shape instead of parsing prose. Do that whenever
you intend to act on the answer rather than read it.

The worker shares your working directory. Handing it files you are mid-edit on
means you will both be told about the conflict, and someone still has to
untangle the edit.`

func spawnWorkerTool(s Spawner) Tool {
	return Tool{
		Name: "spawn_worker",
		Desc: spawnDesc,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "task": {"type": "string",
             "description": "The complete brief. The worker sees only this."},
    "files_hint": {"type": "array", "items": {"type": "string"},
                   "description": "Files to start from. A hint, not a boundary."},
    "result_schema": {"type": "object",
                      "description": "JSON Schema the worker's final answer must validate against."}
  },
  "required": ["task"]
}`),
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var args struct {
				Task         string          `json:"task"`
				FilesHint    []string        `json:"files_hint"`
				ResultSchema json.RawMessage `json:"result_schema"`
			}
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Task) == "" {
				return Result{}, fmt.Errorf("spawn_worker needs a task")
			}
			name, err := s.SpawnWorker(args.Task, args.FilesHint, args.ResultSchema)
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
