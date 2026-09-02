package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"evilcode/internal/provider"
)

// Conversation is the message list. It is append-only: earlier messages are
// never rewritten or reordered, and dynamic state (todos, memories, nudges)
// enters as new tail messages. That is what keeps the provider's prompt cache
// warm across a turn (plan.md invariant 2).
//
// /compact is the one sanctioned rewrite, and it bumps Epoch so anything
// caching by message index knows to start over.
type Conversation struct {
	mu       sync.RWMutex
	system   string
	messages []provider.Message
	epoch    int

	// onAppend persists each message as it lands. It is here rather than in
	// each frontend because §18 makes the JSONL file the source of truth —
	// "resume = replay" — and a frontend that forgets to write is a session
	// that silently resumes empty. Every message goes through Append, so this
	// is the one place that cannot be forgotten.
	onAppend func(provider.Message) error

	// sinkMu orders the sink. It is taken while mu is held and released after
	// the write, so two concurrent appends reach disk in the order they reached
	// memory — without holding the conversation lock across a disk write, which
	// would stall every reader.
	sinkMu sync.Mutex

	// persistErr keeps the first write failure so a turn can report it, under
	// sinkMu. A transcript that is quietly behind the conversation is the kind
	// of loss nobody notices until a resume comes back short.
	persistErr error
}

// NewConversation starts a conversation with a fixed system prompt.
func NewConversation(system string) *Conversation {
	return &Conversation{system: system}
}

// Persist registers a sink for every appended message.
//
// Replay is deliberately not persisted: a resumed session appends the messages
// it just read, and writing them again would double the file on every resume.
func (c *Conversation) Persist(sink func(provider.Message) error) {
	c.mu.Lock()
	c.onAppend = sink
	c.mu.Unlock()
}

// Append adds messages to the tail.
func (c *Conversation) Append(msgs ...provider.Message) {
	c.mu.Lock()
	c.messages = append(c.messages, msgs...)
	sink := c.onAppend
	if sink == nil {
		c.mu.Unlock()
		return
	}
	// Claim the sink's turn before releasing the conversation, so writes reach
	// disk in the order they reached memory. The write itself happens outside
	// the conversation lock: holding that across a disk write would stall every
	// reader.
	c.sinkMu.Lock()
	defer c.sinkMu.Unlock()
	c.mu.Unlock()

	for _, m := range msgs {
		if err := sink(m); err != nil && c.persistErr == nil {
			c.persistErr = err
		}
	}
}

// PersistErr returns the first write failure since the last call and clears it,
// so a turn reports a broken transcript once rather than once per message.
func (c *Conversation) PersistErr() error {
	// Guarded by sinkMu rather than mu: mu is held while sinkMu is taken, and
	// guarding it the other way round would invert the order and deadlock.
	c.sinkMu.Lock()
	defer c.sinkMu.Unlock()
	err := c.persistErr
	c.persistErr = nil
	return err
}

// Messages returns the full list to send, system prompt first. The slice is a
// copy, so a caller cannot mutate history by accident.
func (c *Conversation) Messages() []provider.Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]provider.Message, 0, len(c.messages)+1)
	if c.system != "" {
		out = append(out, provider.Message{Role: provider.RoleSystem, Content: c.system})
	}
	return append(out, c.messages...)
}

const (
	// modelToolOutputProtectTokens keeps the newest completed tool output in
	// full. This is the same protection window OpenCode uses before clearing
	// older tool results from a model request.
	modelToolOutputProtectTokens = 40_000

	// modelToolOutputMinimumPruneTokens avoids rewriting the request for a
	// negligible saving. The durable transcript is never changed by this
	// projection, so the threshold is only about request stability.
	modelToolOutputMinimumPruneTokens = 20_000

	modelToolOutputPlaceholder = "[Old tool result content cleared]"
)

// MessagesForModel returns a request-safe projection of the conversation.
// Conversation.Messages remains the complete durable history; this view
// follows OpenCode's tool-result pruning rule and clears only old completed
// tool output after at least one newer user turn exists. Keeping the full
// transcript for the UI, compaction summary, and resume while sending a small
// projection to the provider is what prevents repeated file reads from
// consuming the entire Codex context.
func (c *Conversation) MessagesForModel() []provider.Message {
	// Messages returns a distinct slice of message values. The projection only
	// replaces fields on those values, so there is no need to deep-copy every
	// tool-call argument and image on every provider request.
	return pruneModelToolResults(c.Messages())
}

func pruneModelToolResults(msgs []provider.Message) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}

	var candidates []int
	total, pruned := 0, 0
	userTurns := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role == provider.RoleUser {
			userTurns++
		}
		// Do not revisit history that has already been represented by a
		// compaction summary. The summary is itself small, and the tail after
		// it is the only part that can still contain live tool output.
		if isCompactionMarker(msg) {
			break
		}
		if userTurns < 2 || msg.Role != provider.RoleTool || msg.ToolName == "skill" {
			continue
		}

		total += modelToolOutputTokens(msg)
		if total > modelToolOutputProtectTokens {
			candidates = append(candidates, i)
			pruned += modelToolOutputTokens(msg)
		}
	}
	if pruned <= modelToolOutputMinimumPruneTokens {
		return msgs
	}
	for _, i := range candidates {
		msgs[i].Content = modelToolOutputPlaceholder
		msgs[i].Images = nil
		// Tool results do not normally carry provider items, but clearing them
		// here keeps a custom tool from smuggling the same large payload through
		// a second field after its visible output was pruned.
		msgs[i].ProviderItems = nil
	}
	return msgs
}

func modelToolOutputTokens(msg provider.Message) int {
	n := len(msg.Content)
	if len(msg.Images) > 0 {
		// Images are encoded separately by providers. Reserve a stable small
		// estimate so an old image-only result can still be selected, without
		// pretending compressed bytes are text tokens.
		n += len(msg.Images) * 256 * 4
	}
	if n <= 0 {
		return 1
	}
	return (n + 3) / 4
}

// Len reports how many non-system messages are held.
func (c *Conversation) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.messages)
}

// Epoch reports the compaction generation.
func (c *Conversation) Epoch() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.epoch
}

// Last returns the final message, or false when the conversation is empty.
func (c *Conversation) Last() (provider.Message, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.messages) == 0 {
		return provider.Message{}, false
	}
	return c.messages[len(c.messages)-1], true
}

// Reset replaces the history wholesale, which `/rewind` needs: the messages it
// keeps are the truth, and appending to a stale list would double them.
//
// Like Compact it bumps the epoch, since anything caching by message index is
// now looking at a different conversation.
func (c *Conversation) Reset(msgs []provider.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append([]provider.Message(nil), msgs...)
	c.epoch++
}

// Sync replaces a remote conversation mirror without changing its compaction
// epoch or persisting anything. The daemon is the owner of the transcript; an
// attached client only needs a current read model for /context and diagnostics.
func (c *Conversation) Sync(msgs []provider.Message, epoch ...int) {
	c.mu.Lock()
	c.messages = append([]provider.Message(nil), msgs...)
	if len(epoch) > 0 {
		c.epoch = epoch[0]
	}
	c.mu.Unlock()
}

// CompactedPrefix marks the synthetic message a compaction leaves behind.
const CompactedPrefix = "[conversation compacted]\n\n"

// CompactMessage is what a compacted history collapses to.
func CompactMessage(summary string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: CompactedPrefix + summary}
}

// SystemPrompt returns the stable system prompt.
func (c *Conversation) SystemPrompt() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.system
}

// SetSystemPrompt refreshes dynamic prompt metadata without rewriting the
// append-only conversation. Skill reload uses this to publish a new index while
// preserving every user, assistant, and tool message already in the session.
func (c *Conversation) SetSystemPrompt(system string) {
	c.mu.Lock()
	c.system = system
	c.mu.Unlock()
}

// ProjectContext is the assembled per-repo context.
type ProjectContext struct {
	// Instructions is the concatenated content of the AGENTS.md/CLAUDE.md
	// files that were found.
	Instructions string

	// Sources lists where the instructions came from, for the header and for
	// answering "why is it behaving like that".
	Sources []string

	// Root is the detected project root.
	Root string
}

// instructionFiles are read in this order; both names are supported because
// both conventions exist in the wild.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// LoadProjectContext searches cwd, then the git root, then the user config
// directory (plan.md §15). Files are concatenated in that order, so the most
// specific instructions come first and a general fallback still applies.
func LoadProjectContext(cwd, configDir string) ProjectContext {
	var pc ProjectContext
	pc.Root = cwd

	dirs := []string{cwd}
	if root := gitRoot(cwd); root != "" && root != cwd {
		dirs = append(dirs, root)
		pc.Root = root
	}
	if configDir != "" {
		dirs = append(dirs, configDir)
	}

	var b strings.Builder
	seen := map[string]bool{}
	for _, dir := range dirs {
		for _, name := range instructionFiles {
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}
			seen[path] = true
			data, err := os.ReadFile(path)
			if err != nil || len(strings.TrimSpace(string(data))) == 0 {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "# From %s\n\n%s", path, strings.TrimSpace(string(data)))
			pc.Sources = append(pc.Sources, path)
		}
	}
	pc.Instructions = b.String()
	return pc
}

func gitRoot(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Skill is one entry in the skills index.
type Skill struct {
	Name string
	Desc string
	Path string
}

// PromptProfile selects the stable system-prompt contract for a model family.
// Codex gets a compact execution-oriented contract; the default keeps the
// fuller provider-neutral contract used by other backends.
type PromptProfile string

const (
	PromptProfileDefault PromptProfile = "default"
	PromptProfileCodex   PromptProfile = "codex"
)

// PromptProfileForProvider chooses the profile from the resolved backend and
// model reference. The provider type is authoritative for a model such as
// gpt-5.6-luna, while the name fallback also covers a Codex-named model routed
// through an OpenAI-compatible provider and an attached client whose concrete
// provider has not been rebuilt yet.
func PromptProfileForProvider(p provider.Provider, providerName, model string) PromptProfile {
	name := strings.TrimSpace(providerName)
	providerMatches := false
	if p != nil {
		if name == "" {
			name = p.Name()
			providerMatches = true
		} else {
			providerMatches = strings.EqualFold(name, strings.TrimSpace(p.Name()))
		}
	}
	_, concreteCodex := p.(*provider.Codex)
	if (providerMatches && concreteCodex) || strings.EqualFold(name, "codex") ||
		strings.Contains(strings.ToLower(strings.TrimSpace(model)), "codex") {
		return PromptProfileCodex
	}
	return PromptProfileDefault
}

// BuildSystemPrompt assembles the stable system prompt. Skills contribute only
// their names and one-liners; bodies load on demand through the skill tool,
// which is what keeps the prompt cacheable as the skill set grows.
func BuildSystemPrompt(pc ProjectContext, skills []Skill, extra string) string {
	return BuildSystemPromptForProfile(pc, skills, extra, PromptProfileDefault)
}

// BuildSystemPromptForProvider selects the prompt profile for a resolved
// provider/model pair. Keeping this at the prompt boundary makes every
// frontend (headless, TUI, and daemon) apply the same model-specific contract.
func BuildSystemPromptForProvider(pc ProjectContext, skills []Skill, extra string,
	p provider.Provider, providerName, model string) string {
	return BuildSystemPromptForProfile(pc, skills, extra,
		PromptProfileForProvider(p, providerName, model))
}

// BuildSystemPromptForProfile assembles the stable system prompt for profile.
func BuildSystemPromptForProfile(pc ProjectContext, skills []Skill, extra string,
	profile PromptProfile) string {
	var b strings.Builder
	if profile == PromptProfileCodex {
		b.WriteString(codexPrompt)
	} else {
		b.WriteString(identity)
		b.WriteString("\n\n")
		b.WriteString(toolGuidance)
	}

	if pc.Root != "" {
		fmt.Fprintf(&b, "\n\nWorking directory: %s", pc.Root)
	}
	if len(skills) > 0 {
		b.WriteString("\n\nSkills available (optional instruction bundles; load a body with the skill tool before following one):")
		for _, s := range skills {
			fmt.Fprintf(&b, "\n- %s: %s", s.Name, s.Desc)
		}
	}
	if pc.Instructions != "" {
		b.WriteString("\n\n---\n\nProject instructions (repository or user supplied; follow when relevant):\n\n")
		b.WriteString(pc.Instructions)
	}
	if extra != "" {
		b.WriteString("\n\n")
		b.WriteString(extra)
	}
	return b.String()
}
