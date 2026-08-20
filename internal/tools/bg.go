package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type bgArgs struct {
	Op      string `json:"op"`
	ID      int    `json:"id,omitempty"`
	Lines   int    `json:"lines,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

func (e *Exec) bgTool() Tool {
	return Tool{
		Name: "bg",
		Desc: "Inspect and control commands started with bash background=true. Use list " +
			"when the task id is unknown, status for state, output or tail for output, " +
			"wait to block until completion, and cancel to stop it. Prefer wait over polling.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "op":      {"type": "string", "enum": ["list", "status", "output", "tail", "wait", "cancel"]},
    "id":      {"type": "integer", "description": "Background task id"},
    "lines":   {"type": "integer", "description": "Number of tail lines; defaults to 40"},
    "timeout": {"type": "integer", "description": "wait timeout in seconds; defaults to 120"}
  },
  "required": ["op"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args bgArgs
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if e.Bg == nil {
				return Result{}, fmt.Errorf("no background task registry is configured")
			}
			switch args.Op {
			case "list":
				return Result{Output: e.bgList(), Intent: "background tasks"}, nil
			case "status", "output", "tail", "wait", "cancel":
				if args.ID <= 0 {
					return Result{}, fmt.Errorf("bg %s needs a positive task id", args.Op)
				}
			default:
				return Result{}, fmt.Errorf("unknown bg op %q (want list, status, output, tail, wait, or cancel)", args.Op)
			}

			task, ok := e.Bg.Task(args.ID)
			if !ok {
				return Result{}, fmt.Errorf("background task %d was not found", args.ID)
			}
			task.refreshOutput()
			switch args.Op {
			case "status":
				return Result{Output: formatTaskStatus(task), Intent: fmt.Sprintf("task %d status", args.ID)}, nil
			case "output":
				_, failed, output := task.Snapshot()
				return Result{Output: output, Intent: fmt.Sprintf("task %d output", args.ID)}, taskResultError(args.ID, failed)
			case "tail":
				_, failed, output := task.Snapshot()
				lines := args.Lines
				if lines <= 0 {
					lines = 40
				}
				return Result{Output: tailLines(output, lines), Intent: fmt.Sprintf("task %d tail", args.ID)}, taskResultError(args.ID, failed)
			case "cancel":
				if err := e.Bg.Cancel(args.ID); err != nil {
					return Result{}, err
				}
				return Result{Output: fmt.Sprintf("cancellation requested for background task %d", args.ID), Intent: fmt.Sprintf("cancel task %d", args.ID)}, nil
			case "wait":
				waitCtx := ctx
				cancel := func() {}
				if args.Timeout > 0 {
					waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
				} else {
					waitCtx, cancel = context.WithTimeout(ctx, 120*time.Second)
				}
				defer cancel()
				finished, err := e.Bg.Wait(waitCtx, args.ID)
				if finished == nil {
					return Result{}, err
				}
				// A timeout returns the live task pointer. Refresh once after the
				// wait so output written during the blocked interval is visible in
				// the timeout preview instead of lagging by one poll.
				finished.refreshOutput()
				_, failed, output := finished.Snapshot()
				if err != nil {
					return Result{Output: fmt.Sprintf("background task %d is still running\n%s", args.ID, tailLines(output, 40)), Intent: fmt.Sprintf("wait task %d", args.ID)}, fmt.Errorf("background task %d did not finish: %w", args.ID, err)
				}
				result := Result{Output: fmt.Sprintf("background task %d finished\n%s", args.ID, output), Intent: fmt.Sprintf("task %d finished", args.ID)}
				return result, taskResultError(args.ID, failed)
			}
			return Result{}, nil
		},
	}
}

func (e *Exec) bgList() string {
	tasks := e.Bg.Tasks()
	if len(tasks) == 0 {
		return "no background tasks"
	}
	var b strings.Builder
	for _, task := range tasks {
		b.WriteString(formatTaskStatus(task))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatTaskStatus(task *BackgroundTask) string {
	done, failed, _ := task.Snapshot()
	state := "running"
	if done {
		state = "finished"
		if failed {
			state = "failed"
		}
	}
	p := task.Progress()
	progress := ""
	if p.Known {
		progress = " · " + p.String()
	}
	return fmt.Sprintf("%d [%s] %s%s", task.ID, state, task.Label, progress)
}

func taskResultError(id int, failed bool) error {
	if failed {
		return fmt.Errorf("background task %d failed", id)
	}
	return nil
}

func tailLines(output string, n int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= n {
		return output
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
