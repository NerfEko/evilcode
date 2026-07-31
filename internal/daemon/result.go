package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// ExtractJSON pulls the JSON value out of a worker's final message.
//
// Models wrap JSON in fences and preambles no matter how firmly the prompt says
// not to, and rejecting a correct answer over a code fence would make schema
// validation useless in practice. What it will not do is guess at prose — a
// message with no JSON in it comes back as a failure, which is the whole point
// of asking for a schema (plan.md §20).
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if fence := strings.Index(s, "```"); fence >= 0 {
		rest := s[fence+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	}

	// The outermost object or array, whichever starts first.
	obj, arr := strings.Index(s, "{"), strings.Index(s, "[")
	start, open, closing := obj, byte('{'), byte('}')
	if obj < 0 || (arr >= 0 && arr < obj) {
		start, open, closing = arr, '[', ']'
	}
	if start < 0 {
		return ""
	}

	depth := 0
	inString, escaped := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Braces inside a string are text, not structure.
		case c == open:
			depth++
		case c == closing:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// compileSchema resolves a JSON Schema for validation.
//
// jsonschema-go rather than a hand-rolled subset: it is already in the module
// graph as the MCP SDK's dependency, so using it costs nothing at build time,
// and a partial validator is the worst of both — it silently passes documents
// it does not understand, which means a spawner cannot trust a pass.
func compileSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}

// ValidateResult checks a worker's final message against the schema its spawner
// supplied, returning the validated JSON.
//
// The point is that a spawner never parses prose. It gets a value it can index
// or an error saying why it could not — not a paragraph it has to interpret,
// which is where multi-agent systems quietly go wrong.
func ValidateResult(output string, schema json.RawMessage) (string, error) {
	if len(schema) == 0 {
		return strings.TrimSpace(output), nil
	}
	body := ExtractJSON(output)
	if body == "" {
		return "", fmt.Errorf("the worker returned no JSON")
	}

	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return "", fmt.Errorf("the worker's output is not valid JSON: %w", err)
	}
	resolved, err := compileSchema(schema)
	if err != nil {
		return "", fmt.Errorf("the supplied schema is not usable: %w", err)
	}
	if err := resolved.Validate(value); err != nil {
		return "", fmt.Errorf("the worker's output does not match the schema: %w", err)
	}
	return body, nil
}
