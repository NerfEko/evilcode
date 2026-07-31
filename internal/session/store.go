// Package session persists conversations as JSONL and names them after the
// creature tables (plan.md §18).
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"evilcode/internal/core"
	"evilcode/internal/provider"
)

// EntryType tags a JSONL envelope.
type EntryType string

const (
	TypeUser      EntryType = "user"
	TypeAssistant EntryType = "assistant"
	TypeTool      EntryType = "tool"
	TypeMeta      EntryType = "meta"
)

// Entry is one line of a session file. Resuming is replaying these in order.
type Entry struct {
	TS   time.Time       `json:"ts"`
	Type EntryType       `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Meta records things that are not messages: model switches, token totals,
// checkpoints, clean exit.
type Meta struct {
	Kind      string `json:"kind"`
	Model     string `json:"model,omitempty"`
	Name      string `json:"name,omitempty"`
	Note      string `json:"note,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
}

// Meta kinds.
const (
	MetaStart      = "start"
	MetaModel      = "model"
	MetaTokens     = "tokens"
	MetaCheckpoint = "checkpoint"
	MetaCleanExit  = "clean_exit"
	MetaTitle      = "title"
	MetaCompact    = "compact"
	MetaSaved      = "saved"
	MetaUnsaved    = "unsaved"
)

// Store appends session entries to a JSONL file.
type Store struct {
	Name string
	Path string

	mu   sync.Mutex
	file *os.File
	w    *bufio.Writer
}

// Dir returns the sessions directory under the data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, "sessions") }

// Open creates or appends to a named session.
func Open(dataDir, name string) (*Store, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Store{Name: name, Path: path, file: f, w: bufio.NewWriter(f)}, nil
}

// Create opens a new session with a generated creature name (plan.md §2.2).
// Under EVILCODE_DETERMINISTIC the name is fixed, which is what makes golden
// frames reproducible (invariant 5).
//
// The name is claimed with O_EXCL and a collision is retried. Listing the
// existing sessions and then opening the free name it found is two operations:
// two creators list together, both see the same name free, and both append to
// one log — two conversations interleaved in one file, each store believing it
// owns it. The daemon spawning workers makes that ordinary rather than exotic.
func Create(dataDir string) (*Store, error) {
	if os.Getenv("EVILCODE_DETERMINISTIC") == "1" {
		return CreateNamed(dataDir, "dracula")
	}
	taken := map[string]bool{}
	existing, _ := List(dataDir)
	for _, s := range existing {
		taken[s.Name] = true
	}
	// Bounded: the creature table is finite, and a run of collisions means the
	// table is full rather than that another attempt would help.
	for attempt := range 64 {
		name := core.PickName(core.Creatures,
			core.SeedFrom(fmt.Sprintf("%s/%d", time.Now(), attempt)), taken)
		st, err := CreateNamed(dataDir, name)
		if err == nil {
			return st, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		taken[name] = true
	}
	return nil, fmt.Errorf("no free session name after 64 attempts")
}

// PickFreeName proposes a session name nothing on disk holds.
//
// It claims nothing — CreateNamed does that, exclusively. This exists for the
// daemon, which has to settle a worker's name before it builds anything under
// it: a name allocated afterwards leaves the worker holding the log of whatever
// it collided with.
func PickFreeName(dataDir string) string {
	if os.Getenv("EVILCODE_DETERMINISTIC") == "1" {
		return "dracula"
	}
	existing, _ := List(dataDir)
	taken := make(map[string]bool, len(existing))
	for _, s := range existing {
		taken[s.Name] = true
	}
	return core.PickName(core.Creatures, core.SeedFrom(time.Now().String()), taken)
}

// CreateNamed claims one specific session name, failing if it is already taken.
//
// Under EVILCODE_DETERMINISTIC the name repeats by design, so an existing file
// is reopened rather than refused — goldens depend on the same session name
// every run, and refusing would break every replay.
func CreateNamed(dataDir, name string) (*Store, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".jsonl")

	flags := os.O_CREATE | os.O_EXCL | os.O_WRONLY | os.O_APPEND
	if os.Getenv("EVILCODE_DETERMINISTIC") == "1" {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	st := &Store{Name: name, Path: path, file: f, w: bufio.NewWriter(f)}
	cwd, _ := os.Getwd()
	if err := st.WriteMeta(Meta{Kind: MetaStart, Cwd: cwd}); err != nil {
		// A store returned alongside an error is a store nobody closes: the
		// caller takes the error path and the descriptor, and the claimed name,
		// stay held for the life of the process.
		f.Close()
		return nil, err
	}
	return st, nil
}

// Append writes one entry.
func (s *Store) Append(e Entry) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return err
	}
	// Flush per entry: a session that loses its tail on a crash is exactly the
	// session someone wants to resume.
	return s.w.Flush()
}

// WriteMessage records a conversation message.
func (s *Store) WriteMessage(m provider.Message) error {
	data, err := encodeMessage(s.Path, m)
	if err != nil {
		return err
	}
	var t EntryType
	switch m.Role {
	case provider.RoleUser:
		t = TypeUser
	case provider.RoleAssistant:
		t = TypeAssistant
	case provider.RoleTool:
		t = TypeTool
	default:
		t = TypeMeta
	}
	return s.Append(Entry{Type: t, Data: data})
}

// WriteMeta records a non-message event.
func (s *Store) WriteMeta(m Meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.Append(Entry{Type: TypeMeta, Data: data})
}

// Reopen points the store back at its path.
//
// Compact and Rewind replace the log with a temp file and a rename, which
// leaves this store's O_APPEND descriptor on the orphaned pre-rename inode:
// everything written afterwards goes to a file with no name and disappears
// when the descriptor closes. Reopen is how a store survives its own file
// being rewritten underneath it.
func (s *Store) Reopen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reopenLocked()
}

// reopenLocked swaps in a fresh descriptor with s.mu already held, so a rewrite
// can hold the lock across the whole read-write-rename-reopen sequence. Without
// that, an append landing between the rename and the reopen still reaches the
// orphaned inode — the window is narrow but it is a busy one, since a rewrite
// is proportional to the size of the log.
func (s *Store) reopenLocked() error {
	// Flush to the old inode first: whatever is still buffered was written
	// before the rewrite and belongs to the file that is now the backup.
	var flushErr error
	if s.w != nil {
		flushErr = s.w.Flush()
	}
	if s.file != nil {
		_ = s.file.Close()
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return errors.Join(flushErr, err)
	}
	s.file = f
	s.w = bufio.NewWriter(f)
	return flushErr
}

// Close flushes and marks a clean exit, which is how crash detection tells a
// killed session from a finished one (plan.md §18).
func (s *Store) Close() error {
	if err := s.WriteMeta(Meta{Kind: MetaCleanExit}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.file.Close()
}

// Info summarizes a session file for the resume list.
type Info struct {
	Name     string
	Path     string
	Emoji    string
	Messages int
	Modified time.Time
	Title    string

	// Crashed reports a session whose file has no clean-exit marker.
	Crashed bool

	// Saved marks a pinned session (📌 in the picker).
	Saved bool

	// Compactions counts how many times this session has been compacted, which
	// drives the picker's 📦 glyph (§5.4) and the status line's history warning
	// at three or more (§8.2).
	Compactions int

	// Cwd is where the session was started, so the picker can flag the ones
	// belonging to the directory you are in now.
	Cwd string
}

// List returns every stored session, most recently modified first.
func List(dataDir string) ([]Info, error) {
	dir := Dir(dataDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		info, err := Describe(dataDir, name)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Describe reads a session file's summary without loading its messages into
// the conversation.
func Describe(dataDir, name string) (Info, error) {
	path := filepath.Join(Dir(dataDir), name+".jsonl")
	stat, err := os.Stat(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Name:     name,
		Path:     path,
		Emoji:    core.CreatureEmoji(name),
		Modified: stat.ModTime(),
		Crashed:  true, // until a clean-exit marker says otherwise
	}

	entries, err := Read(path)
	if err != nil {
		return info, err
	}
	for _, e := range entries {
		switch e.Type {
		case TypeUser, TypeAssistant, TypeTool:
			info.Messages++
		case TypeMeta:
			var m Meta
			if json.Unmarshal(e.Data, &m) != nil {
				continue
			}
			switch m.Kind {
			case MetaCleanExit:
				info.Crashed = false
			case MetaTitle:
				info.Title = m.Note
			case MetaCompact:
				info.Compactions++
			case MetaStart:
				if m.Cwd != "" {
					info.Cwd = m.Cwd
				}
			case MetaSaved:
				info.Saved = true
			case MetaUnsaved:
				info.Saved = false
			}
		}
	}
	return info, nil
}

// Read parses a session file into entries. A truncated final line (the classic
// result of a hard kill) is skipped rather than failing the whole read — the
// point of resuming is to recover what survived.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Messages replays a session file into a message list.
func Messages(path string) ([]provider.Message, error) {
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}
	var out []provider.Message
	for _, e := range entries {
		if e.Type == TypeMeta {
			continue
		}
		m, err := decodeMessage(path, e.Data)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Resume loads a session for appending and returns its messages.
func Resume(dataDir, name string) (*Store, []provider.Message, error) {
	path := filepath.Join(Dir(dataDir), name+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("no session named %q", name)
	}
	msgs, err := Messages(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := Open(dataDir, name)
	if err != nil {
		return nil, nil, err
	}
	return st, msgs, nil
}

// Fork copies a session under a new name (plan.md §18).
func Fork(dataDir, from, to string) error {
	src := filepath.Join(Dir(dataDir), from+".jsonl")
	dst := filepath.Join(Dir(dataDir), to+".jsonl")
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("session %q already exists", to)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return copyBlobs(blobDir(src), blobDir(dst))
}

// copyBlobs duplicates a session's attachments alongside a fork. Content-
// addressed names mean copying is the whole job — no rewriting of references.
func copyBlobs(src, dst string) error {
	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
