// Package wiring builds a ready-to-run agent from configuration.
//
// It exists because `run`, `serve`, and the TUI each need the same twelve
// steps — resolve the model, open or resume the session, load project context
// and repo overrides, assemble tools, open the memory bank, attach hooks — and
// three copies of that drift. The TUI keeps its own copy of the parts that are
// genuinely interactive (skills in the prompt, the `ask` tool, MCP); everything
// headless comes through here.
package wiring

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/lsp"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
)

// Options select what to build.
type Options struct {
	// Model is an explicit `model@provider` reference, or "" for the default.
	Model string

	// Resume names an existing session; empty creates a new one.
	Resume string

	// Store, when set, is the session log to use — already created and named by
	// the caller. The daemon needs this: a worker's name has to be settled
	// before anything is built under it, and Build creating its own store is
	// what left a renamed worker writing to another session's log.
	Store *session.Store

	// Cwd is the workspace root. Empty means the process's directory.
	Cwd string

	// NoTools builds an agent with no tools at all.
	NoTools bool

	// Extract turns on ambient memory extraction. Off for one-shot runs: a
	// single turn never reaches the interval, and arming it would spend a
	// side-call per invocation for nothing (plan.md §19).
	Extract bool

	// TodoNamespace is the todo store every session in a swarm shares. Left
	// empty a session gets its own, which is what a solo run wants; set to one
	// name across a daemon it becomes the shared plan of §20, where a group a
	// worker closes is the same group its spawner is watching.
	TodoNamespace string

	// Todos and Bank are stores the caller already owns, shared by reference.
	//
	// A namespace names a set of files, and two stores over one set of files are
	// two divergent copies of it — each reading its own snapshot and writing the
	// whole file back. Sharing the *store* is what makes a namespace mean one
	// plan rather than one filename. The build does not close what it did not
	// open.
	Todos *todo.Store
	Bank  *memory.Store
}

// modelRefForResume returns the model reference to resolve for this build: an
// explicit Model wins, and a resume with no explicit model reuses the one the
// session remembers, so a daemon or headless resume lands on the same model the
// conversation left off with (§18). Empty falls through to the config default.
func modelRefForResume(dataDir string, opts Options) string {
	if opts.Model != "" {
		return opts.Model
	}
	if opts.Resume == "" {
		return ""
	}
	if info, err := session.Describe(dataDir, opts.Resume); err == nil {
		return info.Model
	}
	return ""
}

// Session is everything a caller has to hold onto and close.
type Session struct {
	Agent  *agent.Agent
	Store  *session.Store
	Memory *memory.Manager
	Config *config.Config

	// Todos is the session's plan state.
	Todos *todo.Store

	// Model is the resolved model name, which is not always the requested one.
	Model string

	// Prior is how many messages a resumed session replayed.
	Prior int

	closers []func()
}

// Close releases everything in reverse order of acquisition.
func (s *Session) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	s.closers = nil
}

// repoConfig returns a per-build copy of cfg with the repository's overrides
// applied, leaving the caller's config untouched.
func repoConfig(cfg *config.Config, root string) (*config.Config, error) {
	local := cfg.Clone()
	if err := local.LoadRepoOverrides(root); err != nil {
		return nil, err
	}
	return local, nil
}

// Build assembles a session. On error nothing is left open.
func Build(cfg *config.Config, opts Options) (*Session, error) {
	cwd := opts.Cwd
	if cwd == "" {
		return nil, fmt.Errorf("wiring: no workspace directory")
	}
	dataDir := config.DataDir()

	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	// Onto a copy: cfg belongs to the daemon and is shared by every session it
	// hosts. Applying a repository's overrides to it pinned one repo's model
	// for all of them, and raced two builds against each other. Done before
	// resolving, so a repo-pinned default_model actually takes effect on this
	// build rather than the pre-override config winning.
	cfg, err := repoConfig(cfg, pc.Root)
	if err != nil {
		return nil, err
	}

	prov, modelName, err := cfg.Resolve(modelRefForResume(dataDir, opts))
	if err != nil {
		return nil, err
	}

	out := &Session{Config: cfg, Model: modelName}

	var store *session.Store
	var prior []provider.Message
	switch {
	case opts.Store != nil:
		store = opts.Store
	case opts.Resume != "":
		st, msgs, rerr := session.Resume(dataDir, opts.Resume)
		if rerr != nil {
			return nil, rerr
		}
		store, prior = st, msgs
	default:
		if store, err = session.Create(dataDir); err != nil {
			return nil, err
		}
	}
	out.Store, out.Prior = store, len(prior)
	out.closers = append(out.closers, func() { store.Close() })

	// Record the model this build is on, so the session remembers it for a later
	// resume (§18). Matches the TUI and headless paths; last-write-wins on read.
	if err := store.WriteModel(config.ModelRef(modelName, prov.Name())); err != nil {
		store.Close()
		return nil, err
	}

	conv := agent.NewConversation(agent.BuildSystemPrompt(pc, nil, ""))
	conv.Append(prior...)
	// Registered *after* the replay: a resumed session appends what it just
	// read, and persisting that would double the file on every resume.
	conv.Persist(func(m provider.Message) error { return store.WriteMessage(m) })

	// Overrides are looked up by the *resolved* model, not the flag: a session
	// relying on default_model would otherwise silently get none of them.
	overrides := cfg.ModelOverrides(modelName)
	exposure := tools.NewExposure()
	var lsps *lsp.Manager
	if !opts.NoTools {
		// Search can use the same lazy language-server manager as the interactive
		// path, even though headless sessions do not expose the standalone lsp
		// tool. A session that never greps still pays no indexing cost.
		lsps = lsp.NewManager(pc.Root, cfg.LSP)
		out.closers = append(out.closers, lsps.Close)
	}

	var ts tools.Set
	if !opts.NoTools {
		if canned, ok := provider.DemoCannedTools(); ok {
			// A screen recording replaying a captured transcript: tool
			// *results* are canned too, so it never depends on a particular
			// repo being checked out at record time (plan.md §14 covers the
			// model side of this; this is the same idea for tools).
			ts = tools.Canned(canned)
		} else {
			execTools := tools.NewExec(cwd).
				WithExposure(exposure).
				WithScratchDir(filepath.Join(dataDir, "scratch")).
				WithRiskPaths(config.ConfigDir(), dataDir)
			if lsps != nil {
				execTools.WithLSP(lsps)
			}
			ts = append(tools.NewFS(cwd).WithAnchors(overrides.AnchorEdits).
				WithConfine(cfg.Features.ConfineToWorkspace).WithVision(overrides.Vision).
				WithExposure(exposure).Tools(),
				execTools.Tools()...)
			ts = append(ts, tools.NewGit(pc.Root).Tools()...)
		}
		// No `ask` tool: a headless session has nobody to ask, and a tool that
		// is present and always fails is worse than one that is absent.
	}

	a := agent.New(store.Name, prov, modelName, ts, conv)
	// An explicit [[model]] context_window wins; otherwise the provider is
	// asked, so a discovered window drives the meter and compaction instead
	// of the hardcoded guess behind them.
	a.NumCtx = config.ContextWindowFor(prov, modelName, overrides.ContextWindow)
	a.MaxSteps = cfg.Features.MaxSteps

	// Compaction reaches headless and the daemon too. It was a *tui.Model
	// method, so a long daemon session, an overnight run and every spawned
	// worker had no way to compact at all.
	a.Compactor = &agent.Compactor{
		Summarize: func(ctx context.Context, system, user string) (string, error) {
			return cfg.Router().SideCall(ctx, config.RoleSmol, system, user)
		},
		Persist: func(summary string) ([]provider.Message, error) {
			return store.Compact(dataDir, summary)
		},
		OnCompaction: exposure.Reset,
	}
	out.Agent = a
	out.closers = append(out.closers, a.Close)

	// The todo store is shared across a swarm when a namespace is named, so
	// "the auth flow" means one group to every agent rather than N private
	// lists that happen to share a word (plan.md §20).
	todoName := opts.TodoNamespace
	if todoName == "" {
		todoName = store.Name
	}
	todos := opts.Todos
	if todos == nil {
		var terr error
		if todos, terr = todo.NewStore(dataDir, todoName); terr != nil {
			if opts.TodoNamespace != "" {
				// A named namespace is a swarm's shared plan: building the session
				// anyway would give it silent, private todo state instead of the
				// coordination it was configured to have (plan.md §20).
				out.Close()
				return nil, fmt.Errorf("todo store %q: %w", todoName, terr)
			}
			fmt.Fprintln(os.Stderr, "evilcode: todo store unavailable:", terr)
			todos = nil
		}
	}
	if todos != nil {
		out.Todos = todos
		if !opts.NoTools {
			a.Tools = append(a.Tools, tools.NewTodo(todos, nil))
		}
	}

	// A memory bank that will not open disables memory rather than failing the
	// build: it is an enhancement, not a prerequisite (plan.md §19).
	bank := opts.Bank
	owned := false
	if bank == nil {
		var berr error
		if bank, berr = memory.Open(dataDir); berr != nil {
			bank = nil
		} else {
			owned = true
		}
	}
	if bank != nil {
		mem := memory.NewManagerWithModel(bank, prov, cfg.Router(), store.Name, cfg.Features.Memory, prov.Name()+"::embedding")
		out.Memory = mem
		if owned {
			out.closers = append(out.closers, func() { bank.Close() })
		}

		if !opts.NoTools {
			a.Tools = append(a.Tools, tools.NewMemory(mem)...)
		}
		a.Recall = func(ctx context.Context, in string) (string, any) {
			return mem.Recall(ctx, in)
		}
		if opts.Extract {
			hook := agent.NewMemoryHook(mem)
			a.Hooks = hook
			out.closers = append(out.closers, hook.Close)
		}
	}
	return out, nil
}
