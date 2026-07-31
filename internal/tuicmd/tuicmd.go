// Package tuicmd wires configuration, session storage, and the agent into the
// interactive TUI.
package tuicmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/mcp"
	"evilcode/internal/session"
	"evilcode/internal/todo"
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

	// A repo may pin its roles and default model.
	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return err
	}

	// Skills contribute only their names and one-liners to the prompt; bodies
	// load through the skill tool, which keeps the prompt cacheable as the set
	// grows (plan.md §15).
	skills := tools.LoadSkills(tools.SkillDirs(pc.Root, config.ConfigDir()))
	var promptSkills []agent.Skill
	for _, sk := range skills.Index() {
		promptSkills = append(promptSkills, agent.Skill{Name: sk.Name, Desc: sk.Desc, Path: sk.Path})
	}

	// A server that is not installed is a normal state, not a startup failure.
	mcpClient := mcp.New()
	var mcpServers []mcp.ServerConfig
	for _, srv := range cfg.MCP {
		mcpServers = append(mcpServers, mcp.ServerConfig{
			Name: srv.Name, Command: srv.Command, Args: srv.Args, Env: srv.Env,
		})
	}
	for _, err := range mcpClient.Connect(context.Background(), mcpServers) {
		fmt.Fprintln(os.Stderr, "evilcode:", err)
	}
	defer mcpClient.Close()
	conv := agent.NewConversation(agent.BuildSystemPrompt(pc, promptSkills, ""))
	if prior > 0 {
		_, msgs, err := session.Resume(dataDir, *resume)
		if err != nil {
			return err
		}
		conv.Append(msgs...)
	}

	todos, err := todo.NewStore(dataDir, store.Name)
	if err != nil {
		return err
	}

	a := agent.New(store.Name, prov, modelName, nil, conv)
	poke := agent.NewPokeHook(todos, cfg.Features.AutoPoke)
	a.Hooks = poke

	prompts, err := session.OpenHistory(dataDir)
	if err != nil {
		return err
	}

	keymap, problems := tui.NewKeymap(cfg.Keybindings)
	for _, p := range problems {
		// A bad binding is reported rather than silently dropped: a rebind that
		// does nothing is worse than one that says why.
		fmt.Fprintln(os.Stderr, "evilcode: "+p)
	}

	var mcpStatus []tui.MCPStatus
	for _, s := range mcpClient.Summaries() {
		mcpStatus = append(mcpStatus, tui.MCPStatus{Name: s.Name, Tools: s.Tools})
	}

	// Look overrides up by the *resolved* model, not the flag. Passing the
	// flag means a session relying on default_model silently gets no
	// per-model settings at all, which is how anchor_edits appeared to be
	// broken when it was simply never switched on.
	overrides := cfg.ModelOverrides(modelName)
	fsTools := tools.NewFS(cwd).WithAnchors(overrides.AnchorEdits).
		WithConfine(cfg.Features.ConfineToWorkspace)
	execTools := tools.NewExec(cwd)

	m := tui.NewModel(a, headerState(cfg, store.Name, modelName, prov.Name(), cwd,
		skills.Names(), mcpStatus)).
		WithTodos(todos, poke).
		WithHistory(prompts).
		WithKeymap(keymap, tui.LoadHotkeyUsage(dataDir), cfg.Display.KeybindingHints).
		WithSessions(dataDir, cwd, store).
		WithBackground(execTools.Bg)
	if prior > 0 {
		m.RebuildFrom(conv.Messages())
	}

	ts := append(fsTools.Tools(), execTools.Tools()...)
	ts = append(ts, tools.NewGit(pc.Root).Tools()...)
	ts = append(ts, tools.NewTodo(todos, nil))
	ts = append(ts, tools.NewAsk(m.Asker()))
	if len(promptSkills) > 0 {
		ts = append(ts, tools.NewSkillTool(skills))
	}
	ts = append(ts, mcpClient.Tools()...)
	a.Tools = ts

	a.NumCtx = overrides.ContextWindow
	defer a.Close()

	if err := tui.RunModel(m); err != nil {
		return err
	}
	// The session picker exits with a target rather than swapping state in
	// place: the agent, todo store, history, and breakers are each bound to one
	// session, and re-entering is a much smaller surface than rebuilding them.
	if target := m.ResumeTarget(); target != "" {
		return Run(append([]string{"-resume", target}, modelArgs(*model)...))
	}
	return nil
}

// modelArgs preserves an explicit -m across a session switch.
func modelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"-m", model}
}

func headerState(cfg *config.Config, sessionName, model, providerName, cwd string,
	skillNames []string, mcpSummaries []tui.MCPStatus) tui.HeaderState {
	h := tui.HeaderState{
		SessionName: sessionName,
		Version:     Version,
		Provider:    providerName,
		Model:       model,
		Cwd:         prettyPath(cwd),
		Branch:      gitBranch(cwd),
		Skills:      skillNames,
		MCP:         mcpSummaries,
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
