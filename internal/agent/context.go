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
	onAppend func(provider.Message)
}

// NewConversation starts a conversation with a fixed system prompt.
func NewConversation(system string) *Conversation {
	return &Conversation{system: system}
}

// Persist registers a sink for every appended message.
//
// Replay is deliberately not persisted: a resumed session appends the messages
// it just read, and writing them again would double the file on every resume.
func (c *Conversation) Persist(sink func(provider.Message)) {
	c.mu.Lock()
	c.onAppend = sink
	c.mu.Unlock()
}

// Append adds messages to the tail.
func (c *Conversation) Append(msgs ...provider.Message) {
	c.mu.Lock()
	c.messages = append(c.messages, msgs...)
	sink := c.onAppend
	c.mu.Unlock()

	// Outside the lock: the sink writes to a file, and holding the
	// conversation lock across a disk write would stall every reader.
	if sink != nil {
		for _, m := range msgs {
			sink(m)
		}
	}
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

// identity is the base system prompt. It is deliberately short: the budget is
// well under ~1200 tokens (plan.md §15), because every token here is paid on
// every request for the life of the session.
const identity = `You are evilcode, a coding agent working in a terminal alongside one developer.

Work directly on the codebase with the tools available. Read before you edit.
Prefer small, verifiable changes, and run the project's own checks to confirm
them rather than asserting success.

Be concise. The developer reads your output in a terminal, not a document.
Skip preamble, skip summaries of what you are about to do, and skip restating
the request. When you finish, say what changed and what you verified.

If a request is ambiguous in a way that changes the work, ask. Otherwise pick
the reading a careful colleague would and say which one you picked.`

// toolGuidance teaches the habits that keep the loop cheap and correct.
const toolGuidance = `Tool use:
- Call tools in parallel when their results do not depend on each other.
- read a file before editing it; edit needs the exact current text.
- If an edit reports the old string was not found, re-read the file rather than
  guessing again — the file has changed or the indentation differs.
- Use grep to find things and glob to list them; do not shell out to find or
  grep through bash.
- bash keeps its working directory between calls.`

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

// BuildSystemPrompt assembles the stable system prompt. Skills contribute only
// their names and one-liners; bodies load on demand through the skill tool,
// which is what keeps the prompt cacheable as the skill set grows.
func BuildSystemPrompt(pc ProjectContext, skills []Skill, extra string) string {
	var b strings.Builder
	b.WriteString(identity)
	b.WriteString("\n\n")
	b.WriteString(toolGuidance)

	if pc.Root != "" {
		fmt.Fprintf(&b, "\n\nWorking directory: %s", pc.Root)
	}
	if len(skills) > 0 {
		b.WriteString("\n\nSkills available (load a body with the skill tool before following it):")
		for _, s := range skills {
			fmt.Fprintf(&b, "\n- %s: %s", s.Name, s.Desc)
		}
	}
	if pc.Instructions != "" {
		b.WriteString("\n\n---\n\nProject instructions:\n\n")
		b.WriteString(pc.Instructions)
	}
	if extra != "" {
		b.WriteString("\n\n")
		b.WriteString(extra)
	}
	return b.String()
}
