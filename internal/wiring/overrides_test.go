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

// H5.18: Build used to resolve the model before applying repo overrides, so a
// repo-pinned default_model never took effect — Resolve had already run
// against the pre-override config by the time the pin was even loaded.
func TestBuildResolvesAgainstTheRepoPinnedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, config.RepoConfigName),
		[]byte("default_model = \"pinned-model@mock\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.DefaultModel = "unpinned-model@mock"

	sess, err := Build(cfg, Options{Cwd: repo, NoTools: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if sess.Model != "pinned-model" {
		t.Errorf("resolved model = %q, want the repo's pinned model", sess.Model)
	}
}
