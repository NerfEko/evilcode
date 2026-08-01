package agent

import (
	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// EventKind tags an Event. Kinds are strings rather than an int enum so a
// serialized event stays readable on the daemon socket and in a session log.
type EventKind string

const (
	EventTurnStart      EventKind = "turn_start"
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventToolStart      EventKind = "tool_start"
	EventToolResult     EventKind = "tool_result"
	EventTokenUsage     EventKind = "token_usage"
	EventNotice         EventKind = "notice"
	EventMemoryRecall   EventKind = "memory_recall"
	EventTurnEnd        EventKind = "turn_end"
	EventError          EventKind = "error"
)

// Level classifies a Notice.
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// EndReason says why a turn finished.
type EndReason string

const (
	EndComplete    EndReason = "complete"
	EndInterrupted EndReason = "interrupted"
	EndError       EndReason = "error"
	EndMaxSteps    EndReason = "max_steps"
)

// Usage is token accounting for the turn so far.
type Usage struct {
	In       int  `json:"in"`
	Out      int  `json:"out"`
	CtxUsed  int  `json:"ctx_used"`
	CtxMax   int  `json:"ctx_max"`
	CacheHit bool `json:"cache_hit"`

	// CacheRead is prompt tokens served from the provider's KV cache this
	// request; CacheWrite is the prefix written into it. Read+write is the
	// full prompt the cache saw, so the hit rate is read/(read+write). Both
	// are zero for providers that do not report caching (plan.md §16).
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`

	// GenMS is how long this request spent generating, in milliseconds.
	//
	// Reported per request because a turn is not one request: a turn with three
	// tool rounds makes four, and the gaps between them are tool execution.
	// Dividing tokens by wall-clock-since-turn-start counts that tool time as
	// generation and reports a rate far below the real one.
	GenMS int `json:"gen_ms,omitempty"`
}

// Event is the single contract between the agent core and every frontend: the
// TUI, `evilcode run`, and the daemon are three consumers of one stream. This
// is what makes headless mode, serve/attach, swarms, and the probe rig cheap
// (plan.md invariant 1) — and it is why this package must never import
// bubbletea.
type Event struct {
	Kind    EventKind `json:"kind"`
	Session string    `json:"session"`
	Seq     int       `json:"seq"`

	// Text carries a delta for the streaming kinds and the message for Notice.
	Text string `json:"text,omitempty"`

	// Level qualifies a Notice.
	Level Level `json:"level,omitempty"`

	// Call is set on ToolStart and ToolResult.
	Call *provider.ToolCall `json:"call,omitempty"`

	// Output, Err, Diff and DiffStat describe a finished tool call.
	Output   string          `json:"output,omitempty"`
	Diff     string          `json:"diff,omitempty"`
	DiffStat *tools.DiffStat `json:"diff_stat,omitempty"`
	Intent   string          `json:"intent,omitempty"`

	// Display is a tool-specific render payload, carried on the event so the
	// UI never has to read it from a side channel.
	Display any `json:"-"`

	// Images are raw image bytes a tool result attached for the vision path,
	// carried so the UI can render them inline. Not serialized: a remote
	// attach sees the text result and a placeholder, which is enough — the
	// bytes are display-only, the model already received them on the turn.
	Images [][]byte `json:"-"`

	// NoWrite is set by a write-capable tool that did not actually write, so the
	// daemon's swarm coordination does not queue a stale-file notice for a file
	// that never changed (a fully-failed multiedit).
	NoWrite bool `json:"-"`

	// Err is the in-process error. ErrText carries it across a socket, where a
	// Go error cannot travel.
	Err     error  `json:"-"`
	ErrText string `json:"error,omitempty"`

	// Usage is set on TokenUsage.
	Usage *Usage `json:"usage,omitempty"`

	// Reason is set on TurnEnd.
	Reason EndReason `json:"reason,omitempty"`
}

// IsError reports whether a tool-result event represents a failure.
func (e Event) IsError() bool { return e.Err != nil || e.ErrText != "" }

// ErrMessage is the reason, whichever field survived. An event that crossed the
// daemon socket has only ErrText: Err is an interface and does not serialize,
// so anything reading Err alone reports a remote failure as "<nil>".
func (e Event) ErrMessage() string {
	if e.ErrText != "" {
		return e.ErrText
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return ""
}

// newEvent stamps the session and sequence number every event carries.
//
// Atomic because emitters are not all on the turn's goroutine: the daemon's
// conflict delivery calls Notice from the pump while the turn emits from its
// own, and two events sharing a sequence number is a client reattaching and
// silently missing one — the sequence is how it works out what it missed.
func (a *Agent) newEvent(kind EventKind) Event {
	return Event{Kind: kind, Session: a.Session, Seq: int(a.seq.Add(1))}
}
