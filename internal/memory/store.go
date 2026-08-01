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

// Record is one memory.
type Record struct {
	ID      int64     `json:"id"`
	Text    string    `json:"text"`
	Kind    Kind      `json:"kind"`
	Session string    `json:"session,omitempty"`
	TS      time.Time `json:"ts"`

	// Vec is the embedding. It may be empty: embedding runs off the hot path
	// and is allowed to fail, in which case the text is still stored and is
	// still reachable by substring search.
	Vec []float32 `json:"vec,omitempty"`

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

// Add stores a memory, merging it into a near-duplicate if one exists.
//
// It returns the stored record and whether it merged. Merging keeps the newer
// text, because a restated fact is usually a corrected one.
func (s *Store) Add(text string, kind Kind, session string, vec []float32, ts time.Time) (Record, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Record{}, false, fmt.Errorf("memory text is empty")
	}
	if !kind.Valid() {
		return Record{}, false, fmt.Errorf("unknown memory kind %q (want fact, preference, project, or episode)", kind)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if i := s.findDuplicate(text, vec); i >= 0 {
		merged := s.records[i]
		merged.Text = text
		merged.Kind = kind
		merged.Session = session
		merged.TS = ts
		if len(vec) > 0 {
			merged.Vec = vec
		}
		if err := s.append(merged); err != nil {
			return Record{}, false, err
		}
		s.records[i] = merged
		return merged, true, nil
	}

	r := Record{ID: s.nextID, Text: text, Kind: kind, Session: session, TS: ts, Vec: vec}
	if err := s.append(r); err != nil {
		return Record{}, false, err
	}
	s.nextID++
	s.records = append(s.records, r)
	return r, false, nil
}

// findDuplicate returns the index of a near-identical memory, or -1.
func (s *Store) findDuplicate(text string, vec []float32) int {
	lower := strings.ToLower(text)
	for i, r := range s.records {
		if r.Deleted {
			continue
		}
		// Exact text is a duplicate whether or not either side embedded, which
		// is what keeps `remember` idempotent when the embedder is down.
		if strings.EqualFold(r.Text, text) || strings.ToLower(r.Text) == lower {
			return i
		}
		if len(vec) > 0 && len(r.Vec) == len(vec) && Cosine(r.Vec, vec) >= DedupeThreshold {
			return i
		}
	}
	return -1
}

// Forget tombstones a memory by ID.
func (s *Store) Forget(id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID != id || r.Deleted {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		if !r.Deleted {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	return out
}

// Len is the number of live memories.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		if !r.Deleted {
			n++
		}
	}
	return n
}

// Hit is one search result.
type Hit struct {
	Record
	Score float64
}

// RecallThreshold is the minimum score for passive recall. Below it a memory is
// noise, and injecting noise into every turn is worse than injecting nothing.
const RecallThreshold = 0.55

// Search ranks memories against a query embedding, returning the top n above
// the threshold.
//
// When the query has no embedding — the embedder was down, or the caller only
// has text — it falls back to substring matching, so recall degrades to
// something useful rather than to nothing.
func (s *Store) Search(query string, vec []float32, n int, threshold float64) []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hits []Hit
	if len(vec) > 0 {
		for _, r := range s.records {
			if r.Deleted || len(r.Vec) != len(vec) {
				continue
			}
			score := Cosine(r.Vec, vec)
			if score < threshold {
				continue
			}
			hits = append(hits, Hit{Record: r, Score: score * r.Kind.Weight()})
		}
	} else {
		// Only the no-embedding case degrades to substring matching. A working
		// embedder that found nothing above threshold means nothing is
		// relevant — falling back here would silently override a correct
		// "nothing relevant" with the far looser lexical fallback, recalling
		// something on almost every message.
		hits = s.substringHits(query)
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		// A tie goes to the more recent memory.
		return hits[i].TS.After(hits[j].TS)
	})
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

// substringHits is the no-embedding fallback. Scores sit just above the recall
// threshold so a lexical hit is usable but never outranks a semantic one.
func (s *Store) substringHits(query string) []Hit {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	var hits []Hit
	for _, r := range s.records {
		if r.Deleted {
			continue
		}
		text := strings.ToLower(r.Text)
		matched := 0
		for _, w := range words {
			if len(w) > 2 && strings.Contains(text, w) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		hits = append(hits, Hit{
			Record: r,
			Score:  RecallThreshold + 0.3*float64(matched)/float64(len(words)),
		})
	}
	return hits
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
