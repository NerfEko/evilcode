// Package todo implements the plan/todo/discipline system of plan.md §12: the
// part of the harness that argues with the agent's own confidence.
package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
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

func cloneUint8Ptr(value *uint8) *uint8 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneItem(item Item) Item {
	item.Group = cloneStringPtr(item.Group)
	item.BlockedBy = append([]string(nil), item.BlockedBy...)
	item.Confidence = cloneUint8Ptr(item.Confidence)
	item.CompletionConfidence = cloneUint8Ptr(item.CompletionConfidence)
	item.ConfidenceHistory = append([]uint8(nil), item.ConfidenceHistory...)
	return item
}

func cloneItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, item := range items {
		out[i] = cloneItem(item)
	}
	return out
}

func cloneGoal(goal Goal) Goal {
	goal.ClosedFeedbackLoop = cloneUint8Ptr(goal.ClosedFeedbackLoop)
	goal.FeedbackLoop = cloneStringPtr(goal.FeedbackLoop)
	goal.EndToEndOwnership = cloneUint8Ptr(goal.EndToEndOwnership)
	goal.ClosedFeedbackLoopHistory = append([]uint8(nil), goal.ClosedFeedbackLoopHistory...)
	goal.EndToEndOwnershipHistory = append([]uint8(nil), goal.EndToEndOwnershipHistory...)
	return goal
}

func cloneGoals(goals []Goal) []Goal {
	out := make([]Goal, len(goals))
	for i, goal := range goals {
		out[i] = cloneGoal(goal)
	}
	return out
}

func clonePlan(plan Plan) Plan {
	plan.UserIntention = cloneStringPtr(plan.UserIntention)
	plan.UnderstandsUserIntent = cloneUint8Ptr(plan.UnderstandsUserIntent)
	plan.UnderstandsUserIntentHistory = append([]uint8(nil), plan.UnderstandsUserIntentHistory...)
	return plan
}

// Limits from plan.md §12.6. These are coordination state, not a log, and an
// unbounded one is a memory leak with extra steps.
const (
	MaxItems        = 1024
	MaxObservations = 256
	maxStateBytes   = 8 << 20
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
	if session == "" || session == "." || session == ".." ||
		filepath.Base(session) != session || strings.ContainsAny(session, `/\\`) ||
		strings.ContainsRune(session, 0) || filepath.IsAbs(session) {
		return nil, fmt.Errorf("invalid todo namespace %q", session)
	}
	s := &Store{Dir: filepath.Join(dataDir, "todos"), Session: session}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return nil, err
	}
	return s, s.load()
}

func (s *Store) path(suffix string) string {
	return filepath.Join(s.Dir, s.Session+suffix+".json")
}

func (s *Store) load() error {
	return errors.Join(
		readJSON(s.path(""), &s.items),
		readJSON(s.path("-goals"), &s.goals),
		readJSON(s.path("-plan"), &s.plan),
		readJSON(s.path("-gates"), &s.obs),
	)
}

// readJSON loads a file if present. A missing file is a fresh session's
// legitimate empty state; anything else — a permissions error, a corrupt
// file — is real trouble and must not be mistaken for the same thing.
func readJSON(path string, dst any) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return fmt.Errorf("%s exceeds %d bytes", path, maxStateBytes)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// stage writes a file's new contents beside it, ready to be renamed into place.
//
// The temp name carries the store's session, so two namespaces staging at once
// do not write through one another's file — the previous `path + ".tmp"` was
// shared by every store over the same directory.
func stage(path string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// save writes the four files a transaction touches.
//
// Every file is staged and synced before any of them is renamed, so the failure
// that actually happens — no space, no permission, a bad path — happens while
// the previous state is still the state on disk. The renames themselves are the
// commit: four of them are not one atomic operation, but a rename that fails
// after its siblings succeeded needs the filesystem to break between two
// metadata updates, where the old code needed only a full disk.
func (s *Store) save() error {
	files := []struct {
		path string
		v    any
	}{
		{s.path(""), s.items},
		{s.path("-goals"), s.goals},
		{s.path("-plan"), s.plan},
		{s.path("-gates"), s.obs},
	}

	staged := make([]string, 0, len(files))
	defer func() {
		for _, tmp := range staged {
			os.Remove(tmp)
		}
	}()
	for _, f := range files {
		tmp, err := stage(f.path, f.v)
		if err != nil {
			return err
		}
		staged = append(staged, tmp)
	}
	backups := make([]string, len(files))
	installed := make([]bool, len(files))
	restore := func() error {
		var restoreErr error
		for i, f := range files {
			if backups[i] == "" {
				if installed[i] {
					if err := os.RemoveAll(f.path); err != nil && !os.IsNotExist(err) {
						restoreErr = errors.Join(restoreErr, err)
					}
				}
				continue
			}
			if err := os.RemoveAll(f.path); err != nil && !os.IsNotExist(err) {
				restoreErr = errors.Join(restoreErr, err)
				continue
			}
			if err := os.Rename(backups[i], f.path); err != nil {
				restoreErr = errors.Join(restoreErr, err)
			}
		}
		return restoreErr
	}
	for i, f := range files {
		if _, err := os.Lstat(f.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errors.Join(err, restore())
		}
		old, err := os.CreateTemp(filepath.Dir(f.path), ".todo-backup-*")
		if err != nil {
			return errors.Join(err, restore())
		}
		backupName := old.Name()
		if err := old.Close(); err != nil {
			return errors.Join(err, restore())
		}
		if err := os.Remove(backupName); err != nil {
			return errors.Join(err, restore())
		}
		if err := os.Rename(f.path, backupName); err != nil {
			return errors.Join(err, restore())
		}
		backups[i] = backupName
	}
	for i, f := range files {
		if err := os.Rename(staged[i], f.path); err != nil {
			return errors.Join(err, restore())
		}
		installed[i] = true
	}
	for _, old := range backups {
		if old != "" {
			_ = os.Remove(old)
		}
	}
	return nil
}

// Items returns a copy of the stored list.
func (s *Store) Items() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneItems(s.items)
}

// Goals returns a copy of the stored goals.
func (s *Store) Goals() []Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneGoals(s.goals)
}

// Plan returns the session plan.
func (s *Store) Plan() Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePlan(s.plan)
}

// Goal looks up a group's goal.
func (s *Store) Goal(group string) (Goal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.goals {
		if g.Group == group {
			return cloneGoal(g), true
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
//
// A write that cannot be persisted is not applied. The transaction mutates live
// state as it goes — the gates read what has been applied so far, so a clone
// would have to be threaded through every one of them — and a failed save
// restores the state it started from. What the store serves is what a restart
// would replay.
func (s *Store) Apply(w Write) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	restore := s.snapshot()

	if len(w.Items) > MaxItems {
		return Result{
			Rejected: true,
			Explanation: fmt.Sprintf(
				"a todo list of %d items exceeds the %d-item cap; this is coordination state, not a log",
				len(w.Items), MaxItems),
		}, nil
	}
	seen := make(map[string]bool, len(w.Items))
	for i, item := range w.Items {
		if strings.TrimSpace(item.Content) == "" {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %d has empty content", i)}, nil
		}
		if strings.TrimSpace(item.ID) == "" {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %d (%q) has a blank id", i, item.Content)}, nil
		}
		if seen[item.ID] {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item id %q is used by more than one item in this write", item.ID)}, nil
		}
		seen[item.ID] = true
		if !item.Status.Valid() {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has invalid status %q", item.Content, item.Status)}, nil
		}
		if item.Priority != "" && !item.Priority.Valid() {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has invalid priority %q", item.Content, item.Priority)}, nil
		}
		if item.Confidence != nil && *item.Confidence > 100 {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has confidence %d, which must be 0-100", item.Content, *item.Confidence)}, nil
		}
		if item.CompletionConfidence != nil && *item.CompletionConfidence > 100 {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q has completion_confidence %d, which must be 0-100", item.Content, *item.CompletionConfidence)}, nil
		}
		if slices.Contains(item.BlockedBy, item.ID) {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("item %q depends on itself", item.Content)}, nil
		}
	}
	if w.Plan != nil && w.Plan.UnderstandsUserIntent != nil && *w.Plan.UnderstandsUserIntent > 100 {
		return Result{Rejected: true,
			Explanation: fmt.Sprintf("understands_user_intent is %d, which must be 0-100", *w.Plan.UnderstandsUserIntent)}, nil
	}
	seenGoals := make(map[string]bool, len(w.Goals))
	for _, goal := range w.Goals {
		if seenGoals[goal.Group] {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("goal group %q is updated more than once in this write", goal.Group)}, nil
		}
		seenGoals[goal.Group] = true
		if goal.ClosedFeedbackLoop != nil && *goal.ClosedFeedbackLoop > 100 {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("goal %q closed_feedback_loop is %d, which must be 0-100", goal.Group, *goal.ClosedFeedbackLoop)}, nil
		}
		if goal.EndToEndOwnership != nil && *goal.EndToEndOwnership > 100 {
			return Result{Rejected: true,
				Explanation: fmt.Sprintf("goal %q end_to_end_ownership is %d, which must be 0-100", goal.Group, *goal.EndToEndOwnership)}, nil
		}
	}

	// Items is a full replacement. A dependency on an old item omitted from this
	// write would become a dangling reference as soon as the write lands.
	validIDs := make(map[string]bool, len(seen))
	for id := range seen {
		validIDs[id] = true
	}
	for _, item := range w.Items {
		for _, dep := range item.BlockedBy {
			if !validIDs[dep] {
				return Result{Rejected: true,
					Explanation: fmt.Sprintf("item %q depends on unknown id %q", item.Content, dep)}, nil
			}
		}
	}
	if id, ok := dependencyCycle(w.Items); ok {
		return Result{Rejected: true,
			Explanation: fmt.Sprintf("todo dependencies contain a cycle involving %q", id)}, nil
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

	prev := cloneItems(s.items)
	res := Result{Delta: DiffItems(prev, w.Items)}

	// Histories are tool-owned and append-only, and each write contributes at
	// most one observation per score. A single completion update must not be
	// able to manufacture an apparent intermediate step (plan.md §12.2).
	s.items = mergeItems(prev, w.Items)

	if w.Plan != nil {
		firstPlanWrite := s.plan.UnderstandsUserIntent == nil
		s.plan.UserIntention = cloneStringPtr(w.Plan.UserIntention)
		if v := w.Plan.UnderstandsUserIntent; v != nil {
			s.plan.UnderstandsUserIntentHistory = appendScore(s.plan.UnderstandsUserIntentHistory, *v)
			s.plan.UnderstandsUserIntent = cloneUint8Ptr(v)

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
		restore()
		return res, err
	}
	return res, nil
}

// snapshot copies the four pieces of state a transaction touches and returns
// the undo. Slices are copied element-wise, so a write that rewrites an item in
// place is rolled back with everything else.
func (s *Store) snapshot() func() {
	items := cloneItems(s.items)
	goals := cloneGoals(s.goals)
	obs := append([]Observation(nil), s.obs...)
	plan := clonePlan(s.plan)
	return func() {
		s.items, s.goals, s.obs, s.plan = items, goals, obs, plan
	}
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
		g.FeedbackLoop = cloneStringPtr(in.FeedbackLoop)
	}
	if v := in.ClosedFeedbackLoop; v != nil {
		g.ClosedFeedbackLoopHistory = appendScore(g.ClosedFeedbackLoopHistory, *v)
		g.ClosedFeedbackLoop = cloneUint8Ptr(v)
		if *v < QualityGate {
			s.observe(Observation{Kind: KindLoop, Group: in.Group, Score: *v})
		}
	}
	if v := in.EndToEndOwnership; v != nil {
		g.EndToEndOwnershipHistory = appendScore(g.EndToEndOwnershipHistory, *v)
		g.EndToEndOwnership = cloneUint8Ptr(v)
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
		item = cloneItem(item)
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

func dependencyCycle(items []Item) (string, bool) {
	deps := make(map[string][]string, len(items))
	for _, item := range items {
		deps[item.ID] = item.BlockedBy
	}
	state := make(map[string]uint8, len(items)) // 1 visiting, 2 finished
	var visit func(string) (string, bool)
	visit = func(id string) (string, bool) {
		switch state[id] {
		case 1:
			return id, true
		case 2:
			return "", false
		}
		state[id] = 1
		for _, dep := range deps[id] {
			if cycle, ok := visit(dep); ok {
				return cycle, true
			}
		}
		state[id] = 2
		return "", false
	}
	for id := range deps {
		if cycle, ok := visit(id); ok {
			return cycle, true
		}
	}
	return "", false
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
