package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"evilcode/internal/provider"
)

// AdvisorPrompt is the second model's brief (plan.md §21).
//
// It is written to make silence the easy answer. An advisor that comments on
// every turn is a second driver arguing with the first, which is exactly what
// §21 says this must not become.
const AdvisorPrompt = `You are watching another agent work. Say nothing unless something is wrong.

Answer NONE unless one of these is true:
  - it is about to do something destructive or irreversible that was not asked for
  - it has said it is finished while its own todo list says otherwise
  - it has repeated the same failing approach three or more times
  - it has misread what the user asked for

If one applies, answer with one sentence naming the concern. No preamble, no
suggestions, no encouragement.

NONE is the right answer almost every time. Say it.`

// AdvisorNone is the reply that means "nothing to raise".
const AdvisorNone = "NONE"

// Advisor is the cheap conscience of §21: a second, small model that watches
// the conversation and may raise at most one concern per turn.
type Advisor struct {
	// Ask runs the side-call. It is a function rather than a router so this
	// package keeps knowing nothing about config, and so a test can answer
	// without a model.
	Ask func(ctx context.Context, system, user string) (string, error)

	// TodoState is a one-line summary of the todo list, when there is one. The
	// advisor cannot see the tools, so "it says it is done but the list is not"
	// is only checkable if the state is handed to it.
	TodoState func() string

	mu      sync.Mutex
	enabled bool

	// fired counts injections, which exists to be asserted on: §21's limit is
	// one per turn, and a limit nobody checks is a comment.
	fired int

	// lastConcern suppresses the same worry raised twice in a row. An advisor
	// repeating itself is noise, and noise gets ignored — including the time it
	// is right.
	lastConcern string
}

// NewAdvisor builds an advisor.
func NewAdvisor(ask func(context.Context, string, string) (string, error), enabled bool) *Advisor {
	return &Advisor{Ask: ask, enabled: enabled}
}

// Enabled reports whether the advisor is on.
func (a *Advisor) Enabled() bool {
	if a == nil || a.Ask == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled
}

// SetEnabled implements `/advisor on|off`.
func (a *Advisor) SetEnabled(on bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.enabled = on
	a.mu.Unlock()
}

// Status is the answer to `/advisor status`.
func (a *Advisor) Status() string {
	if a == nil || a.Ask == nil {
		return "advisor: not configured (no model for the smol role)"
	}
	state := "off"
	if a.Enabled() {
		state = "on"
	}
	a.mu.Lock()
	fired := a.fired
	a.mu.Unlock()
	return fmt.Sprintf("advisor: %s · %d %s raised this session",
		state, fired, concernNoun(fired))
}

func concernNoun(n int) string {
	if n == 1 {
		return "concern"
	}
	return "concerns"
}

// PostTurn implements Hooks.
//
// It never appends to the conversation itself — the concern goes in as a
// system-source interrupt, delivered at safe point D like every other one — so
// it always returns false and can sit anywhere in the chain without starving
// the hooks behind it.
func (a *Advisor) PostTurn(ctx context.Context, ag *Agent) (bool, error) {
	if !a.Enabled() {
		return false, nil
	}
	// One arguing voice at a time (§21): if auto-poke has something queued, the
	// advisor stays quiet this turn rather than piling on.
	if ag.PendingInterrupts() > 0 {
		return false, nil
	}

	view := a.compress(ag)
	if view == "" {
		return false, nil
	}

	concern, err := a.Ask(ctx, AdvisorPrompt, view)
	if err != nil {
		// A conscience that breaks the turn is worse than no conscience.
		return false, nil
	}
	concern = strings.TrimSpace(concern)
	if concern == "" || strings.HasPrefix(strings.ToUpper(concern), AdvisorNone) {
		return false, nil
	}

	a.mu.Lock()
	repeat := concern == a.lastConcern
	if !repeat {
		a.lastConcern = concern
		a.fired++
	}
	a.mu.Unlock()
	if repeat {
		return false, nil
	}

	ag.Interject(Interrupt{Source: SourceSystem, Text: "ⓘ advisor: " + concern})
	return false, nil
}

// AdvisorViewMessages is how many recent messages the advisor sees. It is a
// compressed view rather than the transcript: the whole point of §21 is that
// this is cheap, and a second model re-reading the entire conversation every
// turn is not.
const AdvisorViewMessages = 6

// compress builds the small view the advisor reasons over.
func (a *Advisor) compress(ag *Agent) string {
	msgs := ag.Conv.Messages()
	if len(msgs) == 0 {
		return ""
	}

	var b strings.Builder
	if a.TodoState != nil {
		if state := a.TodoState(); state != "" {
			b.WriteString("Todo state: " + state + "\n\n")
		}
	}

	start := max(len(msgs)-AdvisorViewMessages, 0)
	for _, m := range msgs[start:] {
		if m.Role == provider.RoleSystem {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			// A tool-call message with no text still matters: it is what the
			// agent *did*, and a run of them is how a repeated failing approach
			// looks from here.
			if len(m.ToolCalls) > 0 {
				names := make([]string, 0, len(m.ToolCalls))
				for _, c := range m.ToolCalls {
					names = append(names, c.Name)
				}
				fmt.Fprintf(&b, "%s: [called %s]\n", m.Role, strings.Join(names, ", "))
			}
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, truncateForAdvisor(content))
	}
	return strings.TrimSpace(b.String())
}

// AdvisorMessageCap bounds one message in the view, so a single pasted file
// cannot crowd out everything else the advisor needs to see.
const AdvisorMessageCap = 1200

func truncateForAdvisor(s string) string {
	if len(s) <= AdvisorMessageCap {
		return s
	}
	cut := AdvisorMessageCap
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + " […]"
}
