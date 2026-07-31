package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"evilcode/internal/config"
)

// H2.10: repo overrides were applied by mutating the config object the daemon
// shares across every session. One repo's pinned model then applies to sessions
// in other directories, and two concurrent builds mutate it at once.
func TestRepoOverridesDoNotLeakIntoTheSharedConfig(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, config.RepoConfigName),
		[]byte("default_model = \"pinned-by-the-repo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shared := config.Default()
	before := shared.DefaultModel

	if _, err := repoConfig(shared, repo); err != nil {
		t.Fatal(err)
	}
	if shared.DefaultModel != before {
		t.Errorf("the shared config's default model became %q after building a session "+
			"in a repo that pins %q; every other session now uses it too",
			shared.DefaultModel, "pinned-by-the-repo")
	}
}

// And the override must actually reach the session being built.
func TestRepoOverridesApplyToTheSessionBeingBuilt(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, config.RepoConfigName),
		[]byte("default_model = \"pinned-by-the-repo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shared := config.Default()
	local, err := repoConfig(shared, repo)
	if err != nil {
		t.Fatal(err)
	}
	if local.DefaultModel != "pinned-by-the-repo" {
		t.Errorf("the built session's config has default model %q, want the repo's pin",
			local.DefaultModel)
	}
}
