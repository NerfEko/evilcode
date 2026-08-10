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
)

// SocketName is the daemon's socket under $XDG_RUNTIME_DIR.
const SocketName = "evilcode.sock"

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
	MsgAttach    = "attach"
	MsgInput     = "input"
	MsgInterrupt = "interrupt"
	MsgSpawn     = "spawn"
	MsgList      = "list"
	MsgDetach    = "detach"
)

// ClientMsg is one frame from a client.
type ClientMsg struct {
	Kind string `json:"kind"`

	// Session names the target. On attach an empty name creates a new session;
	// on every other kind it is required.
	Session string `json:"session,omitempty"`

	// Text is the prompt for input, or the interjection for interrupt.
	Text string `json:"text,omitempty"`

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
	Kind string `json:"kind"`

	// Event carries a serialized agent event. Event.Display does not cross the
	// socket — it is an in-process render payload with no wire form — so a
	// remote client renders the plain row rather than the tool's rich tile.
	Event *agent.Event `json:"event,omitempty"`

	// Snapshot is the attached session's state, sent once before replay.
	Snapshot *Snapshot `json:"snapshot,omitempty"`

	// Sessions answers a list.
	Sessions []SessionInfo `json:"sessions,omitempty"`

	// Err is set on MsgError. A protocol error names what the client asked for,
	// because the alternative is a silent no-op that looks like a hang.
	Err string `json:"error,omitempty"`
}

// Snapshot is what a client needs to render a session it has just attached to.
type Snapshot struct {
	Session string `json:"session"`
	Model   string `json:"model"`
	Cwd     string `json:"cwd"`

	// Running says whether a turn is in flight, so a client that attaches
	// mid-turn shows the spinner instead of an idle composer.
	Running bool `json:"running"`

	// Seq is the newest sequence number in the ring at snapshot time.
	Seq int `json:"seq"`

	// Messages is the conversation so far, which is how an attaching client
	// gets history without replaying every delta that produced it.
	Messages []Message `json:"messages,omitempty"`
}

// Message is one conversation entry in a snapshot. It is the provider message
// flattened to what a renderer needs, so the wire format does not have to track
// the provider package.
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Repairs []string `json:"repairs,omitempty"`
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
}
