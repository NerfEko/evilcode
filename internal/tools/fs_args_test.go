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