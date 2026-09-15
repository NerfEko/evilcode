package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickGruntNameNumbersTheCrew(t *testing.T) {
	dir := t.TempDir()
	if got := PickGruntName(dir); got != "grunt-1" {
		t.Fatalf("first grunt = %q, want grunt-1", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"grunt-1", "grunt-2", "grunt-4", "wisp-13", "grunt-3-2"} {
		if err := os.WriteFile(filepath.Join(dir, "sessions", name+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// High-water mark is 4 (grunt-3-2 is a collision suffix, not a number),
	// so the next proposal is grunt-5.
	if got := PickGruntName(dir); got != "grunt-5" {
		t.Fatalf("next grunt = %q, want grunt-5", got)
	}
}

func TestSpawnedWorkersAreNamedGrunts(t *testing.T) {
	// Covered in the daemon package (it owns spawn); this guards the
	// contract at the source: grunt names never collide with creature names.
	dir := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		name := PickGruntName(dir)
		if !strings.HasPrefix(name, "grunt-") {
			t.Fatalf("worker name = %q, want the grunt- prefix", name)
		}
		if seen[name] {
			t.Fatalf("duplicate grunt proposal %q", name)
		}
		seen[name] = true
		st, err := CreateNamedAt(dir, name, dir)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	if got := PickGruntName(dir); got != "grunt-4" {
		t.Fatalf("after 3 claims next = %q, want grunt-4", got)
	}
}
