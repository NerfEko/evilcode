// Package jsonl contains the small amount of shared recovery machinery used
// by the append-only JSONL stores.
package jsonl

import "bytes"

// Salvage scans a malformed JSONL line for complete object values whose
// decoded shape is accepted by decode. The scanner keeps one lexical state as
// it walks the line, so it does not restart a JSON decoder at every brace.
//
// A candidate at depth zero is an ordinary next record. A candidate seen while
// the damaged prefix still has an open string is also eligible: that is the
// characteristic shape of a torn string followed by a later append. Objects
// nested inside an existing object or array are not eligible, which prevents a
// message payload from being promoted into a session or memory record.
func Salvage[T any](line, prefix []byte, decode func([]byte) (T, bool)) []T {
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
		if ok && (candidateState.depth == 0 || candidateState.inString) {
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
