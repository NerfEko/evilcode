package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Skill is a prompt fragment loaded on demand.
//
// Only the name and one-liner go into the system prompt; the body loads through
// the skill tool when the model actually needs it. That is what keeps the
// prompt cacheable as the skill set grows — a system prompt that changes size
// with every added skill invalidates the cache for every request (plan.md §15).
type Skill struct {
	Name string
	Desc string
	Path string
}

// SkillSet is a loaded skill index.
type SkillSet struct {
	mu     sync.RWMutex
	skills []Skill
	bodies map[string]string
}

// SkillDirs returns the directories searched for skills, nearest first.
func SkillDirs(repoRoot, configDir string) []string {
	var dirs []string
	if repoRoot != "" {
		dirs = append(dirs, filepath.Join(repoRoot, ".evilcode", "skills"))
	}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "skills"))
	}
	return dirs
}

// LoadSkills indexes every `*.md` in the search path.
//
// It reads only the front matter and first paragraph, not the body: indexing a
// hundred skills should not cost a hundred file reads worth of memory, and the
// body is fetched when asked for.
func LoadSkills(dirs []string) *SkillSet {
	set := &SkillSet{bodies: map[string]string{}}
	seen := map[string]bool{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			// Nearest directory wins, so a repo can shadow a global skill.
			if seen[name] {
				continue
			}
			path := filepath.Join(dir, e.Name())
			desc, err := skillSummary(path)
			if err != nil {
				continue
			}
			seen[name] = true
			set.skills = append(set.skills, Skill{Name: name, Desc: desc, Path: path})
		}
	}
	sort.Slice(set.skills, func(i, j int) bool { return set.skills[i].Name < set.skills[j].Name })
	return set
}

// skillSummary reads a skill's one-line description: the `description:` front
// matter field if present, else the first non-heading line.
func skillSummary(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")

	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) == "---" {
				break
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
				return strings.TrimSpace(strings.Trim(v, `"'`)), nil
			}
		}
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || t == "---" || strings.HasPrefix(t, "#") {
			continue
		}
		if len(t) > 120 {
			t = t[:119] + "…"
		}
		return t, nil
	}
	return "(no description)", nil
}

// Index returns the skills for the system prompt.
func (s *SkillSet) Index() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Skill(nil), s.skills...)
}

// Names lists the skill names.
func (s *SkillSet) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.skills))
	for i, sk := range s.skills {
		out[i] = sk.Name
	}
	return out
}

// Body loads and caches a skill's full text.
func (s *SkillSet) Body(name string) (string, error) {
	s.mu.RLock()
	if body, ok := s.bodies[name]; ok {
		s.mu.RUnlock()
		return body, nil
	}
	var path string
	for _, sk := range s.skills {
		if sk.Name == name {
			path = sk.Path
		}
	}
	s.mu.RUnlock()

	if path == "" {
		return "", fmt.Errorf("no skill named %q (available: %s)",
			name, strings.Join(s.Names(), ", "))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	body := stripFrontMatter(string(data))

	s.mu.Lock()
	s.bodies[name] = body
	s.mu.Unlock()
	return body, nil
}

// stripFrontMatter removes a leading `---` block, which is metadata for the
// index rather than instructions for the model.
func stripFrontMatter(s string) string {
	if !strings.HasPrefix(strings.TrimSpace(s), "---") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.TrimLeft(strings.Join(lines[i+1:], "\n"), "\n")
		}
	}
	return s
}

// NewSkillTool builds the on-demand loader.
func NewSkillTool(set *SkillSet) Tool {
	return Tool{
		Name: "skill",
		Desc: "Load a skill's full instructions. The system prompt lists the available " +
			"skills by name; call this before following one.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The skill's name, as listed in the prompt"}
  },
  "required": ["name"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			body, err := set.Body(a.Name)
			if err != nil {
				return Result{}, err
			}
			return Result{Output: body, Intent: "loaded skill " + a.Name}, nil
		},
	}
}
