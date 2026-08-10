package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"evilcode/internal/jsonl"
)

type sessionSearchArgs struct {
	Query string `json:"query"`
	Role  string `json:"role,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type searchableMessage struct {
	Role  string
	Text  string
	Terms map[string]struct{}
	Time  time.Time
}

type indexedSession struct {
	name     string
	size     int64
	modified time.Time
	terms    map[string]struct{}
	messages []searchableMessage
	bytes    int64
}

const (
	// Search is a convenience index, not a second copy of the transcript. These
	// bounds keep one unusually large tool result or a long-lived daemon's
	// session corpus from turning a search into an unbounded memory allocation.
	maxIndexedMessageBytes = 16 << 10
	maxIndexedSessionBytes = 1 << 20
	maxIndexedTermsPerMsg  = 4096
	maxIndexedTermsPerFile = 65536
	maxIndexedCorpusBytes  = 16 << 20
)

// sessionSearchIndex is deliberately private to the tool package. provider
// owns the canned demo tool fixtures and importing session here would create a
// provider → tools → session cycle; the native JSONL envelope is stable and
// small enough to decode directly for this read-only index.
type sessionSearchIndex struct {
	mu    sync.Mutex
	files map[string]indexedSession
	bytes int64
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

func (i *sessionSearchIndex) search(ctx context.Context, dataDir, currentName, query, role string, limit int) ([]sessionSearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
			messages, readErr := readSearchMessages(ctx, path)
			if readErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				i.remove(path)
				continue
			}
			cached = indexedSession{
				name: strings.TrimSuffix(entry.Name(), ".jsonl"), size: stat.Size(),
				modified: stat.ModTime(), messages: messages,
				terms: termsForMessages(messages),
			}
			cached.bytes = indexedSessionBytes(cached)
			i.replace(path, cached)
			i.reads++
		}
	}
	for path := range i.files {
		if !seen[path] {
			i.remove(path)
		}
	}

	queryText := strings.ToLower(strings.TrimSpace(query))
	var hits []sessionSearchHit
	for _, file := range i.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.name == currentName || !containsAll(file.terms, wanted) {
			continue
		}
		for _, message := range file.messages {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !roleMatches(message.Role, role) || !containsAll(message.Terms, wanted) {
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

// replace keeps the cache bounded while retaining the file being searched.
// Eviction is intentionally simple: the least recently modified cached file
// leaves first. An evicted file is reread on a later search, which is the safe
// trade for a read-only convenience index.
func (i *sessionSearchIndex) replace(path string, next indexedSession) {
	i.remove(path)
	for len(i.files) > 0 && i.bytes+next.bytes > maxIndexedCorpusBytes {
		var oldestPath string
		var oldest time.Time
		for candidatePath, candidate := range i.files {
			if oldestPath == "" || candidate.modified.Before(oldest) {
				oldestPath, oldest = candidatePath, candidate.modified
			}
		}
		i.bytes -= i.files[oldestPath].bytes
		delete(i.files, oldestPath)
	}
	i.files[path] = next
	i.bytes += next.bytes
}

func (i *sessionSearchIndex) remove(path string) {
	if old, ok := i.files[path]; ok {
		i.bytes -= old.bytes
		delete(i.files, path)
	}
}

type sessionSearchHit struct {
	Name, Role, Excerpt string
	Date                time.Time
	Score               int
}

type searchEntry struct {
	TS   time.Time       `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func readSearchMessages(ctx context.Context, path string) ([]searchableMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []searchableMessage
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			out = appendSearchLine(out, bytesTrimSpace(line))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return out, nil
}

// bytesTrimSpace is kept local so the scanner can pass byte slices directly
// to json.Unmarshal without converting each line to a second string.
func bytesTrimSpace(data []byte) []byte {
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\t' || data[0] == '\r' || data[0] == '\n') {
		data = data[1:]
	}
	for len(data) > 0 {
		last := data[len(data)-1]
		if last != ' ' && last != '\t' && last != '\r' && last != '\n' {
			break
		}
		data = data[:len(data)-1]
	}
	return data
}

func appendSearchLine(out []searchableMessage, line []byte) []searchableMessage {
	if len(line) == 0 {
		return out
	}
	if entry, ok := decodeSearchEntry(line); ok {
		if message, ok := searchableMessageFor(entry); ok {
			return appendBoundedMessage(out, message)
		}
		return out
	}

	// A hard kill can leave a torn JSON object immediately before a later
	// append. Use the same shared lexer as session.Read so search and resume see
	// the same repaired records instead of silently disagreeing.
	entries := jsonl.Salvage(line, []byte(`{"ts"`), func(raw []byte) (searchEntry, bool) {
		return decodeSearchEntry(raw)
	}, func(candidate jsonl.Candidate) bool {
		if candidate.Depth != 1 {
			return true
		}
		switch candidate.KeyBefore() {
		case "", "ts", "type", "data":
			return true
		default:
			return false
		}
	})
	for _, entry := range entries {
		if message, ok := searchableMessageFor(entry); ok {
			out = appendBoundedMessage(out, message)
		}
	}
	return out
}

func decodeSearchEntry(raw []byte) (searchEntry, bool) {
	var entry searchEntry
	if json.Unmarshal(raw, &entry) != nil || entry.Type == "" || len(entry.Data) == 0 {
		return searchEntry{}, false
	}
	return entry, true
}

func searchableMessageFor(entry searchEntry) (searchableMessage, bool) {
	if entry.Type == "meta" {
		return searchableMessage{}, false
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
		return searchableMessage{}, false
	}
	if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
		return searchableMessage{}, false
	}
	text := strings.TrimSpace(message.Content)
	if message.Reasoning != "" {
		text = strings.TrimSpace(strings.Join([]string{text, message.Reasoning}, "\n"))
	}
	for _, call := range message.ToolCalls {
		text = strings.TrimSpace(strings.Join([]string{text, call.Name, string(call.Args)}, "\n"))
	}
	if message.ToolName != "" {
		// Tool results commonly contain output as well as their name. Keep both;
		// role=tool searches should find `grep` even when its output is non-empty.
		text = strings.TrimSpace(strings.Join([]string{message.ToolName, text}, "\n"))
	}
	if text == "" {
		return searchableMessage{}, false
	}
	return searchableMessage{
		Role:  message.Role,
		Text:  boundedIndexedText(text),
		Terms: termsForTextLimited(text, maxIndexedTermsPerMsg),
		Time:  entry.TS,
	}, true
}

func appendBoundedMessage(out []searchableMessage, message searchableMessage) []searchableMessage {
	if indexedMessagesBytes(out)+indexedMessageBytes(message) > maxIndexedSessionBytes {
		return out
	}
	return append(out, message)
}

func indexedMessagesBytes(messages []searchableMessage) int64 {
	var total int64
	for _, message := range messages {
		total += indexedMessageBytes(message)
	}
	return total
}

func indexedMessageBytes(message searchableMessage) int64 {
	var total int64 = int64(len(message.Role) + len(message.Text) + 64)
	for term := range message.Terms {
		total += int64(len(term) + 16)
	}
	return total
}

func indexedSessionBytes(file indexedSession) int64 {
	return int64(len(file.name)+64) + indexedMessagesBytes(file.messages) + int64(len(file.terms))*16
}

func boundedIndexedText(text string) string {
	if len(text) <= maxIndexedMessageBytes {
		return text
	}
	keep := (maxIndexedMessageBytes - len("\n…\n")) / 2
	start := keepUTF8Prefix(text, keep)
	end := keepUTF8Suffix(text, keep)
	return start + "\n…\n" + end
}

func keepUTF8Prefix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func keepUTF8Suffix(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
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
	return termsForTextLimited(text, maxIndexedTermsPerMsg)
}

func termsForTextLimited(text string, max int) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range searchTerms(text) {
		terms[term] = struct{}{}
		if len(terms) >= max {
			break
		}
	}
	return terms
}

func termsForMessages(messages []searchableMessage) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, message := range messages {
		for term := range message.Terms {
			terms[term] = struct{}{}
			if len(terms) >= maxIndexedTermsPerFile {
				return terms
			}
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
	return newSessionSearch(dataDir, func() string { return currentName })
}

// NewSessionSearchWithCurrentName is the live-name variant used by the TUI.
// Renaming a running session changes the store's basename while this tool is
// still resident, so exclusion must be resolved at search time.
func NewSessionSearchWithCurrentName(dataDir string, currentName func() string) Tool {
	return newSessionSearch(dataDir, currentName)
}

func newSessionSearch(dataDir string, currentName func() string) Tool {
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
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			name := ""
			if currentName != nil {
				name = currentName()
			}
			hits, err := index.search(ctx, dataDir, name, args.Query, role, args.Limit)
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
