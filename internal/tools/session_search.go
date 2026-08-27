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
	Role      string
	Text      string
	Terms     map[string]struct{}
	Time      time.Time
	Truncated bool
	// TermsComplete is false when a single message exceeded the bounded term
	// dictionary. The file then uses a streaming fallback for queries absent
	// from the cache instead of claiming complete search coverage.
	TermsComplete bool
	Ordinal       int
}

type indexedSession struct {
	name     string
	size     int64
	modified time.Time
	terms    map[string]struct{}
	messages []searchableMessage
	bytes    int64
	complete bool
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
	// Keep the files seen during this invocation separate from the bounded
	// cross-invocation cache. Loading a later file may evict an earlier one, but
	// that earlier file still belongs in this search's result set.
	scanned := make(map[string]indexedSession, len(entries))
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
			messages, complete, readErr := readSearchMessages(ctx, path)
			if readErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				i.remove(path)
				continue
			}
			fileTerms := termsForMessages(messages)
			cached = indexedSession{
				name: strings.TrimSuffix(entry.Name(), ".jsonl"), size: stat.Size(),
				modified: stat.ModTime(), messages: messages,
				terms: fileTerms, complete: complete && len(fileTerms) < maxIndexedTermsPerFile,
			}
			cached.bytes = indexedSessionBytes(cached)
			i.replace(path, cached)
			i.reads++
		}
		scanned[path] = cached
	}
	for path := range i.files {
		if !seen[path] {
			i.remove(path)
		}
	}

	queryText := strings.ToLower(strings.TrimSpace(query))
	var hits []sessionSearchHit
	for path, file := range scanned {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.name == currentName {
			continue
		}
		if !file.complete {
			original, scanErr := searchOriginalHits(ctx, path, file.name, file.modified, query, role, wanted, limit)
			if scanErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			hits = append(hits, original...)
			continue
		}
		if !containsAll(file.terms, wanted) {
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
			excerpt := searchExcerpt(message.Text, query)
			if message.Truncated && !excerptContainsQuery(excerpt, query) {
				if exact, exactDate, ok := searchOriginalExcerpt(ctx, path, query, role, wanted, message.Ordinal); ok {
					excerpt = exact
					if !exactDate.IsZero() {
						date = exactDate
					}
				}
			}
			hits = append(hits, sessionSearchHit{
				Name: file.name, Date: date, Role: message.Role,
				Excerpt: excerpt, Score: score,
			})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return searchHitLess(hits[a], hits[b]) })
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

func searchHitLess(a, b sessionSearchHit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if !a.Date.Equal(b.Date) {
		return a.Date.After(b.Date)
	}
	return a.Name < b.Name
}

type searchEntry struct {
	TS   time.Time       `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func readSearchMessages(ctx context.Context, path string) ([]searchableMessage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var out []searchableMessage
	var indexedBytes int64
	var ordinal int
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			out = appendSearchLine(out, &indexedBytes, &ordinal, bytesTrimSpace(line))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, false, readErr
		}
	}
	complete := len(out) == ordinal
	for _, message := range out {
		complete = complete && message.TermsComplete
	}
	return out, complete, nil
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

func appendSearchLine(out []searchableMessage, indexedBytes *int64, ordinal *int, line []byte) []searchableMessage {
	for _, entry := range searchEntriesFromLine(line) {
		if message, ok := searchableMessageFor(entry); ok {
			message.Ordinal = *ordinal
			*ordinal = *ordinal + 1
			out = appendBoundedMessage(out, indexedBytes, message)
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
	role, text, ok := searchableTextFor(entry)
	if !ok {
		return searchableMessage{}, false
	}
	terms, termsComplete := termsForTextLimitedComplete(text, maxIndexedTermsPerMsg)
	return searchableMessage{
		Role:          role,
		Text:          boundedIndexedText(text),
		Terms:         terms,
		Time:          entry.TS,
		Truncated:     len(text) > maxIndexedMessageBytes,
		TermsComplete: termsComplete,
	}, true
}

func searchableTextFor(entry searchEntry) (string, string, bool) {
	if entry.Type == "meta" {
		return "", "", false
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
		return "", "", false
	}
	if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
		return "", "", false
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
		return "", "", false
	}
	return message.Role, text, true
}

func appendBoundedMessage(out []searchableMessage, indexedBytes *int64, message searchableMessage) []searchableMessage {
	cost := indexedMessageBytes(message)
	for len(out) > 0 && *indexedBytes+cost > maxIndexedSessionBytes {
		*indexedBytes -= indexedMessageBytes(out[0])
		out = out[1:]
	}
	if *indexedBytes+cost > maxIndexedSessionBytes {
		return out
	}
	*indexedBytes += cost
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
	total := int64(len(file.name) + 64)
	total += indexedMessagesBytes(file.messages)
	for term := range file.terms {
		total += int64(len(term) + 16)
	}
	return total
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

func excerptContainsQuery(excerpt, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(excerpt), strings.ToLower(query))
}

// searchOriginalExcerpt rereads only a hit whose bounded cached text did not
// contain the query. The index remains bounded, while the result still shows
// the useful context from the original large message.
func searchOriginalExcerpt(ctx context.Context, path, query, role string, wanted []string, targetOrdinal int) (string, time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	var ordinal int
	for {
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, false
		}
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			for _, entry := range searchEntriesFromLine(bytesTrimSpace(line)) {
				messageRole, text, ok := searchableTextFor(entry)
				if !ok {
					continue
				}
				currentOrdinal := ordinal
				ordinal++
				if currentOrdinal != targetOrdinal || !roleMatches(messageRole, role) || !containsWantedTerms(text, wanted) {
					continue
				}
				return searchExcerpt(text, query), entry.TS, true
			}
		}
		if readErr == io.EOF {
			return "", time.Time{}, false
		}
		if readErr != nil {
			return "", time.Time{}, false
		}
	}
}

// searchOriginalHits is the bounded-cache escape hatch. It runs only when the
// index had to evict messages or truncate a term dictionary, and retains at
// most limit strong/recent hits while streaming the original transcript.
func searchOriginalHits(ctx context.Context, path, name string, modified time.Time, query, role string, wanted []string, limit int) ([]sessionSearchHit, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if limit < 1 {
		limit = 1
	}
	queryText := strings.ToLower(strings.TrimSpace(query))
	hits := make([]sessionSearchHit, 0, limit)
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			for _, entry := range searchEntriesFromLine(bytesTrimSpace(line)) {
				messageRole, text, ok := searchableTextFor(entry)
				if !ok || !roleMatches(messageRole, role) || !containsWantedTerms(text, wanted) {
					continue
				}
				date := entry.TS
				if date.IsZero() {
					date = modified
				}
				score := len(wanted)
				if strings.Contains(strings.ToLower(text), queryText) {
					score += 100
				}
				hits = append(hits, sessionSearchHit{
					Name: name, Date: date, Role: messageRole,
					Excerpt: searchExcerpt(text, query), Score: score,
				})
				if len(hits) >= limit*2 {
					sort.SliceStable(hits, func(a, b int) bool { return searchHitLess(hits[a], hits[b]) })
					hits = hits[:limit]
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return searchHitLess(hits[a], hits[b]) })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func searchEntriesFromLine(line []byte) []searchEntry {
	if len(line) == 0 {
		return nil
	}
	if entry, ok := decodeSearchEntry(line); ok {
		return []searchEntry{entry}
	}
	return jsonl.Salvage(line, []byte(`{"ts"`), func(raw []byte) (searchEntry, bool) {
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
	return collectTerms(text, 4096)
}

func termsForText(text string) map[string]struct{} {
	return termsForTextLimited(text, maxIndexedTermsPerMsg)
}

func termsForTextLimited(text string, max int) map[string]struct{} {
	terms, _ := termsForTextLimitedComplete(text, max)
	return terms
}

func termsForTextLimitedComplete(text string, max int) (map[string]struct{}, bool) {
	terms := make(map[string]struct{})
	for _, term := range collectTerms(text, max) {
		terms[term] = struct{}{}
	}
	// Exactly hitting the ceiling is conservatively treated as incomplete. It
	// may be a false alarm for a message with exactly max distinct terms, but a
	// streaming fallback is preferable to silently missing a later term.
	return terms, len(terms) < max
}

const maxIndexedTermBytes = 256

func collectTerms(text string, max int) []string {
	if max <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, min(max, 32))
	terms := make([]string, 0, min(max, 32))
	start := -1
	add := func(end int) bool {
		if start < 0 {
			return true
		}
		raw := text[start:end]
		start = -1
		if len(raw) > maxIndexedTermBytes {
			return true
		}
		term := strings.ToLower(raw)
		if len([]rune(term)) < 2 {
			return true
		}
		term = strings.Clone(term)
		if _, ok := seen[term]; ok {
			return true
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		return len(terms) < max
	}
	for pos, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			if start < 0 {
				start = pos
			}
			continue
		}
		if !add(pos) {
			return terms
		}
	}
	add(len(text))
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

// containsWantedTerms scans only for the bounded query vocabulary instead of
// building a dictionary for every distinct word in a large original message.
func containsWantedTerms(text string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	var foundMask uint64
	var found []bool
	if len(wanted) > 64 {
		found = make([]bool, len(wanted))
	}
	foundCount := 0
	start := -1
	check := func(end int) bool {
		if start < 0 {
			return false
		}
		raw := text[start:end]
		start = -1
		if len(raw) > maxIndexedTermBytes {
			return false
		}
		for index, term := range wanted {
			alreadyFound := index < 64 && foundMask&(uint64(1)<<index) != 0 || index >= 64 && found[index]
			if alreadyFound || !strings.EqualFold(raw, term) {
				continue
			}
			if index < 64 {
				foundMask |= uint64(1) << index
			} else {
				found[index] = true
			}
			foundCount++
			break
		}
		return foundCount == len(wanted)
	}
	for position, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			if start < 0 {
				start = position
			}
			continue
		}
		if check(position) {
			return true
		}
	}
	return check(len(text))
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
		Name:   "session_search",
		Effect: EffectReadOnly,
		Desc: "Search past sessions by what was said when the current transcript, project " +
			"files, and memory do not contain an earlier decision. Use distinctive words or " +
			"a phrase. Returns session name, date, role, and matching excerpt; the current " +
			"session is excluded. Use role user, assistant, tool, or any.",
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
