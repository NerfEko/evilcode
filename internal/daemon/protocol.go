// Package daemon implements `evilcode serve` and its clients: one process
// holding N sessions, speaking NDJSON over a unix socket (plan.md §20).
//
// This is the payoff of invariant 1. The agent core emits a plain event stream
// that knows nothing about any frontend, so putting a socket in the middle is a
// serialization exercise rather than a rewrite: `attach` runs the same TUI
// against the same events, and the daemon is the only new machinery.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// SocketName is the daemon's socket under $XDG_RUNTIME_DIR.
const SocketName = "evilcode.sock"

// ProtocolVersion changes whenever the transport shape changes in a way that
// an older client cannot safely interpret.
const ProtocolVersion = 2

// SocketPath returns the socket path.
//
// $XDG_RUNTIME_DIR is the correct home for it: it is user-private, on tmpfs,
// and cleaned up at logout, which is exactly the lifetime of a daemon. Falling
// back to /tmp would put a world-readable path where a per-user one belongs, so
// the fallback is a user-owned directory under TMPDIR instead.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, SocketName)
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("evilcode-%d", os.Getuid()))
	return filepath.Join(dir, SocketName)
}

// CheckRuntimeDir verifies a directory is fit to hold the daemon's socket.
//
// MkdirAll(0700) creates a private directory; it does nothing at all to one
// that already exists. When $XDG_RUNTIME_DIR is unset the fallback path under
// TMPDIR is predictable, so an attacker who creates it first — world-writable,
// or as a symlink pointing somewhere they control — owns the directory the
// socket is bound in. The socket carries a live shell: anything that can
// connect runs commands as this user.
//
// Lstat rather than Stat, because a symlink is exactly the case being refused.
func CheckRuntimeDir(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		// Nothing there is fine: it will be created 0700, owned by us.
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to put the daemon socket there", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf(
			"%s is mode %v, writable or readable beyond its owner; "+
				"the daemon socket in it would be reachable by others", dir, perm)
	}
	if err := checkOwner(dir, info); err != nil {
		return err
	}
	return nil
}

// MaxSocketPath is the kernel's cap on a unix socket path.
//
// sockaddr_un.sun_path is 108 bytes on Linux, and exceeding it fails with a
// bare "invalid argument" from both bind and connect — an error that says
// nothing about the actual problem. Checking it here is the difference between
// a five-minute confusion and a one-line fix.
const MaxSocketPath = 107

// CheckSocketPath reports whether a path can be used as a unix socket.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf(
			"socket path is %d bytes, over the kernel's %d-byte limit: %s\n"+
				"pass a shorter -socket path, or set XDG_RUNTIME_DIR",
			len(path), MaxSocketPath, path)
	}
	return nil
}

// Client message kinds (client → server).
const (
	MsgAttach          = "attach"
	MsgInput           = "input"
	MsgInterrupt       = "interrupt"
	MsgReasoningEffort = "reasoning_effort"
	MsgSpawn           = "spawn"
	MsgList            = "list"
	MsgDetach          = "detach"
	MsgStatus          = "status"
	MsgStop            = "stop"
	MsgAnswer          = "answer"
	MsgModel           = "model"
	MsgCommand         = "command"
)

// ClientMsg is one frame from a client.
type ClientMsg struct {
	Version int    `json:"version,omitempty"`
	Kind    string `json:"kind"`

	// Session names the target. On attach an empty name creates a new session;
	// on every other kind it is required.
	Session string `json:"session,omitempty"`

	// Text is the prompt for input, or the interjection for interrupt.
	Text   string   `json:"text,omitempty"`
	Images [][]byte `json:"images,omitempty"`
	// Hidden marks a harness-authored prompt. It is still sent to the provider,
	// but attached clients must not render it as a user-typed message.
	Hidden bool `json:"hidden,omitempty"`

	// ReasoningEffort changes the effort used by the next provider request.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Urgent marks an interrupt that should land at the next safe point rather
	// than waiting for the turn to finish (plan.md §6.3).
	Urgent bool `json:"urgent,omitempty"`

	// Since replays the ring from this sequence number on attach. Zero means
	// "everything the ring still holds"; a reconnecting client passes the last
	// sequence it saw so it does not re-render the whole session.
	Since int `json:"since,omitempty"`

	// Task and Schema describe a spawn (plan.md §20). Schema is a JSON Schema
	// the worker's final output must validate against.
	Task   string          `json:"task,omitempty"`
	Files  []string        `json:"files,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`

	// Cwd and Model are used when attaching without an existing session. They
	// make creation independent of the daemon process directory.
	Cwd     string `json:"cwd,omitempty"`
	Model   string `json:"model,omitempty"`
	NoTools bool   `json:"no_tools,omitempty"`

	RequestID string   `json:"request_id,omitempty"`
	Answers   []string `json:"answers,omitempty"`
	Arg       string   `json:"arg,omitempty"`
	Secret    string   `json:"secret,omitempty"`
}

// Server message kinds (server → client).
const (
	MsgEvent    = "event"
	MsgSnapshot = "snapshot"
	MsgSessions = "sessions"
	MsgError    = "error"
)

// ServerMsg is one frame to a client.
type ServerMsg struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`

	// Event carries a fully serialized agent event, including display payloads
	// and image bytes. The client must not need to reconstruct tool state from
	// a side channel.
	Event *agent.Event `json:"event,omitempty"`

	// Snapshot is the attached session's state, sent once before replay.
	Snapshot *Snapshot `json:"snapshot,omitempty"`

	// Sessions answers a list.
	Sessions []SessionInfo `json:"sessions,omitempty"`

	// Err is set on MsgError. A protocol error names what the client asked for,
	// because the alternative is a silent no-op that looks like a hang.
	Err string `json:"error,omitempty"`

	Status *ServerStatus `json:"status,omitempty"`
}

// Snapshot is what a client needs to render a session it has just attached to.
type Snapshot struct {
	Session string `json:"session"`
	Model   string `json:"model"`
	// Provider is the configured provider instance behind the daemon session.
	// The client needs it to key persistent per-model preferences correctly;
	// "daemon" is only a transport label, not the model's provider identity.
	Provider string `json:"provider,omitempty"`
	Cwd      string `json:"cwd"`

	// ReasoningEffort is the session's current live effort setting.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Vision          bool   `json:"vision"`

	// ReasoningEfforts is the active model's ordered capability list. It lets an
	// attached TUI render the same provider-specific picker as a local client.
	ReasoningEfforts []string    `json:"reasoning_efforts,omitempty"`
	Skills           []string    `json:"skills,omitempty"`
	MCP              []MCPStatus `json:"mcp,omitempty"`

	// Running says whether a turn is in flight, so a client that attaches
	// mid-turn shows the spinner instead of an idle composer.
	Running bool `json:"running"`

	// Seq is the newest sequence number in the ring at snapshot time.
	Seq int `json:"seq"`

	// Epoch is the conversation rewrite generation, incremented by compact and
	// rewind. Attached /context mirrors it alongside the message count.
	Epoch int `json:"epoch"`

	// Messages is the conversation so far, which is how an attaching client
	// gets history without replaying every delta that produced it.
	Messages []Message `json:"messages,omitempty"`

	// Pending contains interactive requests that are waiting in the server,
	// including when no TUI was attached when they were created.
	Pending []agent.AskEvent `json:"pending,omitempty"`

	// Background contains detached shell tasks that outlive their originating
	// tool call and therefore need to be visible after a reconnect.
	Background []BackgroundTask `json:"background,omitempty"`
}

// MCPStatus describes a server connected by the daemon for this session.
type MCPStatus struct {
	Name  string `json:"name"`
	Tools int    `json:"tools"`
}

// BackgroundTask is the wire form of a detached shell task.
type BackgroundTask struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Done     bool   `json:"done"`
	Failed   bool   `json:"failed,omitempty"`
	Progress string `json:"progress,omitempty"`
}

// Message is one conversation entry in a snapshot. It is the provider message
// flattened to what a renderer needs, so the wire format does not have to track
// the provider package.
type Message struct {
	Role          string              `json:"role"`
	Content       string              `json:"content"`
	Reasoning     string              `json:"reasoning,omitempty"`
	ToolCalls     []provider.ToolCall `json:"tool_calls,omitempty"`
	ProviderItems []json.RawMessage   `json:"provider_items,omitempty"`
	ToolCallID    string              `json:"tool_call_id,omitempty"`
	ToolName      string              `json:"tool_name,omitempty"`
	IsError       bool                `json:"is_error,omitempty"`
	Held          bool                `json:"held,omitempty"`
	Images        [][]byte            `json:"images,omitempty"`
	Hidden        bool                `json:"hidden,omitempty"`
	Repairs       []string            `json:"repairs,omitempty"`
}

// SessionInfo is one row of a list response.
type SessionInfo struct {
	Name    string    `json:"name"`
	Model   string    `json:"model"`
	Running bool      `json:"running"`
	Clients int       `json:"clients"`
	Worker  bool      `json:"worker"`
	Started time.Time `json:"started"`
	Stale   bool      `json:"stale,omitempty"`

	// Task is a spawned worker's assignment, so `list` explains why a session
	// nobody started is running.
	Task string `json:"task,omitempty"`

	Cwd      string    `json:"cwd,omitempty"`
	Title    string    `json:"title,omitempty"`
	Modified time.Time `json:"modified,omitempty"`
	Crashed  bool      `json:"crashed,omitempty"`
	Stored   bool      `json:"stored,omitempty"`
	Live     bool      `json:"live,omitempty"`

	// Messages is the session's conversation length (excluding the system
	// message), so a roster can show how much a session has in it.
	Messages int `json:"messages,omitempty"`

	// Pending is the number of interactive asks the session is waiting on,
	// so a roster can flag a session that needs an answer (plan.md §20).
	Pending int `json:"pending,omitempty"`
}

// ServerStatus is the stable response used by lifecycle commands.
type ServerStatus struct {
	PID          int           `json:"pid"`
	Socket       string        `json:"socket"`
	Sessions     int           `json:"sessions"`
	Clients      int           `json:"clients"`
	Running      int           `json:"running"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
	LastActivity time.Time     `json:"last_activity"`
}
