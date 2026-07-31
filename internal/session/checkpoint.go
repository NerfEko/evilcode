package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Rewind truncates a session back to an entry index and returns the resulting
// messages.
//
// Durable state — todos, memories — deliberately survives: rewinding prunes
// exploratory *context*, not the work that was done (plan.md §18). The original
// file is kept alongside as `.bak` so a mistaken rewind is recoverable.
func Rewind(dataDir, name string, entryIndex int) ([]provider.Message, error) {
	path := filepath.Join(Dir(dataDir), name+".jsonl")
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}
	if entryIndex < 0 || entryIndex > len(entries) {
		return nil, fmt.Errorf("rewind point %d is out of range", entryIndex)
	}

	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", data, 0o644)
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
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
		if len(summary) > 400 {
			summary = summary[:400] + "…"
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
	defer st.Close()
	kind := MetaSaved
	if !pinned {
		kind = MetaUnsaved
	}
	return st.WriteMeta(Meta{Kind: kind})
}

// Rename moves a session's file, refusing to overwrite an existing one.
func Rename(dataDir, from, to string) error {
	if strings.ContainsAny(to, "/\\ ") || to == "" {
		return fmt.Errorf("session names must be a single filesystem-safe word")
	}
	src := filepath.Join(Dir(dataDir), from+".jsonl")
	dst := filepath.Join(Dir(dataDir), to+".jsonl")
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("session %q already exists", to)
	}
	return os.Rename(src, dst)
}

// Transfer compacts a session into a summary handoff in a fresh one, carrying
// the durable state across (plan.md §18).
func Transfer(dataDir, from, to, summary string) error {
	if _, err := os.Stat(filepath.Join(Dir(dataDir), to+".jsonl")); err == nil {
		return fmt.Errorf("session %q already exists", to)
	}
	st, err := Open(dataDir, to)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.WriteMeta(Meta{Kind: MetaStart, Note: "transferred from " + from}); err != nil {
		return err
	}
	return st.WriteMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: "[transferred] Continuing from session " + from + ".\n\n" + summary,
	})
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

// Compact rewrites a session down to a single summary message.
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
	path := filepath.Join(Dir(dataDir), name+".jsonl")
	entries, err := Read(path)
	if err != nil {
		return nil, err
	}

	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", data, 0o644)
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
	if data, err := json.Marshal(Meta{Kind: MetaCompact}); err == nil {
		write(Entry{TS: time.Now(), Type: TypeMeta, Data: data})
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
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
