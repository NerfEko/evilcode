package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Access is one agent touching one file.
type Access struct {
	Session string
	Path    string
	Write   bool
	Turn    int
	At      time.Time
	Intent  string
	Preview string
}

// Conflict is what one agent needs told about another's edit.
type Conflict struct {
	// Session is who needs to hear about it.
	Session string

	// Other is who did the writing.
	Other string

	// Path is the display path — root-relative when a root is set, so a
	// notice reads as `internal/tui/app.go` rather than a sixty-character
	// absolute path. It is the form a person sees, not the identity key: two
	// agents that name the same file differently (relative vs absolute) must
	// still clear the same delivered entry on re-read.
	Path string

	// canonical is the normalized absolute path, used as the delivered-key
	// identity so Read's clearing loop — which also normalizes — matches what
	// Write stored. Display paths are for people; canonical paths are for the
	// map. Set by Write; empty for hand-built conflicts (tests, compaction).
	canonical string

	// ReadTurn is the turn the notified agent read the file on. It is used for
	// reader conflicts; writer conflicts use OtherTurn instead.
	ReadTurn int

	// WriterConflict means both sides wrote the same file. In that case the
	// notice speaks about overlapping writes rather than pretending the target
	// was merely a reader.
	WriterConflict bool
	OtherTurn      int

	// Intent and Preview describe the write the notified session needs to know
	// about. They are deliberately bounded before they reach this struct: a
	// conflict is useful only while it remains small.
	Intent  string
	Preview string
}

// Notice is the line delivered into the agent's conversation.
//
// It names the file, the writer, and when the reader last saw it, because an
// agent told only "a file changed" will either ignore it or re-read everything.
func (c Conflict) Notice() string {
	var b strings.Builder
	if c.WriterConflict {
		fmt.Fprintf(&b, "⚠ %s also modified %s at turn %d. "+
			"Your writes overlap; re-read it and coordinate before continuing.",
			c.Other, c.Path, c.OtherTurn)
	} else {
		fmt.Fprintf(&b, "⚠ %s modified %s which you read at turn %d. "+
			"Re-read it before you edit it — what you have is stale.",
			c.Other, c.Path, c.ReadTurn)
	}
	if c.Intent != "" {
		fmt.Fprintf(&b, " Intent: %s.", c.Intent)
	}
	if c.Preview != "" {
		b.WriteString("\nDiff preview:\n")
		b.WriteString(c.Preview)
	}
	return b.String()
}

// ConflictPreviewMaxLines and ConflictPreviewMaxBytes keep a file notice
// actionable without turning it into a second tool result. The line limit
// mirrors the compact preview used by the edit path in jcode.
const (
	ConflictPreviewMaxLines = 6
	ConflictPreviewMaxBytes = 240
)

// DiffPreview returns the first useful lines of a unified diff. It is safe to
// call on an already-previewed value, which lets tests and other event sources
// use the same bound as the daemon's write path.
func DiffPreview(diff string) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}

	lines := strings.Split(diff, "\n")
	truncated := false
	if len(lines) > ConflictPreviewMaxLines {
		lines = lines[:ConflictPreviewMaxLines]
		truncated = true
	}
	preview := strings.Join(lines, "\n")
	if len(preview) > ConflictPreviewMaxBytes {
		preview = truncateUTF8(preview, ConflictPreviewMaxBytes)
		truncated = true
	}
	if truncated {
		preview += "\n…"
	}
	return preview
}

func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
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

	// reads maps path → session → the last read turn and time. The time is
	// what lets a daemon forget an abandoned session's old view.
	reads map[string]map[string]readAccess

	// writes is the log of writes, used to decide what each reader has missed.
	writes []Access

	// root is the workspace, for relative display paths.
	root string

	// delivered records conflicts already handed to a session, so a reader that
	// does not re-read is not told the same thing every turn. Without it the
	// notice becomes noise and gets ignored, which is worse than silence.
	delivered map[string]time.Time

	// now is injectable for expiry tests. Production uses the wall clock.
	now func() time.Time
}

type readAccess struct {
	Turn int
	At   time.Time
}

// RegistryTouchExpiry bounds how long an old read or write can create a
// conflict. A daemon that lives for days should not retain the first turn's
// entire file history in memory.
const RegistryTouchExpiry = 30 * time.Minute

// RegistryWriteLogLimit bounds the retained write log even when a caller has a
// clock that does not advance (or when a burst arrives inside the expiry).
const RegistryWriteLogLimit = 1024

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		reads:     map[string]map[string]readAccess{},
		delivered: map[string]time.Time{},
		now:       time.Now,
	}
}

func (r *Registry) timeNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// expireLocked removes abandoned reads, old writes, and their delivery keys.
// Callers hold r.mu.
func (r *Registry) expireLocked(now time.Time) {
	cutoff := now.Add(-RegistryTouchExpiry)
	for path, readers := range r.reads {
		for session, access := range readers {
			if access.At.IsZero() || access.At.Before(cutoff) {
				delete(readers, session)
			}
		}
		if len(readers) == 0 {
			delete(r.reads, path)
		}
	}

	kept := r.writes[:0]
	for _, access := range r.writes {
		if !access.At.IsZero() && !access.At.Before(cutoff) {
			kept = append(kept, access)
		}
	}
	r.writes = kept
	if len(r.writes) > RegistryWriteLogLimit {
		r.writes = r.writes[len(r.writes)-RegistryWriteLogLimit:]
	}

	for key, at := range r.delivered {
		if at.Before(cutoff) {
			delete(r.delivered, key)
		}
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
	now := r.timeNow()
	r.expireLocked(now)
	if r.reads[path] == nil {
		r.reads[path] = map[string]readAccess{}
	}
	r.reads[path][session] = readAccess{Turn: turn, At: now}

	// Re-reading clears the slate for that file: the agent now has current
	// contents, so any conflict it was told about is resolved.
	for key := range r.delivered {
		if strings.HasPrefix(key, session+"\x00"+path+"\x00") {
			delete(r.delivered, key)
		}
	}
}

// Write records that a session wrote a file and returns the conflicts this
// creates. It is kept as the small API used by callers that have no event
// metadata; daemon event handling uses WriteWithDetails below.
func (r *Registry) Write(session, path string, turn int) []Conflict {
	return r.WriteWithDetails(session, path, turn, "", "")
}

// WriteWithDetails records a write and carries its human intent and a bounded
// diff preview into any conflict notices it creates.
func (r *Registry) WriteWithDetails(session, path string, turn int, intent, preview string) []Conflict {
	if session == "" || path == "" {
		return nil
	}
	path = normalize(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.timeNow()
	r.expireLocked(now)
	intent = strings.TrimSpace(intent)
	preview = DiffPreview(preview)

	// Keep only the latest write by each other session for the writer-to-writer
	// notice. A session that rewrites a file several times is still one peer to
	// coordinate with, and repeating every intermediate diff is noise.
	priorWriters := map[string]Access{}
	for _, access := range r.writes {
		if !access.Write || access.Path != path || access.Session == session {
			continue
		}
		if previous, ok := priorWriters[access.Session]; !ok || previous.At.Before(access.At) {
			priorWriters[access.Session] = access
		}
	}

	r.writes = append(r.writes, Access{
		Session: session, Path: path, Write: true, Turn: turn, At: now,
		Intent: intent, Preview: preview,
	})
	if len(r.writes) > RegistryWriteLogLimit {
		r.writes = r.writes[len(r.writes)-RegistryWriteLogLimit:]
	}

	var out []Conflict
	for reader, read := range r.reads[path] {
		// A session that writes what it read is not in conflict with itself.
		// When the reader is also a prior writer, the writer-specific notice is
		// more accurate and avoids two warnings for one overlapping change.
		if reader == session || priorWriters[reader].Session != "" {
			continue
		}
		out = append(out, Conflict{
			Session: reader, Other: session, Path: r.display(path), ReadTurn: read.Turn,
			Intent: intent, Preview: preview, canonical: path,
		})
	}
	for _, previous := range priorWriters {
		// Tell the earlier writer about the current writer, and the current
		// writer about the earlier one. Both sides need the same fact in the
		// terms of their own conversation.
		out = append(out,
			Conflict{
				Session: previous.Session, Other: session, Path: r.display(path),
				WriterConflict: true, OtherTurn: turn, Intent: intent, Preview: preview,
				canonical: path,
			},
			Conflict{
				Session: session, Other: previous.Session, Path: r.display(path),
				WriterConflict: true, OtherTurn: previous.Turn,
				Intent: previous.Intent, Preview: previous.Preview, canonical: path,
			},
		)
	}
	// Stable order so the same write always produces the same notices, which is
	// what makes a golden of a conflict frame possible at all.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		if out[i].Other != out[j].Other {
			return out[i].Other < out[j].Other
		}
		return out[i].WriterConflict && !out[j].WriterConflict
	})

	// The writer now holds current contents, so its own read state is current.
	if r.reads[path] == nil {
		r.reads[path] = map[string]readAccess{}
	}
	r.reads[path][session] = readAccess{Turn: turn, At: now}
	return out
}

// Pending returns the conflicts a session has not yet been told about, marking
// them delivered. It is called at safe point D (plan.md §6.3, §20) rather than
// the instant a write lands: interrupting an agent mid-tool-call with news
// about a file is how a coordination notice becomes a derailment.
func (r *Registry) Pending(session string, conflicts []Conflict) []Conflict {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.timeNow())

	var out []Conflict
	for _, c := range conflicts {
		if c.Session != session {
			continue
		}
		// Identity is the canonical path, not the display path: Read clears a
		// delivered entry by matching the normalized absolute path, and the
		// display path can be root-relative while the clearing key is absolute.
		// Keying on the display path leaves stale entries that a re-read never
		// clears, so a conflict fires once and never re-arms.
		ident := c.canonical
		if ident == "" {
			ident = c.Path
		}
		key := session + "\x00" + ident + "\x00" + c.Other
		if _, ok := r.delivered[key]; ok {
			continue
		}
		r.delivered[key] = r.timeNow()
		out = append(out, c)
	}
	return out
}

// Files lists every path a session has read, newest first. It is what the
// SwarmStatus widget and `/summon` use to say what an agent is working on.
func (r *Registry) Files(session string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.timeNow())
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
	notice := fmt.Sprintf("⚠ %s modified %d files you read (%s). "+
		"Re-read them before editing — what you have is stale.",
		strings.Join(names, " and "), len(paths), list)
	for _, c := range conflicts {
		if c.Intent == "" && c.Preview == "" {
			continue
		}
		detail := ""
		if c.Intent != "" {
			detail = "Intent: " + c.Intent
		}
		if c.Preview != "" {
			preview := strings.ReplaceAll(strings.TrimSpace(c.Preview), "\n", " / ")
			if detail != "" {
				detail += "; "
			}
			detail += "diff: " + preview
		}
		return notice + " " + detail
	}
	return notice
}

// WritesFiles reports whether a tool changes what it names.
//
// The list is explicit rather than inferred from the result carrying a diff:
// a write that produced no textual change is still a write, and a reader that
// is not told about it is working from a file that moved.
func WritesFiles(tool string) bool {
	switch tool {
	case "write", "edit", "multiedit":
		return true
	}
	return false
}

// ToolPath pulls the file a call names, or "" when it names none.
func ToolPath(tool string, args json.RawMessage) string {
	switch tool {
	case "read", "write", "edit", "multiedit":
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
