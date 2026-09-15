package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkerKnobs(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Features)
		want   string
	}{
		{"negative live workers", func(f *Features) { f.MaxLiveWorkers = -1 }, "features.max_live_workers"},
		{"huge live workers", func(f *Features) { f.MaxLiveWorkers = maxConfigLiveWorkers + 1 }, "features.max_live_workers"},
		{"negative worker steps", func(f *Features) { f.WorkerMaxSteps = -1 }, "features.worker_max_steps"},
		{"huge worker steps", func(f *Features) { f.WorkerMaxSteps = maxConfigSteps + 1 }, "features.worker_max_steps"},
		{"bad worker model shape", func(f *Features) { f.DefaultWorkerModel = "not a ref @@" }, "features.default_worker_model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg.Features)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want a problem at %q", err, tc.want)
			}
		})
	}

	cfg = Default()
	cfg.Features.MaxLiveWorkers = 2
	cfg.Features.WorkerMaxSteps = 10
	cfg.Features.WorkerSpawning = true
	cfg.Features.DefaultWorkerModel = "small@mock"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid worker knobs do not validate: %v", err)
	}
}

func TestWorkerKnobsRoundTripTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "default_model = \"m@mock\"\n" +
		"[[provider]]\nname = \"mock\"\nkind = \"mock\"\n" +
		"[features]\nmax_live_workers = 2\nworker_max_steps = 10\n" +
		"worker_spawning = true\norchestrate_keyword = false\n" +
		"default_worker_model = \"small@mock\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	f := cfg.Features
	if f.MaxLiveWorkers != 2 || f.WorkerMaxSteps != 10 || !f.WorkerSpawning ||
		f.OrchestrateKeyword || f.DefaultWorkerModel != "small@mock" {
		t.Fatalf("worker knobs did not round-trip: %+v", f)
	}
}

func TestRepoOverridesPinDefaultWorkerModel(t *testing.T) {
	dir := t.TempDir()
	body := "[features]\ndefault_worker_model = \"small@mock\"\n"
	if err := os.WriteFile(filepath.Join(dir, RepoConfigName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	if err := cfg.LoadRepoOverrides(dir); err != nil {
		t.Fatal(err)
	}
	if cfg.Features.DefaultWorkerModel != "small@mock" {
		t.Fatalf("repo default_worker_model = %q", cfg.Features.DefaultWorkerModel)
	}
}
