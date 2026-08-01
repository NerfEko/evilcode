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
	"evilcode/internal/graphics"
	"evilcode/internal/lsp"
	"evilcode/internal/mcp"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
	"evilcode/internal/tui"
)

// Version is the build's version string.
var Version = "v0.1.0"

// Run starts the interactive TUI.
// Run starts the interactive TUI, and re-enters it for each session switch.
//
// A loop rather than recursion. Switching used to call Run again from inside
// itself, so the outer frame's defers — the session store, the MCP client, the
// LSP manager, the agent, the memory bank — did not run until the final unwind.
// N switches meant N live sets of every MCP server process and language server,
// all of them idle and none of them reachable.
func Run(args []string) error {
	return runSessions(args, runOnce)
}

// runSessions drives one session at a time, letting each tear down before the
// next begins.
func runSessions(args []string, once func([]string) (string, error)) error {
	model := ""
	for i, a := range args {
		if a == "-m" && i+1 < len(args) {
			model = args[i+1]
		}
	}
	for {
		target, err := once(args)
		if err != nil || target == "" {
			return err
		}
		args = append([]string{"-resume", target}, modelArgs(model)...)
	}
}

// runOnce runs one session and returns the session to switch to, if any.
func runOnce(args []string) (string, error) {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	model := fs.String("m", "", "model reference, e.g. qwen3-coder:480b-cloud@ollama-cloud")
	resume := fs.String("resume", "", "resume a named session")
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	cfg, err := config.Load()
	if err != nil {
		return "", err
	}

	cwd, _ := os.Getwd()
	dataDir := config.DataDir()

	// A repo may pin its roles and default model. Loaded before resolving, so
	// a repo-pinned default_model actually takes effect on this run.
	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return "", err
	}
	prov, modelName, err := cfg.Resolve(*model)
	if err != nil {
		return "", err
	}

	var store *session.Store
	var priorMessages []provider.Message
	if *resume != "" {
		st, msgs, err := session.Resume(dataDir, *resume)
		if err != nil {
			return "", err
		}
		store, priorMessages = st, msgs
	} else {
		if store, err = session.Create(dataDir); err != nil {
			return "", err
		}
	}
	defer store.Close()

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
	if len(priorMessages) > 0 {
		// The messages from the resume above, not a second one. Resuming twice
		// re-parsed the whole file and discarded the store it returned, leaking
		// the descriptor for the life of the session.
		conv.Append(priorMessages...)
	}

	// The JSONL file is the source of truth (§18), so every message goes to it
	// as it lands. Registered after any replay, or a resume would rewrite what
	// it had just read.
	conv.Persist(func(m provider.Message) error { return store.WriteMessage(m) })

	todos, err := todo.NewStore(dataDir, store.Name)
	if err != nil {
		return "", err
	}

	a := agent.New(store.Name, prov, modelName, nil, conv)
	poke := agent.NewPokeHook(todos, cfg.Features.AutoPoke)

	// A memory bank that fails to open is a missing feature, not a failed
	// start: the whole subsystem is an enhancement, and refusing to boot
	// because a JSONL file is unreadable would be the wrong trade (§19).
	var mem *memory.Manager
	if bank, err := memory.Open(dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "evilcode: memory unavailable:", err)
	} else {
		defer bank.Close()
		mem = memory.NewManager(bank, prov, cfg.Router(), store.Name, cfg.Features.Memory)
		a.Recall = func(ctx context.Context, in string) (string, any) {
			tail, hits := mem.Recall(ctx, in)
			return tail, hits
		}
	}

	// Memory first in the chain: it never appends, so it must not sit behind a
	// hook that does, or an auto-poked turn is never observed.
	memoryHook := agent.NewMemoryHook(mem)
	defer memoryHook.Close()
	a.Hooks = agent.Chain{memoryHook, poke}

	// Compaction persists through the session store rather than only in memory:
	// assigning the message slice was what made a compacted session come back
	// uncompacted on resume.
	compactor := &agent.Compactor{
		Summarize: func(ctx context.Context, system, user string) (string, error) {
			return cfg.Router().SideCall(ctx, config.RoleSmol, system, user)
		},
		Persist: func(summary string) ([]provider.Message, error) {
			return store.Compact(dataDir, summary)
		},
	}
	a.Compactor = compactor

	prompts, err := session.OpenHistory(dataDir)
	if err != nil {
		return "", err
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

	// Language servers start on first use, not here: gopls costs seconds and
	// indexes the module, and a session that never asks should never pay.
	lsps := lsp.NewManager(pc.Root, cfg.LSP)
	defer lsps.Close()

	// The advisor is a second, cheap voice, so it goes through the smol role
	// like every other ambient call (§16, §21).
	advisor := agent.NewAdvisor(func(ctx context.Context, system, user string) (string, error) {
		return cfg.Router().SideCall(ctx, config.RoleSmol, system, user)
	}, cfg.Features.Advisor)
	advisor.TodoState = todos.Summary

	// Last in the chain. It never appends, so it cannot starve anything, and
	// putting it after auto-poke is what makes "one arguing voice at a time"
	// true rather than aspirational: by the time it runs, poke has already
	// decided whether it has something to say.
	a.Hooks = append(a.Hooks.(agent.Chain), advisor)

	m := tui.NewModel(a, headerState(cfg, store.Name, modelName, prov.Name(), cwd,
		skills.Names(), mcpStatus)).
		WithTodos(todos, poke).
		WithHistory(prompts).
		WithKeymap(keymap, tui.LoadHotkeyUsage(dataDir), cfg.Display.KeybindingHints).
		WithDisplay(cfg.Display).
		WithSessions(dataDir, cwd, store).
		WithBackground(execTools.Bg).
		WithGraphics(graphics.Detect(), filepath.Join(dataDir, "diagrams")).
		WithMemory(mem).
		WithAdvisor(advisor, lsps).
		WithCompactor(compactor).
		WithVision(overrides.Vision)
	if len(priorMessages) > 0 {
		m.RebuildFrom(conv.Messages())
	}

	ts := append(fsTools.Tools(), execTools.Tools()...)
	ts = append(ts, tools.NewGit(pc.Root).Tools()...)
	ts = append(ts, tools.NewTodo(todos, nil))
	ts = append(ts, tools.NewAsk(m.Asker()))
	if len(promptSkills) > 0 {
		ts = append(ts, tools.NewSkillTool(skills))
	}
	if mem != nil {
		ts = append(ts, tools.NewMemory(mem)...)
	}

	ts = append(ts, tools.NewLSP(lsps))
	ts = append(ts, mcpClient.Tools()...)
	a.Tools = ts

	// An explicit [[model]] context_window wins; otherwise the provider is
	// asked, so a discovered window drives the meter and compaction instead
	// of the hardcoded guess behind them.
	a.NumCtx = config.ContextWindowFor(prov, modelName, overrides.ContextWindow)
	a.MaxSteps = cfg.Features.MaxSteps
	defer a.Close()

	if err := tui.RunModel(m); err != nil {
		return "", err
	}
	// After the TUI is down, so a slow summary shows as a pause at the prompt
	// rather than a frozen frame.
	m.ConsolidateMemory()
	// A reload re-execs rather than rebuilding state in place: the binary may
	// literally be a different one after /rebuild, and every subsystem here is
	// bound to a session anyway.
	if target := m.ReloadTarget(); target != "" {
		return "", tui.Reexec(target)
	}

	// The session picker exits with a target rather than swapping state in
	// place: the agent, todo store, history, and breakers are each bound to one
	// session, and re-entering is a much smaller surface than rebuilding them.
	//
	// Returned rather than recursed into, so this frame's defers run first.
	return m.ResumeTarget(), nil
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
