package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Access is one agent touching one file.
type Access struct {
	Session string
	Path    string
	Write   bool
	Turn    int
	At      time.Time
}

// Conflict is what one agent needs told about another's edit.
type Conflict struct {
	// Session is who needs to hear about it.
	Session string

	// Other is who did the writing.
	Other string

	Path string

	// ReadTurn is the turn the notified agent read the file on, which is the
	// detail that makes the notice actionable rather than alarming: it says how
	// stale what they are working from actually is.
	ReadTurn int
}

// Notice is the line delivered into the agent's conversation.
//
// It names the file, the writer, and when the reader last saw it, because an
// agent told only "a file changed" will either ignore it or re-read everything.
func (c Conflict) Notice() string {
	return fmt.Sprintf("⚠ %s modified %s which you read at turn %d. "+
		"Re-read it before you edit it — what you have is stale.",
		c.Other, c.Path, c.ReadTurn)
}

// Root is the workspace paths are reported relative to. Notices name
// `internal/tui/app.go`, not a sixty-character absolute path: the absolute form
// wraps across three lines in a transcript and tells the reader nothing they
// did not already know about where they are.
//
// Stored on the registry rather than passed to Notice because the registry is
// the only thing that knows both forms.
func (r *Registry) SetRoot(root string) {
	r.mu.Lock()
	r.root = root
	r.mu.Unlock()
}

// display shortens a path for a notice.
func (r *Registry) display(path string) string {
	if r.root == "" {
		return path
	}
	if rel, err := filepath.Rel(r.root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// Registry tracks which session read or wrote which file, so a swarm can tell
// an agent that the ground moved under it (plan.md §20).
//
// It is all in-process maps and a mutex, deliberately: swarm coordination lives
// inside the one daemon process, and a coordination layer that needs its own
// storage would be a second source of truth about what happened.
type Registry struct {
	mu sync.Mutex

	// reads maps path → session → the turn it was last read on.
	reads map[string]map[string]int

	// writes is the log of writes, used to decide what each reader has missed.
	writes []Access

	// root is the workspace, for relative display paths.
	root string

	// delivered records conflicts already handed to a session, so a reader that
	// does not re-read is not told the same thing every turn. Without it the
	// notice becomes noise and gets ignored, which is worse than silence.
	delivered map[string]bool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		reads:     map[string]map[string]int{},
		delivered: map[string]bool{},
	}
}

// normalize makes paths comparable. Two agents naming the same file relatively
// and absolutely must collide, or the registry silently never fires.
func normalize(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

// Read records that a session read a file on a turn.
func (r *Registry) Read(session, path string, turn int) {
	if session == "" || path == "" {
		return
	}
	path = normalize(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads[path] == nil {
		r.reads[path] = map[string]int{}
	}
	r.reads[path][session] = turn

	// Re-reading clears the slate for that file: the agent now has current
	// contents, so any conflict it was told about is resolved.
	for key := range r.delivered {
		if strings.HasPrefix(key, session+"\x00"+path+"\x00") {
			delete(r.delivered, key)
		}
	}
}

// Write records that a session wrote a file and returns the conflicts this
// creates — one per other session that had read it.
func (r *Registry) Write(session, path string, turn int) []Conflict {
	if session == "" || path == "" {
		return nil
	}
	path = normalize(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, Access{
		Session: session, Path: path, Write: true, Turn: turn, At: time.Now(),
	})

	var out []Conflict
	for reader, readTurn := range r.reads[path] {
		// A session that writes what it read is not in conflict with itself.
		if reader == session {
			continue
		}
		out = append(out, Conflict{
			Session: reader, Other: session, Path: r.display(path), ReadTurn: readTurn,
		})
	}
	// Stable order so the same write always produces the same notices, which is
	// what makes a golden of a conflict frame possible at all.
	sort.Slice(out, func(i, j int) bool { return out[i].Session < out[j].Session })

	// The writer now holds current contents, so its own read state is current.
	if r.reads[path] == nil {
		r.reads[path] = map[string]int{}
	}
	r.reads[path][session] = turn
	return out
}

// Pending returns the conflicts a session has not yet been told about, marking
// them delivered. It is called at safe point D (plan.md §6.3, §20) rather than
// the instant a write lands: interrupting an agent mid-tool-call with news
// about a file is how a coordination notice becomes a derailment.
func (r *Registry) Pending(session string, conflicts []Conflict) []Conflict {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Conflict
	for _, c := range conflicts {
		if c.Session != session {
			continue
		}
		key := session + "\x00" + c.Path + "\x00" + c.Other
		if r.delivered[key] {
			continue
		}
		r.delivered[key] = true
		out = append(out, c)
	}
	return out
}

// Files lists every path a session has read, newest first. It is what the
// SwarmStatus widget and `/summon` use to say what an agent is working on.
func (r *Registry) Files(session string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for path, readers := range r.reads {
		if _, ok := readers[session]; ok {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// CompactNotice folds several conflicts into one line.
//
// The toggle exists because a worker rewriting twenty files turns coordination
// into a wall of near-identical warnings, and an agent that scrolls past the
// wall has learned nothing (plan.md §20).
func CompactNotice(conflicts []Conflict) string {
	switch len(conflicts) {
	case 0:
		return ""
	case 1:
		return conflicts[0].Notice()
	}

	others := map[string]bool{}
	paths := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		others[c.Other] = true
		paths = append(paths, filepath.Base(c.Path))
	}

	names := make([]string, 0, len(others))
	for n := range others {
		names = append(names, n)
	}
	sort.Strings(names)
	sort.Strings(paths)

	const show = 4
	list := strings.Join(paths[:min(show, len(paths))], ", ")
	if len(paths) > show {
		list += fmt.Sprintf(", and %d more", len(paths)-show)
	}
	return fmt.Sprintf("⚠ %s modified %d files you read (%s). "+
		"Re-read them before editing — what you have is stale.",
		strings.Join(names, " and "), len(paths), list)
}

// WritesFiles reports whether a tool changes what it names.
//
// The list is explicit rather than inferred from the result carrying a diff:
// a write that produced no textual change is still a write, and a reader that
// is not told about it is working from a file that moved.
func WritesFiles(tool string) bool {
	switch tool {
	case "write", "edit":
		return true
	}
	return false
}

// ToolPath pulls the file a call names, or "" when it names none.
func ToolPath(tool string, args json.RawMessage) string {
	switch tool {
	case "read", "write", "edit":
	default:
		// grep, glob, bash and the rest touch files too, but not in a way that
		// can be attributed to one path — claiming otherwise would produce
		// conflict notices about files nobody edited.
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.Path)
}

// newRegistryAt builds a registry that reports paths relative to a workspace.
func newRegistryAt(root string) *Registry {
	r := NewRegistry()
	r.SetRoot(root)
	return r
}
