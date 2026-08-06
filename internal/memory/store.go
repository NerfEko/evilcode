// Package memory is the long-term memory bank of plan.md §19: durable facts
// the agent recalls across sessions, retrieved by embedding similarity.
//
// The plan specifies a sqlite-vec database. sqlite-vec is a C extension, which
// means cgo plus a compiled `.so` the user has to install before evilcode will
// start — a hard dependency for a bank that holds a few thousand short strings.
// A JSONL file with a brute-force cosine scan is the same behavior at this
// scale, so that is what this is. See DEVIATIONS.md.
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// Kind classifies a memory. The kinds are the plan's, and they exist to let
// recall weight a stated preference above an incidental episode.
type Kind string

const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindProject    Kind = "project"
	KindEpisode    Kind = "episode"
)

// Valid reports whether a kind is one the tools accept.
func (k Kind) Valid() bool {
	switch k {
	case KindFact, KindPreference, KindProject, KindEpisode:
		return true
	}
	return false
}

// Weight biases recall ranking. A preference the user stated outranks an
// episode that merely happened, at equal cosine distance.
func (k Kind) Weight() float64 {
	switch k {
	case KindPreference:
		return 1.15
	case KindProject:
		return 1.08
	case KindFact:
		return 1.0
	default:
		return 0.92
	}
}

// Scope controls which workspace can see a memory. Global records are visible
// everywhere; project records are keyed by the canonical workspace root.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

func (s Scope) Valid() bool {
	return s == ScopeGlobal || s == ScopeProject
}

// Record is one memory.
type Record struct {
	ID      int64     `json:"id"`
	Text    string    `json:"text"`
	Kind    Kind      `json:"kind"`
	Session string    `json:"session,omitempty"`
	TS      time.Time `json:"ts"`

	// Scope is global or project. Empty is treated as global for records written
	// before J5.6 added scope metadata.
	Scope Scope `json:"scope,omitempty"`
	// ProjectRoot is set only for project-scoped records and is canonicalized at
	// the write boundary so equivalent workspace paths share one bank view.
	ProjectRoot string `json:"project_root,omitempty"`

	// Vec is the embedding. It may be empty: embedding runs off the hot path
	// and is allowed to fail, in which case the text is still stored and is
	// still reachable by substring search.
	Vec []float32 `json:"vec,omitempty"`

	// EmbeddingModel identifies the vector space that produced Vec. Empty is
	// retained for legacy records written before model tagging existed; callers
	// with an active model deliberately exclude those vectors from dense scoring.
	EmbeddingModel string `json:"embedding_model,omitempty"`

	// Deleted tombstones a record. The file is append-only, so forgetting is
	// appending a tombstone rather than rewriting history.
	Deleted bool `json:"deleted,omitempty"`
}

// Store is the memory bank, held in memory and appended to a JSONL file.
type Store struct {
	Path string

	mu      sync.Mutex
	records []Record
	nextID  int64
	file    *os.File
	w       *bufio.Writer
}

// AddOptions selects the vector space and visibility scope for a new record.
// A non-empty ProjectRoot implies project scope unless ScopeGlobal is explicit.
type AddOptions struct {
	EmbeddingModel string
	Scope          Scope
	ProjectRoot    string
}

// FileName is the bank's file under the data directory.
const FileName = "memory.jsonl"

// Open loads the bank, creating it if absent.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, FileName)

	s := &Store{Path: path, nextID: 1}
	if err := s.load(path); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	s.file, s.w = f, bufio.NewWriter(f)
	return s, nil
}

// load replays the file. A malformed final line is tolerated and dropped —
// the classic shape of a crash mid-write, and losing the last memory to one
// is survivable — but a malformed line anywhere earlier is corruption further
// back in the log, and returns an error naming its line number rather than
// vanishing silently before the next write buries the evidence.
func (s *Store) load(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	byID := map[int64]int{}
	apply := func(lineNo int, line []byte, final bool) error {
		if len(line) == 0 {
			return nil
		}
		var r Record
		if json.Unmarshal(line, &r) != nil {
			if final {
				return nil
			}
			return fmt.Errorf("%s:%d: malformed memory record", path, lineNo)
		}
		if r.ID >= s.nextID {
			s.nextID = r.ID + 1
		}
		// A later line for the same ID supersedes the earlier one, which is how
		// a merge or a tombstone lands without rewriting the file.
		if i, ok := byID[r.ID]; ok {
			s.records[i] = r
			return nil
		}
		byID[r.ID] = len(s.records)
		s.records = append(s.records, r)
		return nil
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var havePending bool
	var pendingLine []byte
	var pendingNo int
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if havePending {
			if err := apply(pendingNo, pendingLine, false); err != nil {
				return err
			}
		}
		pendingLine = append([]byte(nil), sc.Bytes()...)
		pendingNo = lineNo
		havePending = true
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if havePending {
		var probe Record
		badFinal := json.Unmarshal(pendingLine, &probe) != nil
		if err := apply(pendingNo, pendingLine, true); err != nil {
			return err
		}
		if badFinal {
			end, err := f.Seek(0, io.SeekEnd)
			if err != nil {
				return err
			}
			cut := end - int64(len(pendingLine))
			if cut < 0 {
				return fmt.Errorf("%s: malformed final memory record has an invalid offset", path)
			}
			if err := f.Truncate(cut); err != nil {
				return err
			}
			if err := f.Sync(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) append(r Record) error {
	if s.file == nil || s.w == nil {
		return fmt.Errorf("memory store is closed")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.w.Flush()
}

// Close flushes and closes the file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.w.Flush()
	if cerr := s.file.Close(); err == nil {
		err = cerr
	}
	s.file = nil
	s.w = nil
	return err
}

// DedupeThreshold is the cosine above which two memories are the same memory.
// The plan pins 0.95: near-identical phrasings merge, related-but-distinct
// facts stay separate.
const DedupeThreshold = 0.95

// normalizeProjectRoot makes scope keys stable across relative and absolute
// callers. A failed Abs still gets a cleaned path, so scope matching remains
// deterministic even for a workspace that has just been removed.
func normalizeProjectRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	root = filepath.Clean(root)
	if abs, err := filepath.Abs(root); err == nil {
		root = filepath.Clean(abs)
	}
	return root
}

func normalizeWriteScope(scope Scope, projectRoot string) (Scope, string, error) {
	projectRoot = normalizeProjectRoot(projectRoot)
	switch scope {
	case "":
		if projectRoot != "" {
			return ScopeProject, projectRoot, nil
		}
		return ScopeGlobal, "", nil
	case ScopeGlobal:
		return ScopeGlobal, "", nil
	case ScopeProject:
		if projectRoot == "" {
			return "", "", fmt.Errorf("project memory requires a workspace root")
		}
		return ScopeProject, projectRoot, nil
	default:
		return "", "", fmt.Errorf("unknown memory scope %q (want global or project)", scope)
	}
}

func recordScope(r Record) Scope {
	if r.Scope == ScopeProject && normalizeProjectRoot(r.ProjectRoot) != "" {
		return ScopeProject
	}
	return ScopeGlobal
}

func sameScope(r Record, scope Scope, projectRoot string) bool {
	if recordScope(r) != scope {
		return false
	}
	if scope == ScopeProject {
		return normalizeProjectRoot(r.ProjectRoot) == projectRoot
	}
	return true
}

// visibleInProject implements project ∪ global. An empty query root retains
// the legacy unscoped Store API and returns every live record.
func visibleInProject(r Record, projectRoot string) bool {
	projectRoot = normalizeProjectRoot(projectRoot)
	if projectRoot == "" {
		return true
	}
	if recordScope(r) == ScopeGlobal {
		return true
	}
	return normalizeProjectRoot(r.ProjectRoot) == projectRoot
}

// Add stores a memory, merging it into a near-duplicate if one exists.
//
// It returns the stored record and whether it merged. Merging keeps the newer
// text, because a restated fact is usually a corrected one.
func (s *Store) Add(text string, kind Kind, session string, vec []float32, ts time.Time) (Record, bool, error) {
	return s.AddWithOptions(text, kind, session, vec, AddOptions{}, ts)
}

// AddWithModel stores a global memory and records the embedding model that
// produced its vector. Exact-text duplicates merge across model changes within
// that scope, while cosine deduplication is limited to one model's vector space.
func (s *Store) AddWithModel(text string, kind Kind, session string, vec []float32, model string, ts time.Time) (Record, bool, error) {
	return s.AddWithOptions(text, kind, session, vec, AddOptions{EmbeddingModel: model}, ts)
}

// AddWithOptions stores a memory with model and scope metadata. Scope is part
// of deduplication: a project preference must not merge into a global fact (or
// into the same text from another workspace).
func (s *Store) AddWithOptions(text string, kind Kind, session string, vec []float32, options AddOptions, ts time.Time) (Record, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Record{}, false, fmt.Errorf("memory text is empty")
	}
	if !kind.Valid() {
		return Record{}, false, fmt.Errorf("unknown memory kind %q (want fact, preference, project, or episode)", kind)
	}
	scope, projectRoot, err := normalizeWriteScope(options.Scope, options.ProjectRoot)
	if err != nil {
		return Record{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if i := s.findDuplicate(text, vec, options.EmbeddingModel, scope, projectRoot); i >= 0 {
		merged := s.records[i]
		merged.Text = text
		merged.Kind = kind
		merged.Session = session
		merged.TS = ts
		if len(vec) > 0 {
			merged.Vec = vec
			merged.EmbeddingModel = options.EmbeddingModel
		}
		if err := s.append(merged); err != nil {
			return Record{}, false, err
		}
		s.records[i] = merged
		return merged, true, nil
	}

	r := Record{
		ID:             s.nextID,
		Text:           text,
		Kind:           kind,
		Session:        session,
		TS:             ts,
		Scope:          scope,
		ProjectRoot:    projectRoot,
		Vec:            vec,
		EmbeddingModel: options.EmbeddingModel,
	}
	if err := s.append(r); err != nil {
		return Record{}, false, err
	}
	s.nextID++
	s.records = append(s.records, r)
	return r, false, nil
}

// findDuplicate returns the index of a near-identical memory, or -1.
func (s *Store) findDuplicate(text string, vec []float32, model string, scope Scope, projectRoot string) int {
	lower := strings.ToLower(text)
	for i, r := range s.records {
		if r.Deleted {
			continue
		}
		if !sameScope(r, scope, projectRoot) {
			continue
		}
		// Exact text is a duplicate whether or not either side embedded, which
		// is what keeps `remember` idempotent when the embedder is down.
		if strings.EqualFold(r.Text, text) || strings.ToLower(r.Text) == lower {
			return i
		}
		if len(vec) > 0 && len(r.Vec) == len(vec) && r.EmbeddingModel == model && Cosine(r.Vec, vec) >= DedupeThreshold {
			return i
		}
	}
	return -1
}

// Forget tombstones a memory by ID.
func (s *Store) Forget(id int64) (bool, error) {
	return s.ForgetScoped(id, "")
}

// ForgetScoped tombstones a memory only when it belongs to the current
// project/global view. IDs are global, but a hidden project's id should not be
// writable through another workspace's `/memory forget` command.
func (s *Store) ForgetScoped(id int64, projectRoot string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID != id || r.Deleted || !visibleInProject(r, projectRoot) {
			continue
		}
		r.Deleted = true
		if err := s.append(r); err != nil {
			return false, err
		}
		s.records[i] = r
		return true, nil
	}
	return false, nil
}

// All returns the live records, newest first.
func (s *Store) All() []Record {
	return s.AllScoped("")
}

// AllScoped returns live records visible to a project manager, newest first.
// Global records and records keyed to projectRoot are included.
func (s *Store) AllScoped(projectRoot string) []Record {
	return s.AllByScope(projectRoot, "")
}

// AllByScope returns live records for one explicit scope. An empty scope is the
// project ∪ global view; project scope requires a non-empty workspace root.
func (s *Store) AllByScope(projectRoot string, scope Scope) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectRoot = normalizeProjectRoot(projectRoot)
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		if r.Deleted {
			continue
		}
		visible := false
		switch scope {
		case "":
			visible = visibleInProject(r, projectRoot)
		case ScopeGlobal:
			visible = recordScope(r) == ScopeGlobal
		case ScopeProject:
			visible = projectRoot != "" && recordScope(r) == ScopeProject &&
				normalizeProjectRoot(r.ProjectRoot) == projectRoot
		}
		if visible {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	return out
}

// Len is the number of live memories.
func (s *Store) Len() int {
	return s.LenScoped("")
}

// LenScoped counts live records visible to a project manager.
func (s *Store) LenScoped(projectRoot string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		if !r.Deleted && visibleInProject(r, projectRoot) {
			n++
		}
	}
	return n
}

// Hit is one search result.
type Hit struct {
	Record
	// Score is the final RRF score after kind weighting. Relevance is a
	// human-scale signal from the dense/lexical retrievers used by the adaptive
	// recall cutoff; it is not used to order the fused results.
	Score     float64
	Relevance float64
}

// SearchOptions qualifies dense scoring without changing the legacy Search
// call shape used by tools and older embedders.
type SearchOptions struct {
	// EmbeddingModel limits cosine comparisons to vectors from this model. An
	// empty value preserves the pre-J5 behavior for callers without model data.
	EmbeddingModel string
	// ProjectRoot selects the project ∪ global view. Empty preserves the legacy
	// unscoped Store API and includes every live record.
	ProjectRoot string
}

// RecallThreshold is the minimum score for passive recall. Below it a memory is
// noise, and injecting noise into every turn is worse than injecting nothing.
const RecallThreshold = 0.55

// Search ranks memories with both dense cosine and lexical BM25 retrieval.
// Their rank lists are fused with reciprocal rank fusion (RRF), so an exact
// term match remains visible even when semantic retrieval also returned hits.
// A query without an embedding simply contributes no dense rank list.
func (s *Store) Search(query string, vec []float32, n int, threshold float64, options ...SearchOptions) []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var opts SearchOptions
	if len(options) > 0 {
		opts = options[0]
	}

	records := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		if !r.Deleted && visibleInProject(r, opts.ProjectRoot) {
			records = append(records, r)
		}
	}
	if len(records) == 0 {
		return nil
	}

	pool := len(records)
	if n > 0 {
		pool = n * 5
		if pool < 50 {
			pool = 50
		}
		if pool > len(records) {
			pool = len(records)
		}
	}

	dense := make([]rankedMemory, 0, len(records))
	for i, r := range records {
		if len(vec) == 0 || len(r.Vec) != len(vec) || (opts.EmbeddingModel != "" && r.EmbeddingModel != opts.EmbeddingModel) {
			continue
		}
		score := Cosine(r.Vec, vec)
		// Keep the existing threshold as a dense candidate floor. Lexical
		// ranking is independent, so a low-cosine exact term still survives.
		if score < threshold {
			continue
		}
		dense = append(dense, rankedMemory{index: i, score: score})
	}
	sortRankedMemory(dense, records)
	if len(dense) > pool {
		dense = dense[:pool]
	}

	lexical := bm25Rank(records, query, pool)
	const rrfK = 60.0
	fused := make(map[int]float64, len(dense)+len(lexical))
	denseScores := make(map[int]float64, len(dense))
	lexicalScores := make(map[int]float64, len(lexical))
	for rank, hit := range dense {
		fused[hit.index] += 1 / (rrfK + float64(rank) + 1)
		denseScores[hit.index] = hit.score
	}
	for rank, hit := range lexical {
		fused[hit.index] += 1 / (rrfK + float64(rank) + 1)
		lexicalScores[hit.index] = hit.score
	}

	hits := make([]Hit, 0, len(fused))
	for index, fusedScore := range fused {
		relevance := denseScores[index]
		if lexicalScore := lexicalScores[index]; lexicalScore > 0 {
			// BM25 is unbounded; compress it into a signal near the same
			// 0..1 range as cosine while keeping lexical-only hits visible.
			base := threshold
			if base < 0 {
				base = 0
			}
			if base > 1 {
				base = 1
			}
			lexicalRelevance := base + (1-base)*(lexicalScore/(lexicalScore+1))
			if lexicalRelevance > relevance {
				relevance = lexicalRelevance
			}
		}
		hits = append(hits, Hit{
			Record:    records[index],
			Score:     fusedScore * records[index].Kind.Weight(),
			Relevance: relevance,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Relevance != hits[j].Relevance {
			return hits[i].Relevance > hits[j].Relevance
		}
		if !hits[i].TS.Equal(hits[j].TS) {
			return hits[i].TS.After(hits[j].TS)
		}
		return hits[i].ID < hits[j].ID
	})
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

type rankedMemory struct {
	index int
	score float64
}

func sortRankedMemory(hits []rankedMemory, records []Record) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if !records[hits[i].index].TS.Equal(records[hits[j].index].TS) {
			return records[hits[i].index].TS.After(records[hits[j].index].TS)
		}
		return records[hits[i].index].ID < records[hits[j].index].ID
	})
}

// bm25Rank returns lexical rank positions and raw BM25 scores. It is a small
// one-pass scorer: the bank is intentionally a few thousand short records,
// where rebuilding document frequencies per query is cheaper than maintaining
// another persistent index.
func bm25Rank(records []Record, query string, limit int) []rankedMemory {
	queryTerms := uniqueTerms(query)
	if len(queryTerms) == 0 || len(records) == 0 {
		return nil
	}
	docs := make([][]string, len(records))
	lengthTotal := 0
	df := make(map[string]float64)
	for i, record := range records {
		docs[i] = searchTerms(record.Text)
		lengthTotal += len(docs[i])
		seen := make(map[string]struct{}, len(docs[i]))
		for _, term := range docs[i] {
			if _, ok := seen[term]; ok {
				continue
			}
			seen[term] = struct{}{}
			df[term]++
		}
	}
	avgdl := float64(lengthTotal) / float64(len(records))
	if avgdl == 0 {
		return nil
	}

	const k1 = 1.2
	const b = 0.75
	scored := make([]rankedMemory, 0, len(records))
	for i, doc := range docs {
		if len(doc) == 0 {
			continue
		}
		tf := make(map[string]float64, len(doc))
		for _, term := range doc {
			tf[term]++
		}
		dl := float64(len(doc))
		score := 0.0
		for _, term := range queryTerms {
			frequency := tf[term]
			if frequency == 0 {
				continue
			}
			docFrequency := df[term]
			idf := math.Log((float64(len(records))-docFrequency+0.5)/(docFrequency+0.5) + 1)
			denom := frequency + k1*(1-b+b*dl/avgdl)
			score += idf * frequency * (k1 + 1) / denom
		}
		if score > 0 {
			scored = append(scored, rankedMemory{index: i, score: score})
		}
	}
	sortRankedMemory(scored, records)
	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

func uniqueTerms(text string) []string {
	seen := make(map[string]struct{})
	var terms []string
	for _, term := range searchTerms(text) {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

func searchTerms(text string) []string {
	text = strings.ToLower(text)
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

// Cosine is the cosine similarity of two equal-length vectors.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
