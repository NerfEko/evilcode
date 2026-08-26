package agent

import (
	"encoding/json"

	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
)

// EventKind tags an Event. Kinds are strings rather than an int enum so a
// serialized event stays readable on the daemon socket and in a session log.
type EventKind string

const (
	EventTurnStart       EventKind = "turn_start"
	EventTextDelta       EventKind = "text_delta"
	EventReasoningDelta  EventKind = "reasoning_delta"
	EventToolStart       EventKind = "tool_start"
	EventToolResult      EventKind = "tool_result"
	EventTokenUsage      EventKind = "token_usage"
	EventReasoningEffort EventKind = "reasoning_effort"
	EventNotice          EventKind = "notice"
	EventMemoryRecall    EventKind = "memory_recall"
	EventAsk             EventKind = "ask"
	EventAskResolved     EventKind = "ask_resolved"
	EventModel           EventKind = "model"
	EventBackground      EventKind = "background"
	EventSnapshot        EventKind = "snapshot"
	EventTurnEnd         EventKind = "turn_end"
	EventError           EventKind = "error"
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
	Kind      EventKind `json:"kind"`
	Session   string    `json:"session"`
	Seq       int       `json:"seq"`
	RequestID string    `json:"request_id,omitempty"`

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
	// Held marks a tool call stopped by a safety gate rather than a command
	// that ran and failed. Frontends use it for a distinct warning row.
	Held bool `json:"held,omitempty"`

	// Display is a tool-specific render payload, carried on the event so the
	// UI never has to read it from a side channel. It is deliberately part of
	// the wire contract: a remote client must not silently lose memory tiles,
	// todo deltas, or other rich tool output.
	Display any `json:"display,omitempty"`

	// Images are raw image bytes a tool result attached for the vision path.
	// JSON encodes []byte as base64, preserving the same renderable payload for
	// an attached client and a reconnecting client.
	Images [][]byte `json:"images,omitempty"`

	// NoWrite is set by a write-capable tool that did not actually write, so the
	// daemon's swarm coordination does not queue a stale-file notice for a file
	// that never changed (a fully-failed multiedit).
	NoWrite bool `json:"no_write,omitempty"`

	// Repairs names the argument rewrites RunOne applied (an aliased field, a
	// string-wrapped number coerced). Silent to the model, shown in the tool row
	// so a quietly rewritten argument is findable later (§1.4). Serialized so a
	// daemon-attached TUI and a replayed session show the same rows as local.
	Repairs []string `json:"repairs,omitempty"`

	// Err is the in-process error. ErrText carries it across a socket, where a
	// Go error cannot travel.
	Err     error  `json:"-"`
	ErrText string `json:"error,omitempty"`

	// Usage is set on TokenUsage.
	Usage *Usage `json:"usage,omitempty"`

	// Reason is set on TurnEnd.
	Reason EndReason `json:"reason,omitempty"`

	// Hidden marks a harness-authored turn start. It is explicit on the event
	// because an empty Text is also used for a prompt that was deliberately
	// withheld from every attached client.
	Hidden bool `json:"hidden,omitempty"`

	// ReasoningEffort is set when the live effort control changes.
	ReasoningEffort provider.ReasoningEffort `json:"reasoning_effort,omitempty"`

	// Model and Provider describe a server-owned model switch. They travel as
	// one event so every attached TUI updates the same live session state.
	Model                string   `json:"model,omitempty"`
	Provider             string   `json:"provider,omitempty"`
	ReasoningEfforts     []string `json:"reasoning_efforts,omitempty"`
	ReasoningEffortKnown bool     `json:"reasoning_effort_known,omitempty"`
	Vision               bool     `json:"vision,omitempty"`
	VisionKnown          bool     `json:"vision_known,omitempty"`

	// ContextWindow is the context window the model was resolved to, published
	// with EventModel so an attached TUI mirrors the daemon's window for the
	// context meter. ContextWindowKnown distinguishes a real zero (window not
	// discoverable) from an absent field on the wire.
	ContextWindow      int  `json:"context_window,omitempty"`
	ContextWindowKnown bool `json:"context_window_known,omitempty"`

	// Snapshot fields are used by an attached frontend when the daemon rewrites
	// history or renames a session. They travel through the same event queue as
	// ordinary UI updates so Bubble Tea, not a socket goroutine, owns model
	// mutation.
	SnapshotSession    string             `json:"snapshot_session,omitempty"`
	SnapshotModel      string             `json:"snapshot_model,omitempty"`
	SnapshotProvider   string             `json:"snapshot_provider,omitempty"`
	SnapshotRunning    bool               `json:"snapshot_running,omitempty"`
	SnapshotEpoch      int                `json:"snapshot_epoch,omitempty"`
	SnapshotMessages   []provider.Message `json:"snapshot_messages,omitempty"`
	SnapshotPending    []AskEvent         `json:"snapshot_pending,omitempty"`
	SnapshotBackground []BackgroundState  `json:"snapshot_background,omitempty"`

	// Ask carries a persisted interactive request owned by the server. It is
	// separate from Display because a disconnected client must be able to
	// reconstruct and answer the question later.
	Ask *AskEvent `json:"ask,omitempty"`

	// Background carries a detached shell task update owned by the daemon.
	Background *BackgroundState `json:"background,omitempty"`
}

// AskEvent is the transport-safe form of tools.AskRequest.
type AskEvent struct {
	ID       string            `json:"id"`
	Question string            `json:"question"`
	Options  []tools.AskOption `json:"options"`
	Multi    bool              `json:"multi,omitempty"`
}

// BackgroundState is the transport-safe state of one detached shell task.
// The daemon owns the process; clients only render this view.
type BackgroundState struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Done     bool   `json:"done"`
	Failed   bool   `json:"failed,omitempty"`
	Progress string `json:"progress,omitempty"`
}

// UnmarshalJSON restores the typed display payloads used by the TUI. A plain
// interface field would otherwise decode arrays into []any and objects into
// map[string]any, making a reconnect silently lose todo deltas and memory
// tiles even though the bytes crossed the socket successfully.
func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = Event(decoded)
	var raw struct {
		Display json.RawMessage `json:"display"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Display) == 0 || string(raw.Display) == "null" {
		return nil
	}
	if e.Kind == EventMemoryRecall {
		var hits []memory.Hit
		if err := json.Unmarshal(raw.Display, &hits); err == nil {
			e.Display = hits
		}
		return nil
	}
	if e.Kind == EventToolResult && e.Call != nil && e.Call.Name == "todo" {
		var delta todo.Delta
		if err := json.Unmarshal(raw.Display, &delta); err == nil {
			e.Display = delta
		}
		return nil
	}
	if e.Kind == EventToolResult && e.Call != nil && e.Call.Name == "recall" {
		var display tools.MemoryDisplay
		if err := json.Unmarshal(raw.Display, &display); err == nil {
			e.Display = display
		}
	}
	return nil
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
