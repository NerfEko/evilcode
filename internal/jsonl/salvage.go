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
	Line     []byte
	Start    int
	Depth    int
	InString bool
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
			Line:     line,
			Start:    start,
			Depth:    candidateState.depth,
			InString: candidateState.inString,
		}
		if ok && (candidate.Depth <= 1 || candidate.InString) && (filter == nil || filter(candidate)) {
			if value, accepted := decode(raw); accepted {
				out = append(out, value)
				state = candidateState
				state.advance(raw)
				cursor = end
				continue
			}
		}

		// Consume the opening brace and keep walking. This matters when the
		// first object is the torn prefix and a complete object starts later on
		// the same physical line.
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
	depth    int
	inString bool
	escaped  bool
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
		case '}', ']':
			if l.depth > 0 {
				l.depth--
			}
		}
	}
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
