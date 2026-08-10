package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type sessionSearchArgs struct {
	Query string `json:"query"`
	Role  string `json:"role,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type searchableMessage struct {
	Role string
	Text string
	Time time.Time
}

type indexedSession struct {
	name     string
	size     int64
	modified time.Time
	terms    map[string]struct{}
	messages []searchableMessage
}

// sessionSearchIndex is deliberately private to the tool package. provider
// owns the canned demo tool fixtures and importing session here would create a
// provider → tools → session cycle; the native JSONL envelope is stable and
// small enough to decode directly for this read-only index.
type sessionSearchIndex struct {
	mu    sync.Mutex
	files map[string]indexedSession
	reads uint64
}

func newSessionSearchIndex() *sessionSearchIndex {
	return &sessionSearchIndex{files: make(map[string]indexedSession)}
}

func (i *sessionSearchIndex) stats() (int, uint64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.files), i.reads
}

func (i *sessionSearchIndex) search(dataDir, currentName, query, role string, limit int) ([]sessionSearchHit, error) {
	wanted := searchTerms(query)
	if len(wanted) == 0 {
		return nil, fmt.Errorf("query must contain at least one word")
	}
	if limit <= 0 {
		limit = 10
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "all" {
		role = "any"
	}
	switch role {
	case "any", "user", "assistant", "tool":
	default:
		return nil, fmt.Errorf("role must be user, assistant, tool, or any")
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(dataDir, "sessions", entry.Name())
		stat, statErr := entry.Info()
		if statErr != nil || !stat.Mode().IsRegular() {
			continue
		}
		seen[path] = true
		cached, ok := i.files[path]
		if !ok || cached.size != stat.Size() || !cached.modified.Equal(stat.ModTime()) {
			messages, readErr := readSearchMessages(path)
			if readErr != nil {
				delete(i.files, path)
				continue
			}
			cached = indexedSession{
				name: strings.TrimSuffix(entry.Name(), ".jsonl"), size: stat.Size(),
				modified: stat.ModTime(), messages: messages,
				terms: termsForMessages(messages),
			}
			i.files[path] = cached
			i.reads++
		}
	}
	for path := range i.files {
		if !seen[path] {
			delete(i.files, path)
		}
	}

	queryText := strings.ToLower(strings.TrimSpace(query))
	var hits []sessionSearchHit
	for _, file := range i.files {
		if file.name == currentName || !containsAll(file.terms, wanted) {
			continue
		}
		for _, message := range file.messages {
			if !roleMatches(message.Role, role) || !containsAll(termsForText(message.Text), wanted) {
				continue
			}
			date := message.Time
			if date.IsZero() {
				date = file.modified
			}
			score := len(wanted)
			if strings.Contains(strings.ToLower(message.Text), queryText) {
				score += 100
			}
			hits = append(hits, sessionSearchHit{
				Name: file.name, Date: date, Role: message.Role,
				Excerpt: searchExcerpt(message.Text, query), Score: score,
			})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		if !hits[a].Date.Equal(hits[b].Date) {
			return hits[a].Date.After(hits[b].Date)
		}
		return hits[a].Name < hits[b].Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

type sessionSearchHit struct {
	Name, Role, Excerpt string
	Date                time.Time
	Score               int
}

func readSearchMessages(path string) ([]searchableMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []searchableMessage
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry struct {
			TS   time.Time       `json:"ts"`
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Type == "meta" {
			continue
		}
		var message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
			ToolName  string `json:"tool_name"`
			ToolCalls []struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(entry.Data, &message) != nil {
			continue
		}
		text := strings.TrimSpace(message.Content)
		if message.Reasoning != "" {
			text = strings.TrimSpace(strings.Join([]string{text, message.Reasoning}, "\n"))
		}
		for _, call := range message.ToolCalls {
			text = strings.TrimSpace(strings.Join([]string{text, call.Name, string(call.Args)}, "\n"))
		}
		if text == "" && message.ToolName != "" {
			text = message.ToolName
		}
		if text == "" {
			continue
		}
		out = append(out, searchableMessage{Role: message.Role, Text: text, Time: entry.TS})
	}
	return out, nil
}

func roleMatches(role, wanted string) bool { return wanted == "any" || role == wanted }

func searchTerms(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if len([]rune(part)) < 2 || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func termsForText(text string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range searchTerms(text) {
		terms[term] = struct{}{}
	}
	return terms
}

func termsForMessages(messages []searchableMessage) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, message := range messages {
		for term := range termsForText(message.Text) {
			terms[term] = struct{}{}
		}
	}
	return terms
}

func containsAll(available map[string]struct{}, wanted []string) bool {
	for _, term := range wanted {
		if _, ok := available[term]; !ok {
			return false
		}
	}
	return true
}

func searchExcerpt(text, query string) string {
	clean := strings.Join(strings.Fields(text), " ")
	if len([]rune(clean)) <= 320 {
		return clean
	}
	lower, needle := strings.ToLower(clean), strings.ToLower(strings.TrimSpace(query))
	start := strings.Index(lower, needle)
	if start < 0 {
		for _, term := range searchTerms(query) {
			if pos := strings.Index(lower, term); pos >= 0 {
				start = pos
				break
			}
		}
	}
	if start < 120 {
		start = 0
	} else {
		start -= 120
	}
	end := start + 320
	if end > len(clean) {
		end = len(clean)
	}
	for start > 0 && !utf8.RuneStart(clean[start]) {
		start--
	}
	for end < len(clean) && !utf8.RuneStart(clean[end]) {
		end++
	}
	return strings.TrimSpace(clean[start:end])
}

// NewSessionSearch builds the cross-session recall tool. The index belongs to
// the tool instance so a long-lived TUI, headless run, or daemon worker reuses
// unchanged files without sharing mutable session state across agents.
func NewSessionSearch(dataDir, currentName string) Tool {
	index := newSessionSearchIndex()
	return Tool{
		Name: "session_search",
		Desc: "Search past sessions by what was said. Returns the session name, date, role, and matching excerpt. Use role user, assistant, tool, or any.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Distinctive words or a phrase from an earlier session"},
    "role": {"type": "string", "enum": ["user", "assistant", "tool", "any"], "description": "Which speaker to search; defaults to any"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum matches; defaults to 10"}
  },
  "required": ["query"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			_ = ctx
			var args sessionSearchArgs
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			args.Query = strings.TrimSpace(args.Query)
			if args.Query == "" {
				return Result{}, fmt.Errorf("query is required")
			}
			if args.Limit == 0 {
				args.Limit = 10
			}
			if args.Limit < 1 || args.Limit > 50 {
				return Result{}, fmt.Errorf("limit must be between 1 and 50")
			}
			role := strings.ToLower(strings.TrimSpace(args.Role))
			if role == "" {
				role = "any"
			}
			hits, err := index.search(dataDir, currentName, args.Query, role, args.Limit)
			if err != nil {
				return Result{}, err
			}
			if len(hits) == 0 {
				return Result{Output: fmt.Sprintf("no session matches %q", args.Query), Intent: "no matching session"}, nil
			}

			var b strings.Builder
			fmt.Fprintf(&b, "found %d session match", len(hits))
			if len(hits) != 1 {
				b.WriteByte('e')
			}
			b.WriteString(":\n")
			for _, hit := range hits {
				fmt.Fprintf(&b, "- %s [%s] (%s): %s\n", hit.Name, hit.Date.Format("2006-01-02 15:04"), hit.Role, hit.Excerpt)
			}
			return Result{Output: strings.TrimRight(b.String(), "\n"), Intent: fmt.Sprintf("searched sessions for %q", args.Query)}, nil
		},
	}
}
