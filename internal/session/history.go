package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Prompt history limits (plan.md §5.2, §6.1).
const (
	// HistoryCap is how many prompts survive a compaction.
	HistoryCap = 1000

	// CompactAt is the line count that triggers a rewrite.
	CompactAt = 2000

	// MaxPromptLen keeps a giant paste out of the history file entirely.
	MaxPromptLen = 10_000
)

// History is the append-only prompt log, shared across sessions so Up-arrow
// recall reaches prior sessions (plan.md §5.2).
type History struct {
	Path string

	mu      sync.Mutex
	entries []string
}

// HistoryPath is the prompt history file under the data directory.
func HistoryPath(dataDir string) string {
	return filepath.Join(dataDir, "prompt-history.jsonl")
}

// OpenHistory loads the prompt history, creating it lazily on first write.
func OpenHistory(dataDir string) (*History, error) {
	h := &History{Path: HistoryPath(dataDir)}
	if err := h.load(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *History) load() error {
	f, err := os.OpenFile(h.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var s string
		if json.Unmarshal([]byte(line), &s) != nil {
			continue
		}
		h.entries = append(h.entries, s)
	}
	return sc.Err()
}

// Add records a prompt. Prompts over MaxPromptLen are not recorded at all —
// a pasted file is not something anyone wants to scroll back to.
func (h *History) Add(prompt string) error {
	prompt = strings.TrimRight(prompt, "\n")
	if strings.TrimSpace(prompt) == "" || len(prompt) > MaxPromptLen {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	// Consecutive duplicates are noise; a repeat further back is legitimate
	// history and stays.
	if n := len(h.entries); n > 0 && h.entries[n-1] == prompt {
		return nil
	}
	old := append([]string(nil), h.entries...)
	h.entries = append(h.entries, prompt)
	needsCompaction := len(h.entries) >= CompactAt

	if needsCompaction {
		if err := h.compactLocked(); err != nil {
			h.entries = old
			return err
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(h.Path), 0o700); err != nil {
		h.entries = old
		return err
	}
	f, err := os.OpenFile(h.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		h.entries = old
		return err
	}
	line, err := json.Marshal(prompt)
	if err != nil {
		_ = f.Close()
		h.entries = old
		return err
	}
	_, err = f.Write(append(line, '\n'))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		h.entries = old
		return errors.Join(err, closeErr)
	}
	return nil
}

// compact rewrites the file with the most recent HistoryCap entries after
// dropping older duplicates.
func (h *History) compact() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.compactLocked()
}

func (h *History) compactLocked() error {
	kept := dedupeKeepingLatest(h.entries)
	if len(kept) > HistoryCap {
		kept = kept[len(kept)-HistoryCap:]
	}
	snapshot := append([]string(nil), kept...)

	if err := os.MkdirAll(filepath.Dir(h.Path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range snapshot {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	// Write to a temp file and rename, so an interrupted compaction cannot
	// leave a half-written history behind.
	tmp, err := os.CreateTemp(filepath.Dir(h.Path), ".prompt-history-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write([]byte(b.String())); err != nil {
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
	if err := os.Rename(tmpName, h.Path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(h.Path))
	if err != nil {
		return err
	}
	dirErr := dir.Sync()
	closeErr := dir.Close()
	if err := errors.Join(dirErr, closeErr); err != nil {
		return err
	}
	h.entries = kept
	return nil
}

// dedupeKeepingLatest removes earlier copies of repeated prompts, keeping the
// most recent occurrence and the overall ordering.
func dedupeKeepingLatest(in []string) []string {
	lastIdx := make(map[string]int, len(in))
	for i, s := range in {
		lastIdx[s] = i
	}
	out := make([]string, 0, len(in))
	for i, s := range in {
		if lastIdx[s] == i {
			out = append(out, s)
		}
	}
	return out
}

// All returns the recorded prompts, oldest first.
func (h *History) All() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.entries...)
}

// Len reports how many prompts are held.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// Search returns prompts matching query by free-form fuzzy match (characters in
// order, anywhere), most recent first, capped at max. This is deliberately
// looser than the slash-palette scorer: readline history search matches
// anywhere in the line, not from an anchor (plan.md §5.2).
func (h *History) Search(query string, max int) []string {
	h.mu.Lock()
	entries := h.entries
	h.mu.Unlock()

	// An empty query matches nothing, matching readline's behavior.
	if query == "" {
		return nil
	}
	if max <= 0 {
		max = 50
	}

	type scored struct {
		text  string
		score int
		age   int
	}
	var hits []scored
	seen := map[string]bool{}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if seen[e] {
			continue
		}
		score, ok := fuzzyScore(query, e)
		if !ok {
			continue
		}
		seen[e] = true
		hits = append(hits, scored{text: e, score: score, age: len(entries) - i})
		if len(hits) >= max*4 {
			break
		}
	}

	// Sort by score, then recency. A stable insertion keeps this readable at
	// this size, and the list is capped anyway.
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0; j-- {
			a, b := hits[j-1], hits[j]
			if b.score > a.score || (b.score == a.score && b.age < a.age) {
				hits[j-1], hits[j] = b, a
				continue
			}
			break
		}
	}
	if len(hits) > max {
		hits = hits[:max]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.text
	}
	return out
}

// fuzzyScore matches query characters in order anywhere in text, rewarding
// adjacency and word-start hits so "fixauth" ranks "fix the auth" above a
// scattered coincidence.
func fuzzyScore(query, text string) (int, bool) {
	lq, lt := strings.ToLower(query), strings.ToLower(text)
	score, qi, lastMatch := 0, 0, -2

	for ti := 0; ti < len(lt) && qi < len(lq); ti++ {
		if lt[ti] != lq[qi] {
			continue
		}
		score += 1
		if ti == lastMatch+1 {
			score += 3 // adjacent characters are a much stronger signal
		}
		if ti == 0 || lt[ti-1] == ' ' || lt[ti-1] == '/' || lt[ti-1] == '-' {
			score += 2 // word starts
		}
		lastMatch = ti
		qi++
	}
	if qi < len(lq) {
		return 0, false
	}
	// Prefer shorter matches: the same characters in a tighter line is a
	// better answer.
	score -= len(lt) / 40
	return score, true
}
