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

// newEvent stamps the session and sequence number every event carries.
func (a *Agent) newEvent(kind EventKind) Event {
	a.seq++
	return Event{Kind: kind, Session: a.Session, Seq: a.seq}
}
