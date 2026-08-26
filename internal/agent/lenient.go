package agent

import (
	"encoding/json"
	"strings"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// parseLenientToolCalls extracts a tool call that a small model wrote as JSON
// in prose instead of a structured tool_call record (config
// [[model]] lenient_tool_parse, A6).
//
// Strictness is the point: the call must name a tool that actually exists in
// the set, the arguments must be a JSON object, and at most one call per
// message is accepted. Ordinary prose cannot misfire because it would have to
// name a real tool exactly and carry an object literal. On success the matched
// JSON is stripped from the content so the transcript keeps the surrounding
// prose and the turn dispatches the call like any other.
func parseLenientToolCalls(content string, ts tools.Set) ([]provider.ToolCall, string, bool) {
	raw, start, end, ok := findToolJSON(content)
	if !ok {
		return nil, content, false
	}

	// `function` may be either a plain name ("read") or the wrapped OpenAI
	// shape ({"name": ..., "arguments": ...}); decode it as raw bytes and
	// interpret both.
	var shape struct {
		Tool      string          `json:"tool"`
		Name      string          `json:"name"`
		Function  json.RawMessage `json:"function"`
		Type      string          `json:"type"`
		Args      json.RawMessage `json:"args"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, content, false
	}

	name := strings.TrimSpace(shape.Tool)
	if name == "" {
		name = strings.TrimSpace(shape.Name)
	}
	var nestedArgs json.RawMessage
	if name == "" && len(shape.Function) > 0 {
		// A quoted string is a bare name.
		var bare string
		if err := json.Unmarshal(shape.Function, &bare); err == nil {
			name = strings.TrimSpace(bare)
		} else {
			var nested struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(shape.Function, &nested); err == nil {
				name = strings.TrimSpace(nested.Name)
				nestedArgs = nested.Arguments
			}
		}
	}
	if name == "" {
		return nil, content, false
	}

	// A wrapped "type":"function" shape is accepted; a typed object that
	// claims any other kind cannot be a tool call.
	if shape.Type != "" && shape.Type != "function" {
		return nil, content, false
	}

	if _, ok := ts.Find(name); !ok {
		return nil, content, false
	}

	args := shape.Args
	if len(args) == 0 {
		args = shape.Arguments
	}
	if len(args) == 0 {
		args = nestedArgs
	}
	if len(args) == 0 {
		return nil, content, false
	}
	// Arguments must be a JSON object — not an array, string, or number.
	if args[0] != '{' {
		return nil, content, false
	}

	stripped := strings.TrimSpace(content[:start] + content[end:])
	return []provider.ToolCall{{
		ID:   "call_lenient_1",
		Name: name,
		Args: args,
	}}, stripped, true
}

// findToolJSON locates a JSON object that looks like a tool call: a ```json
// fence, or a brace-balanced object starting at the first '{' in the content.
// The strict name/argument validation in parseLenientToolCalls is what keeps
// a mid-prose object from misfiring.
func findToolJSON(content string) (raw []byte, start, end int, ok bool) {
	// Fenced form: a ```json fence whose body is a single object.
	if i := strings.Index(content, "```json"); i >= 0 {
		bodyStart := i + len("```json")
		if j := strings.Index(content[bodyStart:], "```"); j >= 0 {
			body := strings.TrimSpace(content[bodyStart : bodyStart+j])
			if len(body) > 0 && body[0] == '{' && body[len(body)-1] == '}' {
				return []byte(body), i, bodyStart + j + len("```"), true
			}
		}
	}

	// Inline form: scan from the first '{' until the braces balance. Multi-line
	// objects are supported; the whole span must form one valid object.
	if i := strings.Index(content, "{"); i >= 0 {
		depth := 0
		for j := i; j < len(content); j++ {
			switch content[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return []byte(content[i : j+1]), i, j + 1, true
				}
			}
		}
	}
	return nil, 0, 0, false
}
