// Package todo implements the plan/todo/discipline system of plan.md §12: the
// part of the harness that argues with the agent's own confidence.
package todo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Status is a todo item's state.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusCancelled  Status = "cancelled"
)

// Valid reports whether a status is one the tool accepts.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted, StatusCancelled:
		return true
	}
	return false
}

// Priority weights the confidence average (high 3 / medium 2 / low 1).
type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

func (p Priority) Valid() bool {
	switch p {
	case PriorityHigh, PriorityMedium, PriorityLow:
		return true
	}
	return false
}

// Weight is the priority's weight in the aggregate confidence.
func (p Priority) Weight() int {
	switch p {
	case PriorityHigh:
		return 3
	case PriorityLow:
		return 1
	default:
		return 2
	}
}

// Item is one todo (plan.md §12.2).
type Item struct {
	ID       string   `json:"id"`
	Content  string   `json:"content"`
	Status   Status   `json:"status"`
	Priority Priority `json:"priority"`

	// Group buckets items under a goal. Nil means the flat list.
	Group *string `json:"group,omitempty"`

	// BlockedBy names item IDs that must finish first.
	BlockedBy []string `json:"blocked_by,omitempty"`

	// Confidence is how sure the agent is about the plan for this item;
	// CompletionConfidence is how sure it is the work is actually done.
	Confidence           *uint8 `json:"confidence,omitempty"`
	CompletionConfidence *uint8 `json:"completion_confidence,omitempty"`

	// ConfidenceHistory is tool-owned and append-only. A model-supplied value
	// is discarded on write: the whole point of the trail is that the agent
	// cannot author it.
	ConfidenceHistory []uint8 `json:"confidence_history,omitempty"`
}

// Blocked reports whether this item is waiting on another.
func (i Item) Blocked() bool { return len(i.BlockedBy) > 0 }

// Plan is the session-level intent record, one per session.
type Plan struct {
	// UserIntention is the agent's statement of what the user actually wants.
	UserIntention *string `json:"user_intention,omitempty"`

	// UnderstandsUserIntent is 0-100.
	UnderstandsUserIntent *uint8 `json:"understands_user_intent,omitempty"`

	UnderstandsUserIntentHistory []uint8 `json:"understands_user_intent_history,omitempty"`
}

// Goal is per-group scoring.
//
// Scoring lives at the goal level rather than per item because not every task
// has a meaningful score of its own: "optimize grep latency" can close its
// loop because progress has a metric, "design an onboarding screen" cannot,
// and "read the auth code" has no score at all (plan.md §12.2).
type Goal struct {
	// Group names the goal; empty is the flat list.
	Group string `json:"group"`

	// ClosedFeedbackLoop is 0-100: has anything reported back on whether the
	// work satisfied the requirement.
	ClosedFeedbackLoop *uint8 `json:"closed_feedback_loop,omitempty"`

	// FeedbackLoop is the concrete command and metric, e.g.
	// `go test ./internal/auth/...`.
	FeedbackLoop *string `json:"feedback_loop,omitempty"`

	// EndToEndOwnership is 0-100 and gates completing the group.
	EndToEndOwnership *uint8 `json:"end_to_end_ownership,omitempty"`

	ClosedFeedbackLoopHistory []uint8 `json:"closed_feedback_loop_history,omitempty"`
	EndToEndOwnershipHistory  []uint8 `json:"end_to_end_ownership_history,omitempty"`
}

// Limits from plan.md §12.6. These are coordination state, not a log, and an
// unbounded one is a memory leak with extra steps.
const (
	MaxItems        = 1024
	MaxObservations = 256
)

// Thresholds from plan.md §12.3.
const (
	// QualityGate is the score both intent-understanding and the feedback loop
	// must reach.
	QualityGate = 96

	// SevereIntentMisunderstanding fires immediately rather than deferring.
	SevereIntentMisunderstanding = 60

	// SpikeDelta is the jump that marks a suspicious confidence rise.
	SpikeDelta = 15
)

// Store holds one session's todo state on disk.
type Store struct {
	Dir     string
	Session string

	mu    sync.Mutex
	items []Item
	goals []Goal
	plan  Plan
	obs   []Observation
}

// NewStore opens (or creates) a session's todo state.
func NewStore(dataDir, session string) (*Store, error) {
	s := &Store{Dir: filepath.Join(dataDir, "todos"), Session: session}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	return s, s.load()
}

func (s *Store) path(suffix string) string {
	return filepath.Join(s.Dir, s.Session+suffix+".json")
}

func (s *Store) load() error {
	readJSON(s.path(""), &s.items)
	readJSON(s.path("-goals"), &s.goals)
	readJSON(s.path("-plan"), &s.plan)
	readJSON(s.path("-gates"), &s.obs)
	return nil
}

// readJSON loads a file if present. A missing or corrupt file is not fatal:
// todo state is coordination, and losing it must not take a session down.
func readJSON(path string, dst any) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, dst)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) save() error {
	if err := writeJSON(s.path(""), s.items); err != nil {
		return err
	}
	if err := writeJSON(s.path("-goals"), s.goals); err != nil {
		return err
	}
	if err := writeJSON(s.path("-plan"), s.plan); err != nil {
		return err
	}
	return writeJSON(s.path("-gates"), s.obs)
}

// Items returns a copy of the stored list.
func (s *Store) Items() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Item(nil), s.items...)
}

// Goals returns a copy of the stored goals.
func (s *Store) Goals() []Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Goal(nil), s.goals...)
}

// Plan returns the session plan.
func (s *Store) Plan() Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan
}

// Goal looks up a group's goal.
func (s *Store) Goal(group string) (Goal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.goals {
		if g.Group == group {
			return g, true
		}
	}
	return Goal{}, false
}

// Write is one todo-tool call: a full replacement list plus optional plan and
// goal updates.
type Write struct {
	Items []Item
	Plan  *Plan
	Goals []Goal
}

// Result reports what a write did.
type Result struct {
	// Rejected is set when a hard gate refused the write. The stored list is
	// unchanged and Explanation says why.
	Rejected    bool
	Explanation string

	// Immediate is a nudge that fires now rather than at turn end.
	Immediate string

	// Delta describes what changed, for the display-only §12.5 surfaces.
	Delta Delta
}

// Apply validates and stores a write, recording history observations.
func (s *Store) Apply(w Write) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(w.Items) > MaxItems {
		return Result{
			Rejected: true,
			Explanation: fmt.Sprintf(
				"a todo list of %d items exceeds the %d-item cap; this is coordination state, not a log",
				len(w.Items), MaxItems),
		}, nil
	}
	for i, item := range w.Items {
		if strings.TrimSpace(item.Content) == "" {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %d has empty content", i)}, nil
		}
		if !item.Status.Valid() {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has invalid status %q", item.Content, item.Status)}, nil
		}
		if item.Priority != "" && !item.Priority.Valid() {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has invalid priority %q", item.Content, item.Priority)}, nil
		}
	}

	// The hard gate: completing a group requires end-to-end ownership. This is
	// the one gate that blocks rather than defers (plan.md §12.3).
	if group, ok := s.completesGroup(w); ok {
		owner := ownershipFor(w.Goals, s.goals, group)
		if owner == nil || *owner < QualityGate {
			got := "unset"
			if owner != nil {
				got = fmt.Sprint(*owner)
			}
			return Result{
				Rejected: true,
				Explanation: fmt.Sprintf(
					"rejected: completing the group %q requires end_to_end_ownership of at least %d, but it is %s. "+
						"Owning a goal end to end means you verified the whole thing works, not just that each "+
						"item was edited. Raise it only once you have.",
					group, QualityGate, got),
			}, nil
		}
	}

	prev := append([]Item(nil), s.items...)
	res := Result{Delta: DiffItems(prev, w.Items)}

	// Histories are tool-owned and append-only, and each write contributes at
	// most one observation per score. A single completion update must not be
	// able to manufacture an apparent intermediate step (plan.md §12.2).
	s.items = mergeItems(prev, w.Items)

	if w.Plan != nil {
		firstPlanWrite := s.plan.UnderstandsUserIntent == nil
		s.plan.UserIntention = w.Plan.UserIntention
		if v := w.Plan.UnderstandsUserIntent; v != nil {
			s.plan.UnderstandsUserIntentHistory = appendScore(s.plan.UnderstandsUserIntentHistory, *v)
			s.plan.UnderstandsUserIntent = v

			if *v < QualityGate {
				s.observe(Observation{Kind: KindIntent, Score: *v})
			}
			// The one immediate exception: the *first* plan write admitting it
			// does not know the task. A whole turn of wrong work cannot be
			// undone at turn end (plan.md §12.3).
			if firstPlanWrite && *v < SevereIntentMisunderstanding {
				res.Immediate = fmt.Sprintf(
					"[automated todo quality review - not a user message] You recorded "+
						"understands_user_intent as %d, which means you do not yet know what the user wants. "+
						"Stop and establish that before doing the work: re-read the request, state your "+
						"interpretation, and ask about anything you had to guess. "+
						"Do not reply conversationally or wait for the user.", *v)
			}
		}
	}

	for _, g := range w.Goals {
		s.applyGoal(g)
	}

	if err := s.save(); err != nil {
		return res, err
	}
	return res, nil
}

// completesGroup reports whether this write finishes off a group that was not
// already finished.
func (s *Store) completesGroup(w Write) (string, bool) {
	groups := map[string]bool{}
	for _, item := range w.Items {
		if item.Group == nil {
			continue
		}
		groups[*item.Group] = true
	}
	for group := range groups {
		if allDone(w.Items, group) && !allDone(s.items, group) {
			return group, true
		}
	}
	return "", false
}

func allDone(items []Item, group string) bool {
	seen := false
	for _, item := range items {
		if item.Group == nil || *item.Group != group {
			continue
		}
		seen = true
		if item.Status != StatusCompleted && item.Status != StatusCancelled {
			return false
		}
	}
	return seen
}

func ownershipFor(incoming, stored []Goal, group string) *uint8 {
	for _, g := range incoming {
		if g.Group == group && g.EndToEndOwnership != nil {
			return g.EndToEndOwnership
		}
	}
	for _, g := range stored {
		if g.Group == group && g.EndToEndOwnership != nil {
			return g.EndToEndOwnership
		}
	}
	return nil
}

func (s *Store) applyGoal(in Goal) {
	idx := -1
	for i, g := range s.goals {
		if g.Group == in.Group {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.goals = append(s.goals, Goal{Group: in.Group})
		idx = len(s.goals) - 1
	}
	g := &s.goals[idx]

	if in.FeedbackLoop != nil {
		g.FeedbackLoop = in.FeedbackLoop
	}
	if v := in.ClosedFeedbackLoop; v != nil {
		g.ClosedFeedbackLoopHistory = appendScore(g.ClosedFeedbackLoopHistory, *v)
		g.ClosedFeedbackLoop = v
		if *v < QualityGate {
			s.observe(Observation{Kind: KindLoop, Group: in.Group, Score: *v})
		}
	}
	if v := in.EndToEndOwnership; v != nil {
		g.EndToEndOwnershipHistory = appendScore(g.EndToEndOwnershipHistory, *v)
		g.EndToEndOwnership = v
	}
}

// appendScore adds one observation, collapsing an unchanged repeat. Recording
// the same number twice would make a stalled score look like progress.
func appendScore(history []uint8, v uint8) []uint8 {
	if n := len(history); n > 0 && history[n-1] == v {
		return history
	}
	return append(history, v)
}

// mergeItems carries tool-owned history forward across a write, keyed by item
// ID, and discards any history the model tried to supply.
func mergeItems(prev, incoming []Item) []Item {
	byID := make(map[string]Item, len(prev))
	for _, p := range prev {
		byID[p.ID] = p
	}

	out := make([]Item, 0, len(incoming))
	for _, item := range incoming {
		old, existed := byID[item.ID]

		// The trail is only meaningful if the agent cannot author it.
		item.ConfidenceHistory = nil
		if existed {
			item.ConfidenceHistory = old.ConfidenceHistory
		}
		if item.Confidence != nil {
			item.ConfidenceHistory = appendScore(item.ConfidenceHistory, *item.Confidence)
		}
		if item.Priority == "" {
			item.Priority = PriorityMedium
			if existed && old.Priority != "" {
				item.Priority = old.Priority
			}
		}
		out = append(out, item)
	}
	return out
}

// IsSpike reports whether a completed item's confidence jumped suspiciously in
// its final recorded step (plan.md §12.2).
//
// A single history entry is never a spike: there is no earlier value to have
// jumped from, and treating it as one would flag every first-time completion.
func IsSpike(item Item) bool {
	if item.Status != StatusCompleted {
		return false
	}
	h := item.ConfidenceHistory
	switch {
	case len(h) == 0:
		if item.Confidence == nil || item.CompletionConfidence == nil {
			return false
		}
		return int(*item.CompletionConfidence)-int(*item.Confidence) >= SpikeDelta
	case len(h) == 1:
		return false
	default:
		return int(h[len(h)-1])-int(h[len(h)-2]) >= SpikeDelta
	}
}

// AggregateConfidence is the priority-weighted mean completion confidence,
// excluding cancelled items. It returns false when nothing carries a score.
func AggregateConfidence(items []Item) (int, bool) {
	weighted, weight := 0, 0
	for _, item := range items {
		if item.Status == StatusCancelled {
			continue
		}
		if item.CompletionConfidence == nil {
			continue
		}
		w := item.Priority.Weight()
		weighted += int(*item.CompletionConfidence) * w
		weight += w
	}
	if weight == 0 {
		return 0, false
	}
	// Round half up.
	return (weighted*2 + weight) / (weight * 2), true
}

// Incomplete counts items still to do.
func Incomplete(items []Item) int {
	n := 0
	for _, item := range items {
		if item.Status == StatusPending || item.Status == StatusInProgress {
			n++
		}
	}
	return n
}

// Groups returns group names in first-seen order, with the ungrouped bucket
// last so a flat list reads naturally.
func Groups(items []Item) []string {
	var order []string
	seen := map[string]bool{}
	hasUngrouped := false
	for _, item := range items {
		if item.Group == nil {
			hasUngrouped = true
			continue
		}
		if !seen[*item.Group] {
			seen[*item.Group] = true
			order = append(order, *item.Group)
		}
	}
	if hasUngrouped {
		order = append(order, "")
	}
	return order
}

// SortItems orders a list for display: in progress, pending, completed,
// cancelled (plan.md §8.4).
func SortItems(items []Item) []Item {
	out := append([]Item(nil), items...)
	rank := map[Status]int{
		StatusInProgress: 0,
		StatusPending:    1,
		StatusCompleted:  2,
		StatusCancelled:  3,
	}
	sort.SliceStable(out, func(i, j int) bool {
		return rank[out[i].Status] < rank[out[j].Status]
	})
	return out
}

// Summary is a one-line description of the list, for anything that needs to
// reason about progress without reading the whole thing — the advisor's
// compressed view most of all (plan.md §21).
func (s *Store) Summary() string {
	items := s.Items()
	if len(items) == 0 {
		return ""
	}
	var done, active, blocked int
	for _, it := range items {
		switch {
		case it.Status == StatusCompleted || it.Status == StatusCancelled:
			done++
		case len(it.BlockedBy) > 0:
			blocked++
		case it.Status == StatusInProgress:
			active++
		}
	}
	out := fmt.Sprintf("%d/%d done", done, len(items))
	if active > 0 {
		out += fmt.Sprintf(", %d in progress", active)
	}
	if blocked > 0 {
		out += fmt.Sprintf(", %d blocked", blocked)
	}
	return out
}
