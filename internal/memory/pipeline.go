package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Embedder turns text into vectors. provider.Provider satisfies it.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// SideCaller runs a cheap one-shot completion through a role.
// config.Router satisfies it.
//
// Every call memory makes goes through the `smol` role, so remembering never
// spends the main model's budget (plan.md §16).
type SideCaller interface {
	SideCall(ctx context.Context, role, system, user string) (string, error)
}

// Stage is where the memory pipeline is in its four steps (plan.md §8.8).
type Stage int

const (
	StageIdle Stage = iota
	StageFind
	StageCheck
	StageInject
	StageUpdate
)

func (s Stage) String() string {
	switch s {
	case StageFind:
		return "searching memories"
	case StageCheck:
		return "checking relevance"
	case StageInject:
		return "injecting context"
	case StageUpdate:
		return "updating memory"
	default:
		return "idle"
	}
}

// Activity is the pipeline's observable state, read by the widget.
type Activity struct {
	Stage      Stage
	Candidates int
	Relevant   int
	Tokens     int
	Saved      int
	Since      time.Time

	// Failed records the last error, so a bank that is quietly doing nothing
	// because the embedder is down says so instead of looking idle.
	Failed string
}

// RecallCount is how many memories passive recall injects. Four is the plan's
// number: enough to be useful, few enough that a bad match is obvious.
const RecallCount = 4

// ExtractEvery is how many turns pass between ambient extraction side-calls.
const ExtractEvery = 8

// ExtractTimeout bounds a detached extraction call.
const ExtractTimeout = 90 * time.Second

// Manager runs the recall and extraction pipelines around a Store.
//
// Everything here is allowed to fail. Memory is an enhancement, and an
// enhancement that can break the turn loop is a liability (plan.md §19).
type Manager struct {
	Store    *Store
	Embedder Embedder
	Router   SideCaller
	Session  string

	// EmbedTimeout bounds an embed call. Passive recall sits in front of every
	// user message, so a hung local daemon must not hang the composer.
	EmbedTimeout time.Duration

	mu       sync.Mutex
	enabled  bool
	act      Activity
	turns    int
	observer func()

	// transcript accumulates the turn text ambient extraction reads. It is
	// cleared after each extraction so the same exchange is never mined twice.
	transcript []string
}

// NewManager builds a manager. A nil store yields a disabled manager, which is
// how `memory = false` is expressed without nil checks at every call site.
func NewManager(store *Store, emb Embedder, router SideCaller, session string, enabled bool) *Manager {
	return &Manager{
		Store:        store,
		Embedder:     emb,
		Router:       router,
		Session:      session,
		EmbedTimeout: 5 * time.Second,
		enabled:      enabled && store != nil,
	}
}

// Enabled reports whether memory is on.
func (m *Manager) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// SetEnabled turns memory on or off at runtime (`/memory on|off`).
func (m *Manager) SetEnabled(on bool) {
	if m == nil || m.Store == nil {
		return
	}
	m.mu.Lock()
	m.enabled = on
	if !on {
		m.act = Activity{}
	}
	m.mu.Unlock()
	m.notify()
}

// OnChange registers a callback fired whenever the activity changes, so the UI
// can repaint without polling.
func (m *Manager) OnChange(f func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.observer = f
	m.mu.Unlock()
}

func (m *Manager) notify() {
	m.mu.Lock()
	f := m.observer
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

// Activity returns the current pipeline state.
func (m *Manager) Activity() Activity {
	if m == nil {
		return Activity{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.act
}

func (m *Manager) setStage(s Stage, mutate func(*Activity)) {
	m.mu.Lock()
	if s != m.act.Stage {
		m.act.Since = time.Now()
	}
	m.act.Stage = s
	if mutate != nil {
		mutate(&m.act)
	}
	m.mu.Unlock()
	m.notify()
}

// embed embeds one string, returning nil when embedding is unavailable.
func (m *Manager) embed(ctx context.Context, text string) []float32 {
	if m.Embedder == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, m.EmbedTimeout)
	defer cancel()

	vecs, err := m.Embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		m.mu.Lock()
		if err != nil {
			m.act.Failed = err.Error()
		}
		m.mu.Unlock()
		return nil
	}
	m.mu.Lock()
	m.act.Failed = ""
	m.mu.Unlock()
	return vecs[0]
}

// QueryVector embeds a query for callers that search the store directly. It
// returns nil when embedding is unavailable, which Search reads as "fall back to
// substring matching".
func (m *Manager) QueryVector(ctx context.Context, query string) []float32 {
	if !m.Enabled() {
		return nil
	}
	return m.embed(ctx, query)
}

// Recall runs passive recall for an incoming user message and returns the tail
// message to append, or "" when nothing cleared the threshold.
//
// The returned hits are for the transcript tile; the string is what the model
// sees.
func (m *Manager) Recall(ctx context.Context, userMsg string) (string, []Hit) {
	if !m.Enabled() {
		return "", nil
	}

	m.setStage(StageFind, func(a *Activity) {
		a.Candidates = m.Store.Len()
		a.Relevant, a.Tokens = 0, 0
	})

	vec := m.embed(ctx, userMsg)
	hits := m.Store.Search(userMsg, vec, RecallCount, RecallThreshold)

	m.setStage(StageCheck, func(a *Activity) { a.Relevant = len(hits) })
	if len(hits) == 0 {
		m.setStage(StageIdle, nil)
		return "", nil
	}

	text := FormatMemories(hits)
	m.setStage(StageInject, func(a *Activity) { a.Tokens = EstimateTokens(text) })
	return text, hits
}

// FormatMemories renders hits as the `<memories>` tail message.
func FormatMemories(hits []Hit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<memories>\n")
	b.WriteString("Things you remember about this user and their work. " +
		"Use them if relevant; do not mention them otherwise.\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "- (%s) %s\n", h.Kind, h.Text)
	}
	b.WriteString("</memories>")
	return b.String()
}

// EstimateTokens is the rough character-count estimate used for the tile and
// the widget. Exactness is not worth a tokenizer here.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Remember stores a memory, embedding it first. It is the `remember` tool's
// implementation and the ambient extractor's sink.
func (m *Manager) Remember(ctx context.Context, text string, kind Kind) (Record, bool, error) {
	if m == nil || m.Store == nil {
		return Record{}, false, fmt.Errorf("memory is not available in this session")
	}
	vec := m.embed(ctx, text)
	rec, merged, err := m.Store.Add(text, kind, m.Session, vec, time.Now())
	if err == nil {
		m.setStage(StageUpdate, func(a *Activity) { a.Saved++ })
	}
	return rec, merged, err
}

// ObserveTurn records a turn's text and reports whether ambient extraction is
// due. The count is per-manager, so a resumed session starts its clock fresh
// rather than immediately extracting from a transcript it just replayed.
func (m *Manager) ObserveTurn(text string) bool {
	if !m.Enabled() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns++
	if s := strings.TrimSpace(text); s != "" {
		m.transcript = append(m.transcript, s)
	}
	return m.turns%ExtractEvery == 0 && len(m.transcript) > 0
}

// peekTranscript returns the accumulated turn text without clearing it, and
// how many turns it covers. The turns stay queued until clearTranscript
// confirms extraction succeeded — a provider error or an unparsable reply
// must not lose them, since there is no other copy to retry from.
func (m *Manager) peekTranscript() (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.transcript, "\n"), len(m.transcript)
}

// clearTranscript drops the first n turns, which is exactly the batch a
// successful extraction just consumed. Turns appended while that extraction
// was in flight land after them and stay queued for the next pass.
func (m *Manager) clearTranscript(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transcript = append([]string(nil), m.transcript[n:]...)
}

const extractSystem = `You extract durable facts worth remembering about a user and their work.

Return a JSON array and nothing else. Each element: {"text": "...", "kind": "fact|preference|project|episode"}.

Remember only what stays true after this conversation ends: stated preferences,
project conventions, tool choices, constraints, names of things. Do not remember
the contents of a file, the result of a command, anything transient, or anything
already obvious from the code.

Return [] if there is nothing worth remembering. That is the common case.`

// Extract runs the ambient extraction side-call over the accumulated turns and
// stores what it finds.
//
// Extraction is deliberately conservative: a memory bank that fills with
// restated file contents is worse than an empty one, because every later recall
// has to outrank the noise.
func (m *Manager) Extract(ctx context.Context) (int, error) {
	if !m.Enabled() || m.Router == nil {
		return 0, nil
	}
	text, n := m.peekTranscript()
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}

	out, err := m.Router.SideCall(ctx, "smol", extractSystem, Truncate(text, 12000))
	if err != nil {
		m.mu.Lock()
		m.act.Failed = err.Error()
		m.mu.Unlock()
		return 0, err
	}

	var items []struct {
		Text string `json:"text"`
		Kind Kind   `json:"kind"`
	}
	if err := json.Unmarshal([]byte(ExtractJSON(out)), &items); err != nil {
		// A small model that answers in prose is a normal failure, not an error
		// worth surfacing: the turns stay queued and the next extraction tries
		// again over the same (plus any newly arrived) text.
		return 0, nil
	}
	m.clearTranscript(n)

	saved := 0
	for _, it := range items {
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		if !it.Kind.Valid() {
			it.Kind = KindFact
		}
		if _, merged, err := m.Remember(ctx, it.Text, it.Kind); err == nil && !merged {
			saved++
		}
	}
	m.setStage(StageIdle, nil)
	return saved, nil
}

// ExtractJSON pulls the first JSON array out of a model's reply, tolerating the
// code fences and preamble a small model tends to wrap it in.
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			return s[i : j+1]
		}
	}
	return s
}

// Truncate caps text at n bytes on a rune boundary.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[truncated]"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

const reflectSystem = `You answer a question using only the remembered facts given to you.

Synthesize across them. If the memories do not answer the question, say so
plainly rather than guessing. Be brief.`

// Reflect answers a question over the whole memory bank via the smol role.
func (m *Manager) Reflect(ctx context.Context, question string) (string, error) {
	if !m.Enabled() {
		return "", fmt.Errorf("memory is off; turn it on with /memory on")
	}
	if m.Router == nil {
		return "", fmt.Errorf("no model is configured for the smol role")
	}

	vec := m.embed(ctx, question)
	// A wider net than passive recall: reflection is an explicit, expensive ask,
	// so it should read broadly rather than only the nearest few.
	hits := m.Store.Search(question, vec, 24, RecallThreshold-0.15)
	if len(hits) == 0 {
		return "", fmt.Errorf("no memories relate to that")
	}

	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "- (%s) %s\n", h.Kind, h.Text)
	}
	return m.Router.SideCall(ctx, "smol",
		reflectSystem, "Memories:\n"+b.String()+"\nQuestion: "+question)
}

const consolidateSystem = `You write a one-paragraph summary of a coding session.

Cover what was worked on and what was decided. Skip mechanics — no file lists,
no command output. Write it so it is useful months later to someone searching
for this session. Two or three sentences.`

// Consolidate summarizes a session on close and stores it as an episode, whose
// embedding is what makes the session searchable from the picker.
func (m *Manager) Consolidate(ctx context.Context, transcript string) (Record, error) {
	if !m.Enabled() || m.Router == nil || strings.TrimSpace(transcript) == "" {
		return Record{}, nil
	}
	summary, err := m.Router.SideCall(ctx, "smol", consolidateSystem, Truncate(transcript, 24000))
	if err != nil || strings.TrimSpace(summary) == "" {
		return Record{}, err
	}
	rec, _, err := m.Remember(ctx, summary, KindEpisode)
	return rec, err
}

// SearchSessions returns the episode memories matching a query, which is the
// session picker's semantic search (plan.md §19).
func (m *Manager) SearchSessions(ctx context.Context, query string, n int) []Hit {
	if !m.Enabled() {
		return nil
	}
	vec := m.embed(ctx, query)
	var out []Hit
	for _, h := range m.Store.Search(query, vec, 0, RecallThreshold-0.15) {
		if h.Kind != KindEpisode || h.Session == "" {
			continue
		}
		out = append(out, h)
		if n > 0 && len(out) == n {
			break
		}
	}
	return out
}
