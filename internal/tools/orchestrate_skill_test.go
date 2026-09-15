package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The orchestrate playbook ships in the repo skill dir so the tool
// description's pointer resolves.
func TestOrchestrateSkillShipsWithPlaybook(t *testing.T) {
	dir := filepath.Join("..", "..", ".agents", "skills")
	if _, err := os.Stat(filepath.Join(dir, "orchestrate", "SKILL.md")); err != nil {
		t.Skipf("orchestrate skill not present beside the repo: %v", err)
	}
	set := LoadSkills([]string{dir})
	found := false
	for _, name := range set.Names() {
		if name == "orchestrate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orchestrate not indexed in %s (have %s)", dir, strings.Join(set.Names(), ", "))
	}
	body, err := set.Body("orchestrate")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Decomposition test",
		"Brief format",
		"Sizing",
		"Waiting",
		"Merge rules",
		"Failure path",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("orchestrate skill is missing %q", want)
		}
	}
}

func TestSpawnDescPointsAtOrchestrateSkill(t *testing.T) {
	tool, ok := NewSpawn(&modelCaptureDouble{}).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	for _, want := range []string{"orchestrate", "batch"} {
		if !strings.Contains(tool.Desc, want) {
			t.Errorf("spawn_worker description is missing %q", want)
		}
	}
}
