package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"evilcode/internal/provider"
)

// Checkpoint marks a point a session can be collapsed back to (plan.md §18).
type Checkpoint struct {
	// Name identifies the checkpoint.
	Name string

	// Entry is the index in the session's entry list where it was written.
	Entry int
}

// WriteCheckpoint records a named marker at the current point.
func (s *Store) WriteCheckpoint(name string) error {
	return s.WriteMeta(Meta{Kind: MetaCheckpoint, Name: name})
}

// Checkpoints lists a session's markers, oldest first.
func Checkpoints(path string) ([]Checkpoint, error) {
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}
	var out []Checkpoint
	for i, e := range entries {
		if e.Type != TypeMeta {
			continue
		}
		var m Meta
		if json.Unmarshal(e.Data, &m) != nil || m.Kind != MetaCheckpoint {
			continue
		}
		out = append(out, Checkpoint{Name: m.Name, Entry: i})
	}
	return out, nil
}

// RewindPoint is one entry a `/rewind` can return to.
type RewindPoint struct {
	// Index is the numbered position shown to the user, 1-based and counting
	// only user prompts — the only points anyone actually thinks in.
	Index int

	// Entry is the position in the raw entry list.
	Entry int

	// Prompt is the user message at this point.
	Prompt string
}

// RewindPoints lists the user prompts a session can be rewound to.
func RewindPoints(path string) ([]RewindPoint, error) {
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}
	var out []RewindPoint
	n := 0
	for i, e := range entries {
		if e.Type != TypeUser {
			continue
		}
		var m provider.Message
		if json.Unmarshal(e.Data, &m) != nil {
			continue
		}
		// Harness-authored continuations are not points a person would think
		// of rewinding to.
		if strings.HasPrefix(m.Content, "[automated ") {
			continue
		}
		n++
		out = append(out, RewindPoint{Index: n, Entry: i, Prompt: m.Content})
	}
	return out, nil
}

// backup copies a session log aside before it is destroyed.
//
// The copy is what makes a mistaken rewind or a bad compaction recoverable, so
// a rewrite that could not write it does not proceed: the failure was ignored,
// which meant the one case where the backup matters — the filesystem is in
// trouble — was exactly the case where it was silently absent.
//
// Written through a temp file and synced, so an interrupted backup cannot
// destroy the previous one and a rename that lands cannot precede its contents.
func backup(path string) error {
	data, err := readSessionFile(path)
	if err != nil {
		return err
	}
	return writeSessionFile(path+".bak", data)
}

// writeSessionFile replaces a session sidecar or log through a unique,
// no-follow temp file. Fixed .tmp names can be planted as symlinks and then
// followed by os.WriteFile; rename of the finished temp replaces that link
// instead of writing through it.
func writeSessionFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(FilePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	dirErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(dirErr, closeErr)
}

// Rewind truncates a session back to an entry index and returns the resulting
// messages.
//
// Durable state — todos, memories — deliberately survives: rewinding prunes
// exploratory *context*, not the work that was done (plan.md §18). The original
// file is kept alongside as `.bak` so a mistaken rewind is recoverable.
func Rewind(dataDir, name string, entryIndex int) ([]provider.Message, error) {
	path, err := pathFor(dataDir, name)
	if err != nil {
		return nil, err
	}
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}
	if entryIndex < 0 || entryIndex > len(entries) {
		return nil, fmt.Errorf("rewind point %d is out of range", entryIndex)
	}

	if err := backup(path); err != nil {
		return nil, err
	}

	kept := entries[:entryIndex]
	var b strings.Builder
	for _, e := range kept {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := writeSessionFile(path, []byte(b.String())); err != nil {
		return nil, err
	}
	return Messages(path)
}

// Rewind truncates the live session and reopens the store on the new file.
//
// Every caller holds an open store across the rewrite, so this is the form to
// reach for: the free function leaves the caller's descriptor on the orphaned
// inode.
func (s *Store) Rewind(dataDir string, entryIndex int) ([]provider.Message, error) {
	return s.rewrite(func() ([]provider.Message, error) {
		return Rewind(dataDir, s.Name, entryIndex)
	})
}

// rewrite runs a whole-file replacement with the store's lock held.
//
// The lock spans the rewrite, not just the reopen: an append arriving between
// the rename and the swap would otherwise still reach the orphaned inode, and a
// rewrite takes as long as the log is large. Holding it also means the buffer is
// flushed before the rewrite reads, so nothing in flight is silently dropped
// from the history it is about to summarize.
func (s *Store) rewrite(do func() ([]provider.Message, error)) ([]provider.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return nil, err
		}
	}
	msgs, err := do()
	if err != nil {
		return nil, err
	}
	return msgs, s.reopenLocked()
}

// CollapseSummary is the one-paragraph handoff injected after a rewind, so the
// model knows what happened in the pruned stretch rather than silently losing it.
func CollapseSummary(discarded []provider.Message) string {
	if len(discarded) == 0 {
		return ""
	}
	var prompts, tools int
	var lastAssistant string
	for _, m := range discarded {
		switch m.Role {
		case provider.RoleUser:
			if !strings.HasPrefix(m.Content, "[automated ") {
				prompts++
			}
		case provider.RoleTool:
			tools++
		case provider.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				lastAssistant = m.Content
			}
		}
	}

	var b strings.Builder
	b.WriteString("[rewound] The conversation was collapsed back to an earlier point. ")
	fmt.Fprintf(&b, "The removed stretch contained %d prompt(s) and %d tool call(s). ", prompts, tools)
	if lastAssistant != "" {
		summary := lastAssistant
		if runes := []rune(summary); len(runes) > 400 {
			summary = string(runes[:400]) + "…"
		}
		b.WriteString("Where it left off: " + summary + " ")
	}
	b.WriteString("Any files already changed are still changed, and todos and memories were kept.")
	return b.String()
}

// Save pins a session so the picker marks it and cleanup leaves it alone.
func Save(dataDir, name string, pinned bool) error {
	st, err := Open(dataDir, name)
	if err != nil {
		return err
	}
	// Not st.Close(): this is a side channel onto a log the real session may
	// still hold open, and Close's clean_exit marker would falsely mark a
	// live session as having exited cleanly (H5.11).
	kind := MetaSaved
	if !pinned {
		kind = MetaUnsaved
	}
	writeErr := st.WriteMeta(Meta{Kind: kind})
	return errors.Join(writeErr, st.closeFile())
}

// Rename moves a session's file, refusing to overwrite an existing one.
func Rename(dataDir, from, to string) error {
	// Rename's own check was the only validation in the package; it is
	// ValidName's job now, via pathFor, on every entry point rather than this
	// one. The space rule stays, because a session name with a space in it is
	// a nuisance in every command that takes one.
	if strings.ContainsAny(to, " \t") {
		return fmt.Errorf("session names must be a single filesystem-safe word")
	}
	src, err := pathFor(dataDir, from)
	if err != nil {
		return err
	}
	dst, err := pathFor(dataDir, to)
	if err != nil {
		return err
	}
	return moveSession(src, dst)
}

// Transfer compacts a session into a summary handoff in a fresh one, carrying
// the durable state across (plan.md §18).
func Transfer(dataDir, from, to, summary string) error {
	st, err := createExclusive(dataDir, to)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = st.Close()
		if !ok {
			_ = os.Remove(st.Path)
		}
	}()

	if err := st.WriteMeta(Meta{Kind: MetaStart, Note: "transferred from " + from}); err != nil {
		return err
	}
	if err := st.WriteMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: "[transferred] Continuing from session " + from + ".\n\n" + summary,
	}); err != nil {
		return err
	}
	ok = true
	return nil
}

func createExclusive(dataDir, name string) (*Store, error) {
	path, err := pathFor(dataDir, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(Dir(dataDir), DirPerm); err != nil {
		return nil, err
	}
	if err := os.Chmod(Dir(dataDir), DirPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, FilePerm)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(FilePerm); err != nil {
		return nil, errors.Join(err, f.Close(), os.Remove(path))
	}
	return &Store{Name: name, Path: path, file: f, w: bufio.NewWriter(f)}, nil
}

// DeriveTitle picks a session's display title.
//
// The order is deliberate: the group label of whatever is in progress, then the
// stated user intention, then the first todo. The list ends up labeled by what
// the agent understood you wanted, which is more useful than the first thing
// you happened to type (plan.md §5.4).
func DeriveTitle(activeGroup, userIntention, firstTodo, firstPrompt string) string {
	for _, candidate := range []string{activeGroup, userIntention, firstTodo, firstPrompt} {
		if s := strings.TrimSpace(candidate); s != "" {
			if len(s) > 60 {
				s = s[:59] + "…"
			}
			return strings.ReplaceAll(s, "\n", " ")
		}
	}
	return ""
}

// CompactedPrefix marks the synthetic message a compaction leaves behind, so a
// replayed session is visibly a summary rather than something the user typed.
const CompactedPrefix = "[conversation compacted]\n\n"

// Compact rewrites a session down to a summary message. It keeps the legacy
// summary-only shape for callers that do not have a live tail to preserve.
//
// This exists because `Conversation.Compact` only ever changed memory: it
// assigned the message slice directly, bypassing Append and the session sink,
// so the summary was never written and the pre-compaction messages stayed in the
// file. Resuming a compacted session replayed the whole uncompacted history and
// silently threw the summary away.
//
// Same atomic shape as Rewind — backup, temp file, rename — so an interrupted
// compaction leaves the previous log intact rather than a half-written one.
func Compact(dataDir, name, summary string) ([]provider.Message, error) {
	return CompactWithTail(dataDir, name, summary, nil)
}

// CompactWithTail rewrites a session to a summary followed by the exact recent
// messages that compaction kept in memory. The tail is written after the
// summary and before the compact marker, so resume reconstructs the same live
// suffix instead of silently losing the task that was in progress.
func CompactWithTail(dataDir, name, summary string, tail []provider.Message) ([]provider.Message, error) {
	path, err := pathFor(dataDir, name)
	if err != nil {
		return nil, err
	}
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}

	if err := backup(path); err != nil {
		return nil, err
	}

	var b strings.Builder
	write := func(e Entry) {
		if line, err := json.Marshal(e); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}

	// Keep the meta history: it carries the model, the cwd and the token totals,
	// and losing it would make a compacted session unresumable rather than
	// merely shorter.
	for _, e := range entries {
		if e.Type == TypeMeta {
			write(e)
		}
	}

	msg := provider.Message{Role: provider.RoleUser, Content: CompactedPrefix + summary}
	if data, err := json.Marshal(msg); err == nil {
		write(Entry{TS: time.Now(), Type: TypeUser, Data: data})
	}
	for _, kept := range tail {
		if kept.Role == provider.RoleSystem {
			continue
		}
		// Use the normal message encoder so preserved vision turns keep the
		// session's content-addressed blob format instead of expanding raw image
		// bytes inline during compaction.
		data, err := encodeMessage(path, kept)
		if err != nil {
			return nil, err
		}
		t := TypeUser
		switch kept.Role {
		case provider.RoleAssistant:
			t = TypeAssistant
		case provider.RoleTool:
			t = TypeTool
		}
		write(Entry{TS: time.Now(), Type: t, Data: data})
	}
	if data, err := json.Marshal(Meta{Kind: MetaCompact}); err == nil {
		write(Entry{TS: time.Now(), Type: TypeMeta, Data: data})
	}

	if err := writeSessionFile(path, []byte(b.String())); err != nil {
		return nil, err
	}
	return Messages(path)
}

// Compact rewrites the live session and reopens the store on the new file.
// See Store.Rewind for why the method exists.
func (s *Store) Compact(dataDir, summary string) ([]provider.Message, error) {
	return s.rewrite(func() ([]provider.Message, error) {
		return Compact(dataDir, s.Name, summary)
	})
}

// CompactWithTail rewrites the live session and preserves the recent messages
// that remain verbatim after the summary.
func (s *Store) CompactWithTail(dataDir, summary string, tail []provider.Message) ([]provider.Message, error) {
	return s.rewrite(func() ([]provider.Message, error) {
		return CompactWithTail(dataDir, s.Name, summary, tail)
	})
}
