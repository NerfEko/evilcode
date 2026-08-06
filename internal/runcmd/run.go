// Package runcmd implements `evilcode run`: a headless one-shot turn that
// prints the event stream (plan.md Phase 1). It exists as much to prove the
// invariant-1 boundary as to be useful — the TUI and the daemon consume this
// same stream.
package runcmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/lsp"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/tools"
)

// Exit codes, so a script can tell what happened without parsing output.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitInterrupted = 130
)

// Run executes one headless turn and returns a process exit code.
func Run(args []string) (int, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	model := fs.String("m", "", "model reference, e.g. qwen3-coder:480b-cloud@ollama-cloud")
	resume := fs.String("resume", "", "resume a named session")
	quiet := fs.Bool("q", false, "print only the model's text, no tool or usage lines")
	noTools := fs.Bool("no-tools", false, "run without any tools")
	remote := fs.Bool("remote", false, "submit into a running daemon instead of executing locally")
	socket := fs.String("socket", "", "daemon socket path, with -remote")
	if err := fs.Parse(args); err != nil {
		return ExitError, err
	}

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		// Reading from a pipe makes `evilcode run` composable with the shell.
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
			piped, _ := io.ReadAll(os.Stdin)
			prompt = strings.TrimSpace(string(piped))
		}
	}
	if prompt == "" {
		return ExitError, fmt.Errorf(`usage: evilcode run [-m model] [-q] "prompt"`)
	}

	if *remote {
		return runRemote(*socket, *resume, prompt, *quiet)
	}

	cfg, err := config.Load()
	if err != nil {
		return ExitError, err
	}

	cwd, _ := os.Getwd()
	dataDir := config.DataDir()

	pc := agent.LoadProjectContext(cwd, config.ConfigDir())
	// Overrides load before resolving, so a repo-pinned default_model actually
	// takes effect on this run rather than the pre-override config winning.
	if err := cfg.LoadRepoOverrides(pc.Root); err != nil {
		return ExitError, err
	}
	// A resumed session remembers the model it left off with; use it unless an
	// explicit -m overrides (§18). Headless resume matches the TUI here, so the
	// same conversation picks up the same model either way.
	ref := *model
	if ref == "" && *resume != "" {
		if info, err := session.Describe(dataDir, *resume); err == nil {
			ref = info.Model
		}
	}
	prov, modelName, err := cfg.Resolve(ref)
	if err != nil {
		return ExitError, err
	}

	var store *session.Store
	var priorMessages []provider.Message
	if *resume != "" {
		st, msgs, err := session.Resume(dataDir, *resume)
		if err != nil {
			return ExitError, err
		}
		store, priorMessages = st, msgs
	} else {
		if store, err = session.Create(dataDir); err != nil {
			return ExitError, err
		}
	}
	defer store.Close()

	// Record the model this run is on, mirroring the TUI: last-write-wins on
	// read, so the remembered model tracks every run, headless or interactive.
	if err := store.WriteModel(config.ModelRef(modelName, prov.Name())); err != nil {
		return ExitError, err
	}

	// Skills reach headless too. Without this a `run` cannot load a skill at
	// all, which is how the selfdev verification discovered the gap: the model
	// was told to load a skill and correctly reported it had no such tool.
	skills := tools.LoadSkills(tools.SkillDirs(pc.Root, config.ConfigDir()))
	var promptSkills []agent.Skill
	for _, sk := range skills.Index() {
		promptSkills = append(promptSkills, agent.Skill{Name: sk.Name, Desc: sk.Desc, Path: sk.Path})
	}
	conv := agent.NewConversation(agent.BuildSystemPrompt(pc, promptSkills, ""))
	if len(priorMessages) > 0 {
		// The messages from the resume above, not a second one. Resuming twice
		// re-parsed the whole file and discarded the store it returned, leaking
		// the descriptor for the life of the run.
		conv.Append(priorMessages...)
	}

	// Look overrides up by the *resolved* model, not the flag. Passing the
	// flag means a session relying on default_model silently gets no
	// per-model settings at all, which is how anchor_edits appeared to be
	// broken when it was simply never switched on.
	overrides := cfg.ModelOverrides(modelName)
	exposure := tools.NewExposure()
	var lsps *lsp.Manager
	if !*noTools {
		// Keep grep's symbol enrichment available to headless runs without
		// starting a language server until a search actually needs it.
		lsps = lsp.NewManager(pc.Root, cfg.LSP)
		defer lsps.Close()
	}

	var ts tools.Set
	if !*noTools {
		if canned, ok := provider.DemoCannedTools(); ok {
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
			if len(promptSkills) > 0 {
				ts = append(ts, tools.NewSkillTool(skills))
			}
		}
		// Headless has nobody to ask, so `ask` is deliberately absent rather
		// than present and always failing.
	}

	// Every message reaches the JSONL file as it lands, which is what makes
	// `-resume` replay anything at all (§18).
	conv.Persist(func(m provider.Message) error { return store.WriteMessage(m) })

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
	defer a.Close()

	// Headless recalls but does not extract: a one-shot invocation has no turn
	// count to reach the extraction interval, and firing a side-call on every
	// `evilcode run` would tax scripted use for nothing (plan.md §19).
	if bank, err := memory.Open(dataDir); err == nil {
		defer bank.Close()
		mem := memory.NewManager(bank, prov, cfg.Router(), store.Name, cfg.Features.Memory)
		if !*noTools {
			ts = append(ts, tools.NewMemory(mem)...)
			a.Tools = ts
		}
		a.Recall = func(ctx context.Context, in string) (string, any) {
			tail, hits := mem.Recall(ctx, in)
			return tail, hits
		}
	}

	// Ctrl+C cancels the turn rather than killing the process, so partial
	// output and the session file both survive.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		<-sig
		cancel()
	}()

	if len(priorMessages) > 0 && !*quiet {
		fmt.Fprintf(os.Stderr, "resumed %s (%d messages)\n", store.Name, len(priorMessages))
	}

	code := printEvents(a, store, *quiet)
	runErr := a.Run(ctx, prompt)
	exit := <-code

	if runErr != nil && exit == ExitOK {
		exit = ExitError
	}
	return exit, runErr
}

// printer renders the event stream to the terminal. It is a type rather than a
// loop so the local and `--remote` paths print identically — a daemon-backed
// run that formatted its own output differently would be a second contract to
// keep in step with §9.5.
type printer struct {
	quiet bool
	store *session.Store

	exit        int
	atLineStart bool

	// tty reports whether stdout is a terminal. Provider output is sanitized
	// when it is: a model can be talked into emitting OSC 52, which writes the
	// user's clipboard, and `evilcode run` prints deltas straight through.
	// Piped output is left byte-exact, because the consumer there is a program
	// that asked for the model's text and is not a terminal to hijack.
	tty bool
}

func newPrinter(quiet bool) *printer {
	return &printer{quiet: quiet, exit: ExitOK, atLineStart: true, tty: isTerminal(os.Stdout)}
}

// isTerminal reports whether f is a character device.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// text renders provider output for this destination.
func (p *printer) text(s string) string {
	if p.tty {
		return core.SanitizeTerminal(s)
	}
	return s
}

// clean sanitizes an event's text for this destination, leaving piped output
// byte-exact.
func (p *printer) clean(e agent.Event) agent.Event {
	if !p.tty {
		return e
	}
	e.Text = core.SanitizeTerminal(e.Text)
	e.ErrText = core.SanitizeTerminal(e.ErrText)
	e.Output = core.SanitizeTerminal(e.Output)
	e.Intent = core.SanitizeTerminal(e.Intent)
	if e.Call != nil {
		call := *e.Call
		call.Name = core.SanitizeTerminal(call.Name)
		e.Call = &call
	}
	if hits, ok := e.Display.([]memory.Hit); ok {
		out := make([]memory.Hit, len(hits))
		for i, h := range hits {
			h.Text = core.SanitizeTerminal(h.Text)
			out[i] = h
		}
		e.Display = out
	}
	return e
}

// newline ends the model's line before anything else writes, so a tool row
// never lands mid-sentence.
func (p *printer) newline() {
	if !p.atLineStart {
		fmt.Println()
		p.atLineStart = true
	}
}

// finish closes out a turn's output.
func (p *printer) finish() { p.newline() }

func (p *printer) print(e agent.Event) {
	// Everything below writes provider- or tool-authored text to a terminal.
	// Sanitizing each site invites the one that gets forgotten, so the event is
	// cleaned once on the way in — the same shape the TUI uses.
	e = p.clean(e)
	switch e.Kind {
	case agent.EventTextDelta:
		fmt.Print(e.Text)
		p.atLineStart = strings.HasSuffix(e.Text, "\n")

	case agent.EventToolResult:
		if p.quiet {
			return
		}
		p.newline()
		// The tool row names a file and an intent, both of which came from the
		// model or from the workspace.
		fmt.Fprintln(os.Stderr, toolLine(e))

	case agent.EventMemoryRecall:
		// Headless has no tile, but it must not inject silently: a scripted run
		// whose answer was shaped by a remembered fact should say so on stderr,
		// where the tool lines already are.
		if p.quiet {
			return
		}
		hits, ok := e.Display.([]memory.Hit)
		if !ok {
			return
		}
		p.newline()
		fmt.Fprintf(os.Stderr, "🧠 recalled %s\n", plural(len(hits), "memory"))
		for _, h := range hits {
			fmt.Fprintf(os.Stderr, "   · %s\n", h.Text)
		}

	case agent.EventNotice:
		if p.quiet {
			return
		}
		p.newline()
		fmt.Fprintf(os.Stderr, "· %s\n", e.Text)

	case agent.EventError:
		p.newline()
		// ErrText, not Err: a Go error cannot cross the socket, so a remote run
		// would print "<nil>" for every failure. emit fills ErrText from Err, so
		// the local path reads the same.
		fmt.Fprintf(os.Stderr, "✗ %s\n", e.ErrText)
		p.exit = ExitError

	case agent.EventTokenUsage:
		if p.store != nil {
			p.store.WriteMeta(session.Meta{
				Kind: session.MetaTokens, TokensIn: e.Usage.In, TokensOut: e.Usage.Out,
			})
		}

	case agent.EventTurnEnd:
		p.newline()
		switch e.Reason {
		case agent.EndInterrupted:
			fmt.Fprintln(os.Stderr, "· interrupted")
			p.exit = ExitInterrupted
		case agent.EndError, agent.EndMaxSteps:
			p.exit = ExitError
		}
	}
}

// plural renders a count with its noun. "memory" is spelled out because it does
// not take a plain -s.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if noun == "memory" {
		return fmt.Sprintf("%d memories", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// printEvents consumes the stream in a goroutine and reports the exit code once
// the turn ends. The stream is the only thing it knows about the agent.
func printEvents(a *agent.Agent, store *session.Store, quiet bool) <-chan int {
	out := make(chan int, 1)
	p := newPrinter(quiet)
	p.store = store
	go func() {
		for e := range a.Events() {
			p.print(e)
			if e.Kind == agent.EventTurnEnd {
				out <- p.exit
				return
			}
		}
		out <- p.exit
	}()
	return out
}

// toolLine renders a completed call the way §9.5 describes:
//
//	✓ read src/main.go · load entry point · 1.2k tok (+8 -5)
func toolLine(e agent.Event) string {
	icon := "✓"
	if e.IsError() {
		icon = "✗"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s %s", icon, e.Call.Name)
	target := toolTarget(e.Call.Args)
	if target != "" {
		fmt.Fprintf(&b, " %s", target)
	}
	// The intent is only worth a column when it says something the name and
	// target do not already say.
	if e.Intent != "" && !strings.Contains(e.Intent, target) {
		fmt.Fprintf(&b, " · %s", e.Intent)
	}
	if len(e.Repairs) > 0 {
		clean := make([]string, len(e.Repairs))
		for i, r := range e.Repairs {
			clean[i] = core.SanitizeTerminal(r)
		}
		fmt.Fprintf(&b, " · repaired: %s", strings.Join(clean, ", "))
	}
	if n := approxTokens(e.Output); n > 0 {
		fmt.Fprintf(&b, " · %s tok", humanCount(n))
	}
	if e.DiffStat != nil {
		fmt.Fprintf(&b, " (+%d -%d)", e.DiffStat.Added, e.DiffStat.Removed)
	}
	if e.IsError() {
		// An error is always shown in full: a tool row you cannot diagnose is
		// worse than no row (plan.md §9.5).
		//
		// ErrText, not Err: an event that crossed the daemon socket was
		// serialized, so only the text survives — and printing the nil Err
		// rendered every remote failure as the word "<nil>".
		fmt.Fprintf(&b, "\n    %s", e.ErrMessage())
	}
	return b.String()
}

// toolTarget pulls the one argument worth showing beside the tool name: the
// file a call touches, the pattern it searches for, the command it runs.
func toolTarget(raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "pattern", "cmd", "query"} {
		if v, ok := args[key].(string); ok && v != "" {
			return shorten(v)
		}
	}
	return ""
}

func shorten(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 60 {
		return s[:59] + "…"
	}
	return s
}

// approxTokens is the usual four-bytes-per-token rule of thumb. It is only ever
// shown as a badge, so an estimate is honest enough.
func approxTokens(s string) int { return len(s) / 4 }

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprint(n)
	}
}
