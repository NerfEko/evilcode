package session

import (
	"testing"

	"evilcode/internal/provider"
)

// Repairs survive a session round-trip: they are persisted on the message and
// restored, so a resumed session's tool row shows them.
func TestRepairsRoundTripThroughSessionEncoding(t *testing.T) {
	m := provider.Message{
		Role: provider.RoleTool, ToolName: "read", Content: "ok",
		Repairs: []string{"file_path→path", "offset: string→number"},
	}
	data, err := encodeMessage("/tmp/x.jsonl", m)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeMessage("/tmp/x.jsonl", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Repairs) != 2 || back.Repairs[0] != "file_path→path" {
		t.Errorf("round-tripped Repairs = %v, want both preserved", back.Repairs)
	}
}
