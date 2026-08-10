package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/todo"
)

// Overnight is the supervised long-run loop of plan.md §5: keep working the
// todo list without a human present, under hard caps.
//
// Everything about it is a breaker. An unattended loop against a metered API is
// the single most expensive thing this program can do, so it stops for turns,
// for wall-clock time, for tokens, for repeated failures, and for a list that
// stopped moving — and it says which one stopped it.
type Overnight struct {
	Active bool

	// MaxTurns, Budget and Deadline are the hard caps.
	MaxTurns int
	Budget   int
	Deadline time.Time

	Turns  int
	Tokens int

	Started time.Time

	// stalled counts consecutive turns that did not change the todo list.
	stalled int

	// lastState is the todo summary at the previous turn, for the stall check.
	lastState string

	// Stopped records why the run ended, which is the whole output of the
	// feature: waking up to a stopped loop and not knowing why is useless.
	Stopped string

	// Preflight is the immutable starting point for the report. Keeping the
	// caps beside the captured state makes a report explain the run it actually
	// describes, even if the defaults change later.
	Preflight OvernightPreflight

	// BeforeTodos is advanced after every recorded turn. It is deliberately
	// separate from Preflight.Todos so each card can show the immediately
	// previous state, not just the state at launch.
	BeforeTodos  []todo.Item
	FinalTodos   []todo.Item
	EndGit       GitSnapshot
	Timeline     []OvernightTimelineEntry
	Cards        []OvernightTaskCard
	CurrentTools []OvernightToolCheck
	ReportPath   string
	StoppedAt    time.Time
}

// Overnight caps. Deliberately modest — this is a personal tool on a personal
// key, and the failure mode is a bill rather than an error.
const (
	OvernightMaxTurns = 40
	OvernightBudget   = 400_000
	OvernightHours    = 8

	// OvernightMaxStalls is how many turns may pass without the todo list
	// moving. A model that answers every nudge without changing anything is the
	// exact loop §12.6 exists to catch, and it happened for real with auto-poke.
	OvernightMaxStalls = 3
)

// Start arms the loop.
func (o *Overnight) Start(now time.Time) {
	o.StartWithPreflight(OvernightPreflight{
		Started:  now,
		MaxTurns: OvernightMaxTurns,
		Budget:   OvernightBudget,
		Deadline: now.Add(OvernightHours * time.Hour),
	})
}

// StartWithSnapshot arms the loop and captures the state that existed before
// the first unattended turn. Start remains intentionally small for callers
// that only need the breaker in isolation; the TUI uses this richer entry
// point so a stopped run can account for its work.
func (o *Overnight) StartWithSnapshot(now time.Time, git GitSnapshot, items []todo.Item) {
	o.StartWithPreflight(OvernightPreflight{
		Started:  now,
		MaxTurns: OvernightMaxTurns,
		Budget:   OvernightBudget,
		Deadline: now.Add(OvernightHours * time.Hour),
		Git:      cloneGitSnapshot(git),
		Todos:    cloneTodoItems(items),
	})
}

// StartWithPreflight arms the loop with an already collected preflight. It is
// useful to tests and keeps the report's input explicit.
func (o *Overnight) StartWithPreflight(preflight OvernightPreflight) {
	if preflight.Started.IsZero() {
		preflight.Started = time.Now()
	}
	if preflight.MaxTurns == 0 {
		preflight.MaxTurns = OvernightMaxTurns
	}
	if preflight.Budget == 0 {
		preflight.Budget = OvernightBudget
	}
	if preflight.Deadline.IsZero() {
		preflight.Deadline = preflight.Started.Add(OvernightHours * time.Hour)
	}
	preflight.Git = cloneGitSnapshot(preflight.Git)
	preflight.Todos = cloneTodoItems(preflight.Todos)
	*o = Overnight{
		Active:      true,
		MaxTurns:    preflight.MaxTurns,
		Budget:      preflight.Budget,
		Deadline:    preflight.Deadline,
		Started:     preflight.Started,
		Preflight:   preflight,
		BeforeTodos: cloneTodoItems(preflight.Todos),
	}
}

// Stop disarms the loop with a reason.
func (o *Overnight) Stop(reason string) {
	o.Active = false
	o.Stopped = reason
	if o.StoppedAt.IsZero() {
		o.StoppedAt = time.Now()
	}
}

// BeginTurn clears evidence from the previous unattended turn.
func (o *Overnight) BeginTurn() {
	o.CurrentTools = nil
}

// AddToolCheck records one tool result for the turn currently being assessed.
func (o *Overnight) AddToolCheck(check OvernightToolCheck) {
	o.CurrentTools = append(o.CurrentTools, check)
}

// ShouldContinue reports whether another turn is allowed, and why not if it is
// not. It is called once per finished turn, and spent is what that turn cost.
//
// The cost accumulates. Assigning it made the budget the most any single turn
// had spent, which no turn ever approaches, so the one breaker that bounds the
// bill could not fire.
func (o *Overnight) ShouldContinue(now time.Time, todoState string, spent int) (bool, string) {
	if !o.Active {
		return false, o.Stopped
	}
	o.Turns++
	o.Tokens += spent

	// Every stop disarms. Returning false while still Active left the loop
	// armed for the next call, which both lies to `/overnight status` and lets
	// a later turn slip past a cap that had already been reached.
	switch {
	case o.Turns >= o.MaxTurns:
		return o.halt(fmt.Sprintf("reached the %d-turn cap", o.MaxTurns))
	case o.Budget > 0 && o.Tokens >= o.Budget:
		return o.halt(fmt.Sprintf("spent the %s-token budget", humanTokens(o.Budget)))
	case !o.Deadline.IsZero() && now.After(o.Deadline):
		return o.halt("ran out of time")
	}

	// A list that is not moving means the model is answering rather than
	// working, and another turn will not change that.
	if todoState == o.lastState {
		o.stalled++
		if o.stalled >= OvernightMaxStalls {
			return o.halt(fmt.Sprintf("the todo list has not moved in %d turns", o.stalled))
		}
	} else {
		o.stalled = 0
		o.lastState = todoState
	}

	if todoState == "" {
		return o.halt("there is no todo list to work through")
	}
	if strings.HasPrefix(todoState, "done") || isComplete(todoState) {
		return o.halt("the todo list is finished")
	}
	return true, ""
}

// halt disarms and reports, so no caller has to remember to.
func (o *Overnight) halt(reason string) (bool, string) {
	o.Stop(reason)
	return false, reason
}

// isComplete reads the "N/M done" summary and reports whether N == M.
func isComplete(summary string) bool {
	var done, total int
	if _, err := fmt.Sscanf(summary, "%d/%d done", &done, &total); err != nil {
		return false
	}
	return total > 0 && done >= total
}

// Status is the line `/overnight status` prints.
func (o *Overnight) Status() string {
	if !o.Active {
		if o.Stopped != "" {
			return fmt.Sprintf("⏳ Overnight stopped after %d turns: %s", o.Turns, o.Stopped)
		}
		return "⏳ Overnight is off · /overnight to start a supervised long run"
	}
	return fmt.Sprintf("⏳ Overnight · turn %d/%d · %s tokens · until %s",
		o.Turns, o.MaxTurns, humanTokens(o.Tokens), o.Deadline.Format("15:04"))
}

// OvernightPrompt is what each unattended turn asks for.
//
// It is written for a model with nobody watching: no questions, no options, and
// an explicit instruction to stop rather than guess — because the one thing
// that cannot happen here is waiting for an answer nobody is there to give.
const OvernightPrompt = `Continue working through the todo list.

Nobody is watching. Do not ask questions — there is no one to answer them. If
you reach something that genuinely needs a decision, mark the item blocked, say
why in one line, and move to the next item.

Work one item at a time and verify each before marking it done. If you cannot
verify it, leave it in progress and say what is missing.`

// overnightCommand implements `/overnight`.
func (m *Model) overnightCommand(arg string) tea.Cmd {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "off", "stop":
		if !m.overnight.Active {
			m.notice = m.overnight.Status()
			return nil
		}
		m.finishOvernight("you stopped it")
		return nil
	case "status":
		m.notice = m.overnight.Status()
		return nil
	case "report":
		m.showOvernightReport()
		return nil
	}

	if m.todos == nil || m.todos.Summary() == "" {
		m.notice = "⏳ Overnight needs a todo list to work through — make a plan first"
		return nil
	}
	if m.processing {
		m.notice = "⏳ Finish the current turn first"
		return nil
	}

	now := time.Now()
	items := m.todos.Items()
	m.overnight.StartWithSnapshot(now, captureGit(m.cwd), items)
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: fmt.Sprintf(
		"⏳ Overnight armed · at most %d turns, %s tokens, %d hours\n"+
			"It stops on its own and says why. /overnight off to stop it now.",
		OvernightMaxTurns, humanTokens(OvernightBudget), OvernightHours)})
	m.scroll.FollowBottom()
	m.submitHidden(OvernightPrompt)
	return nil
}

// stepOvernight runs after a turn ends, continuing or stopping the loop. spent
// is what the finished turn cost, which the caller reads before the status line
// is reset.
func (m *Model) stepOvernight(spent int) {
	if !m.overnight.Active {
		return
	}
	items := []todo.Item(nil)
	if m.todos != nil {
		items = m.todos.Items()
	}
	m.overnight.RecordTurn(time.Now(), spent, items)
	state := ""
	if m.todos != nil {
		state = m.todos.Summary()
	}
	ok, why := m.overnight.ShouldContinue(time.Now(), state, spent)
	if ok {
		m.submitHidden(OvernightPrompt)
		return
	}

	m.finishOvernight(why)
}
