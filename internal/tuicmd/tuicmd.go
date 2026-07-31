// Package tuicmd wires configuration, session storage, and the agent into the
// interactive TUI.
package tuicmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/session"
	"evilcode/internal/tools"
	"evilcode/internal/tui"
)

// Version is the build's version string.
const Version = "v0.1.0"

// Run starts the interactive TUI.
func Run(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	model := fs.String("m", "", "model reference, e.g. qwen3-coder:480b-cloud@ollama-cloud")
	resume := fs.String("resume", "", "resume a named session")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	prov, modelName, err := cfg.Resolve(*model)
	if err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	dataDir := config.DataDir()

	var store *session.Store
	var prior int
	if *resume != "" {
		st, msgs, err := session.Resume(dataDir, *resume)
		if err != nil {
			return err
		}
		store, prior = st, len(msgs)
	} else {
		if store, err = session.Create(dataDir); err != nil {
			return err
		}
	}
	defer store.Close()

	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	conv := agent.NewConversation(agent.BuildSystemPrompt(pc, nil, ""))
	if prior > 0 {
		_, msgs, err := session.Resume(dataDir, *resume)
		if err != nil {
			return err
		}
		conv.Append(msgs...)
	}

	ts := append(tools.NewFS(cwd).Tools(), tools.NewExec(cwd).Tools()...)

	a := agent.New(store.Name, prov, modelName, ts, conv)
	a.NumCtx = cfg.ModelOverrides(*model).ContextWindow
	defer a.Close()

	return tui.Run(a, headerState(cfg, store.Name, modelName, prov.Name(), cwd))
}

func headerState(cfg *config.Config, sessionName, model, providerName, cwd string) tui.HeaderState {
	h := tui.HeaderState{
		SessionName: sessionName,
		Version:     Version,
		Provider:    providerName,
		Model:       model,
		Cwd:         prettyPath(cwd),
		Branch:      gitBranch(cwd),
	}
	for _, p := range cfg.Providers {
		h.Providers = append(h.Providers, tui.ProviderStatus{
			Name: p.Name,
			// A provider with no key configured is reachable only if it needs
			// none, which is how a local daemon shows as ready.
			Ready: p.APIKeyValue() != "" || p.APIKeyEnv == "",
		})
		if p.Name == providerName {
			if p.APIKeyValue() != "" {
				h.AuthKind = "api-key"
			} else {
				h.AuthKind = "local"
			}
		}
	}
	return h
}

// prettyPath shortens a home-relative path to `~/...`.
func prettyPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}

func gitBranch(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Usage prints the subcommand's flags.
func Usage() string {
	return fmt.Sprintf("evilcode tui [-m model] [-resume name]  (%s)", Version)
}
