// Package jsonl contains the small amount of shared recovery machinery used
// by the append-only JSONL stores.
package jsonl

import (
	"bytes"
	"encoding/json"
)

// Candidate describes the lexical context immediately before a complete
// object found by Salvage. Callers can use it to reject a schema-valid object
// that is still clearly a value nested under an unrelated field.
type Candidate struct {
	Line      []byte
	Start     int
	Depth     int
	InString  bool
	Container byte
	InArray   bool
}

// CandidateFilter is an optional caller-specific guard for ambiguous records.
type CandidateFilter func(Candidate) bool

// Salvage scans a malformed JSONL line for complete object values whose
// decoded shape is accepted by decode. The scanner keeps one lexical state as
// it walks the line, so it does not restart a JSON decoder at every brace.
//
// A candidate at depth zero is an ordinary next record. A candidate seen while
// the damaged prefix still has an open string is also eligible: that is the
// characteristic shape of a torn string followed by a later append. A depth of
// one is also eligible because a writer can die after opening the outer record
// (for example immediately after its data key) and the next append then starts
// while that outer object is still open. Deeper candidates remain ineligible so
// nested payload objects are not promoted into session or memory records.
func Salvage[T any](line, prefix []byte, decode func([]byte) (T, bool), filter CandidateFilter) []T {
	var out []T
	state := lexer{}
	cursor := 0
	for cursor < len(line) {
		rel := bytes.Index(line[cursor:], prefix)
		if rel < 0 {
			break
		}
		start := cursor + rel

		candidateState := state
		candidateState.advance(line[cursor:start])
		raw, end, ok := object(line, start)
		candidate := Candidate{
			Line:      line,
			Start:     start,
			Depth:     candidateState.depth,
			InString:  candidateState.inString,
			Container: candidateState.openContainer(),
			InArray:   candidateState.hasContainer('['),
		}
		if ok && !candidate.InArray && (candidate.Depth == 0 || candidate.InString ||
			(candidate.Depth == 1 && candidate.Container == '{')) &&
			(filter == nil || filter(candidate)) {
			if value, accepted := decode(raw); accepted {
				out = append(out, value)
				state = candidateState
				state.advance(raw)
				cursor = end
				continue
			}
		}

		// Keep the lexical context accumulated before this candidate, then consume
		// its opening brace and keep walking. Dropping candidateState here would
		// forget an enclosing array or an open string before the next candidate.
		state = candidateState
		state.advance(line[start : start+1])
		cursor = start + 1
	}
	return out
}

// KeyBefore returns the JSON object key whose value begins at the candidate
// start, when the candidate is immediately after a colon. It is deliberately
// small and tolerant because salvage runs on a malformed prefix; callers use
// an empty result as "unknown context" and fall back to the lexical guard.
func (c Candidate) KeyBefore() string {
	i := c.Start - 1
	for i >= 0 && isJSONSpace(c.Line[i]) {
		i--
	}
	if i < 0 || c.Line[i] != ':' {
		return ""
	}
	i--
	for i >= 0 && isJSONSpace(c.Line[i]) {
		i--
	}
	if i < 0 || c.Line[i] != '"' {
		return ""
	}
	end := i
	for i--; i >= 0; i-- {
		if c.Line[i] != '"' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && c.Line[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			var key string
			if err := json.Unmarshal(c.Line[i:end+1], &key); err == nil {
				return key
			}
			return ""
		}
	}
	return ""
}

func isJSONSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

type lexer struct {
	depth      int
	inString   bool
	escaped    bool
	containers []byte
	openCounts [2]int
}

func (l *lexer) advance(data []byte) {
	for _, c := range data {
		if l.inString {
			if l.escaped {
				l.escaped = false
				continue
			}
			switch c {
			case '\\':
				l.escaped = true
			case '"':
				l.inString = false
			}
			continue
		}

		switch c {
		case '"':
			l.inString = true
		case '{', '[':
			l.depth++
			l.containers = append(l.containers, c)
			l.openCounts[containerSlot(c)]++
		case '}', ']':
			want := byte('{')
			if c == ']' {
				want = '['
			}
			if l.openCounts[containerSlot(want)] == 0 {
				continue
			}
			for i := len(l.containers) - 1; i >= 0; i-- {
				if l.containers[i] == want {
					// A mismatched closer is useful evidence that the damaged
					// prefix's innermost container never closed. Drop it and
					// any containers above the matching opener so later records
					// can be considered against a resynchronized context.
					for _, opener := range l.containers[i:] {
						l.openCounts[containerSlot(opener)]--
					}
					l.containers = l.containers[:i]
					l.depth = len(l.containers)
					break
				}
			}
		}
	}
}

func containerSlot(c byte) int {
	if c == '[' {
		return 1
	}
	return 0
}

func (l lexer) openContainer() byte {
	if len(l.containers) == 0 {
		return 0
	}
	return l.containers[len(l.containers)-1]
}

func (l lexer) hasContainer(want byte) bool {
	for _, container := range l.containers {
		if container == want {
			return true
		}
	}
	return false
}

// object returns the balanced object beginning at start. It intentionally
// leaves JSON grammar validation to decode; this pass only finds a bounded
// candidate without reparsing the rest of the line on every attempt.
func object(line []byte, start int) ([]byte, int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(line); i++ {
		c := line[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return nil, 0, false
			}
			depth--
			if depth == 0 {
				if c != '}' {
					return nil, 0, false
				}
				return line[start : i+1], i + 1, true
			}
		}
	}
	return nil, 0, false
}
