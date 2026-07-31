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

	"evilcode/internal/agent"
	"evilcode/internal/config"
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

// Build assembles a session. On error nothing is left open.
func Build(cfg *config.Config, opts Options) (*Session, error) {
	prov, modelName, err := cfg.Resolve(opts.Model)
	if err != nil {
		return nil, err
	}

	cwd := opts.Cwd
	if cwd == "" {
		return nil, fmt.Errorf("wiring: no workspace directory")
	}
	dataDir := config.DataDir()

	out := &Session{Config: cfg, Model: modelName}
	// Any failure past this point unwinds what has already been opened, so a
	// half-built session never leaks a file handle.
	fail := func(err error) (*Session, error) {
		out.Close()
		return nil, err
	}

	var store *session.Store
	var prior []provider.Message
	if opts.Resume != "" {
		st, msgs, rerr := session.Resume(dataDir, opts.Resume)
		if rerr != nil {
			return nil, rerr
		}
		store, prior = st, msgs
	} else {
		if store, err = session.Create(dataDir); err != nil {
			return nil, err
		}
	}
	out.Store, out.Prior = store, len(prior)
	out.closers = append(out.closers, func() { store.Close() })

	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return fail(err)
	}

	conv := agent.NewConversation(agent.BuildSystemPrompt(pc, nil, ""))
	conv.Append(prior...)
	// Registered *after* the replay: a resumed session appends what it just
	// read, and persisting that would double the file on every resume.
	conv.Persist(func(m provider.Message) { store.WriteMessage(m) })

	// Overrides are looked up by the *resolved* model, not the flag: a session
	// relying on default_model would otherwise silently get none of them.
	overrides := cfg.ModelOverrides(modelName)

	var ts tools.Set
	if !opts.NoTools {
		ts = append(tools.NewFS(cwd).WithAnchors(overrides.AnchorEdits).
			WithConfine(cfg.Features.ConfineToWorkspace).Tools(),
			tools.NewExec(cwd).Tools()...)
		ts = append(ts, tools.NewGit(pc.Root).Tools()...)
		// No `ask` tool: a headless session has nobody to ask, and a tool that
		// is present and always fails is worse than one that is absent.
	}

	a := agent.New(store.Name, prov, modelName, ts, conv)
	a.NumCtx = overrides.ContextWindow

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
	if todos, terr := todo.NewStore(dataDir, todoName); terr == nil {
		out.Todos = todos
		if !opts.NoTools {
			a.Tools = append(a.Tools, tools.NewTodo(todos, nil))
		}
	}

	// A memory bank that will not open disables memory rather than failing the
	// build: it is an enhancement, not a prerequisite (plan.md §19).
	if bank, berr := memory.Open(dataDir); berr == nil {
		mem := memory.NewManager(bank, prov, cfg.Router(), store.Name, cfg.Features.Memory)
		out.Memory = mem
		out.closers = append(out.closers, func() { bank.Close() })

		if !opts.NoTools {
			a.Tools = append(a.Tools, tools.NewMemory(mem)...)
		}
		a.Recall = func(ctx context.Context, in string) (string, any) {
			return mem.Recall(ctx, in)
		}
		if opts.Extract {
			a.Hooks = agent.NewMemoryHook(mem)
		}
	}
	return out, nil
}
