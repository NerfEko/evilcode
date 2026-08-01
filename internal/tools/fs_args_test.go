package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// J1.7: a known alias is repaired before the strict decode, and the repair is
// recorded on the result so the tool row can show it.
func TestReadAcceptsFilePathAliasAndRecordsRepair(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"file_path": "a.txt"})
	if err != nil {
		t.Fatalf("file_path alias rejected: %v", err)
	}
	if !strings.Contains(res.Output, "one") {
		t.Errorf("output = %q, want the file read via the alias", res.Output)
	}
	if len(res.Repairs) == 0 || !containsJoin(res.Repairs, "file_path→path") {
		t.Errorf("repairs = %v, want file_path→path recorded", res.Repairs)
	}
}

// A number given as a string is coerced for a numeric field, and recorded.
func TestReadCoercesStringOffsetAndRecordsRepair(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "1\n2\n3\n4\n5\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt", "offset": "2", "limit": "2"})
	if err != nil {
		t.Fatalf("string offset/limit rejected: %v", err)
	}
	if !strings.Contains(res.Output, "2\t2") {
		t.Errorf("output = %q, want offset 2 applied", res.Output)
	}
	if !containsJoin(res.Repairs, "offset: string→number") || !containsJoin(res.Repairs, "limit: string→number") {
		t.Errorf("repairs = %v, want offset and limit string→number", res.Repairs)
	}
}

// `pattern`→`query` applies only to a tool whose schema has `query`, not to one
// whose real field is `pattern` (grep). repairArgs is schema-conditional.
func TestRepairArgsPatternToQueryIsSchemaConditional(t *testing.T) {
	querySchema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
	repaired, repairs := repairArgs(json.RawMessage(`{"pattern":"x"}`), querySchema)
	if !strings.Contains(string(repaired), `"query":"x"`) {
		t.Errorf("repaired = %s, want pattern renamed to query", repaired)
	}
	if !containsJoin(repairs, "pattern→query") {
		t.Errorf("repairs = %v, want pattern→query", repairs)
	}

	// grep's schema has `pattern`, so the alias must NOT fire.
	grepSchema := json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"glob":{"type":"string"}}}`)
	repaired2, repairs2 := repairArgs(json.RawMessage(`{"pattern":"x"}`), grepSchema)
	if !strings.Contains(string(repaired2), `"pattern":"x"`) {
		t.Errorf("repaired = %s, pattern must be left alone for a pattern-schema tool", repaired2)
	}
	if len(repairs2) != 0 {
		t.Errorf("repairs = %v, want none for a pattern-schema tool", repairs2)
	}
}

// grep with `pattern` (its real field) is unaffected: no repair, still works.
func TestGrepPatternUnaffectedByQueryAlias(t *testing.T) {
	e := NewExec(t.TempDir())
	res, err := run(t, e.Tools(), "grep", map[string]any{"pattern": "func New"})
	if err != nil {
		t.Fatalf("grep pattern rejected: %v", err)
	}
	_ = res
	if len(res.Repairs) != 0 {
		t.Errorf("grep repairs = %v, want none (pattern is grep's real field)", res.Repairs)
	}
}

// A call that already uses the real name pays no repair.
func TestNoRepairWhenRealNameUsed(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repairs) != 0 {
		t.Errorf("repairs = %v, want none when the real name is used", res.Repairs)
	}
}

func containsJoin(parts []string, want string) bool {
	for _, p := range parts {
		if p == want {
			return true
		}
	}
	return false
}
// A non-finite numeric string ("NaN") must not be coerced: leaving it a string
// makes strict decode reject the call, which is the honest outcome for an
// argument that is not a real number.
func TestRepairArgsRejectsNonFiniteNumbers(t *testing.T) {
	querySchema := json.RawMessage(`{"type":"object","properties":{"timeout":{"type":"integer"}}}`)
	for _, bad := range []string{"NaN", "+Inf", "-Inf"} {
		repaired, repairs := repairArgs(
			json.RawMessage(`{"timeout":"`+bad+`"}`), querySchema)
		if len(repairs) != 0 {
			t.Errorf("timeout=%q: repairs = %v, want none (must not coerce non-finite)", bad, repairs)
		}
		if strings.Contains(string(repaired), `"timeout":`) {
			// The field must still be a string so strict decode rejects it.
			if !strings.Contains(string(repaired), `"timeout":"`+bad+`"`) {
				t.Errorf("timeout=%q: repaired = %s, want the string left intact", bad, repaired)
			}
		}
	}
}

// Numeric coercion recurses into nested schema fields (todo's items[].confidence).
func TestRepairArgsCoercesNestedNumbers(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
	  "items":{"type":"array","items":{"type":"object","properties":{
	    "confidence":{"type":"integer"}}}},
	  "plan":{"type":"object","properties":{"max_steps":{"type":"integer"}}}
	}}`)
	repaired, repairs := repairArgs(json.RawMessage(`{
	  "items":[{"confidence":"90"}],
	  "plan":{"max_steps":"7"}
	}`), schema)
	if !strings.Contains(string(repaired), `"confidence":90`) {
		t.Errorf("repaired = %s, want items[].confidence coerced to 90", repaired)
	}
	if !strings.Contains(string(repaired), `"max_steps":7`) {
		t.Errorf("repaired = %s, want plan.max_steps coerced to 7", repaired)
	}
	if !containsJoin(repairs, "items.confidence: string→number") ||
		!containsJoin(repairs, "plan.max_steps: string→number") {
		t.Errorf("repairs = %v, want nested coercion recorded", repairs)
	}
}
