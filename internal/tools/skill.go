package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"evilcode/internal/tools/commandrisk"
)

// Skill is one entry in the cheap, prompt-safe skill index. Path is the
// directory that owns the skill, not the markdown file: directory skills can
// ship scripts and reference material beside SKILL.md, and /skills needs to
// make that source visible when overlays shadow one another.
type Skill struct {
	Name string
	Desc string
	Path string
}

type skillFrontMatter struct {
	Description     string
	AllowedTools    []string
	AllowedToolsSet bool
}

type cachedSkillBody struct {
	body    string
	modTime time.Time
	size    int64
}

type cachedSkillVector struct {
	summary string
	vector  []float32
}

// SkillSet is a loaded skill index. The index is deliberately separate from
// bodies: startup and the system prompt read metadata only, while the full
// instructions arrive through the skill tool on demand.
type SkillSet struct {
	mu sync.RWMutex

	dirs    []string
	skills  []Skill
	files   map[string]string
	meta    map[string]skillFrontMatter
	bodies  map[string]cachedSkillBody
	vectors map[string]cachedSkillVector

	onLoad func(*ToolPolicy)
}

// SkillDirs returns the directories searched for skills, nearest first.
//
// The order is intentionally explicit. A repository overlay wins over its
// user-level library, and the first occurrence of a name is the one the model
// sees. The home directory is resolved here rather than passed by callers so
// TUI, headless, and daemon sessions cannot quietly disagree.
func SkillDirs(repoRoot, configDir string) []string {
	var dirs []string
	if repoRoot != "" {
		dirs = append(dirs,
			filepath.Join(repoRoot, ".evilcode", "skills"),
			filepath.Join(repoRoot, ".agents", "skills"),
		)
	}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".agents", "skills"),
			filepath.Join(home, ".claude", "skills"),
		)
	}
	return uniquePaths(dirs)
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// LoadSkills indexes flat <name>.md skills and directory <name>/SKILL.md
// skills. It reads only front matter and the first prose paragraph at startup;
// the body is fetched when asked for.
func LoadSkills(dirs []string) *SkillSet {
	set := &SkillSet{
		dirs:    uniquePaths(dirs),
		files:   map[string]string{},
		meta:    map[string]skillFrontMatter{},
		bodies:  map[string]cachedSkillBody{},
		vectors: map[string]cachedSkillVector{},
	}
	set.Reload()
	return set
}

// Reload rereads the index and forgets cached bodies and embeddings. Missing
// directories remain fine, just as they are at startup; this makes authoring a
// new user skill a normal /skills reload rather than a process restart.
func (s *SkillSet) Reload() {
	if s == nil {
		return
	}
	skills, files, meta := loadSkillIndex(s.dirs)
	s.mu.Lock()
	s.skills = skills
	s.files = files
	s.meta = meta
	s.bodies = map[string]cachedSkillBody{}
	s.vectors = map[string]cachedSkillVector{}
	s.mu.Unlock()
}

func loadSkillIndex(dirs []string) ([]Skill, map[string]string, map[string]skillFrontMatter) {
	var skills []Skill
	files := map[string]string{}
	meta := map[string]skillFrontMatter{}
	seen := map[string]bool{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		// A directory skill is the canonical global layout. Process it before
		// flat files in the same overlay so an accidental pair has one stable
		// and useful meaning.
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				continue
			}
			name := entry.Name()
			bodyPath := filepath.Join(path, "SKILL.md")
			if seen[name] {
				continue
			}
			if _, err := os.Stat(bodyPath); err != nil {
				continue
			}
			if addSkill(&skills, files, meta, seen, name, path, bodyPath) {
				continue
			}
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".md")
			if seen[name] {
				continue
			}
			addSkill(&skills, files, meta, seen, name, dir, filepath.Join(dir, entry.Name()))
		}
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, files, meta
}

func addSkill(skills *[]Skill, files map[string]string, meta map[string]skillFrontMatter,
	seen map[string]bool, name, sourceDir, bodyPath string) bool {
	fm, body, err := readSkillFile(bodyPath)
	if err != nil {
		return false
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		desc = firstDescription(body)
	}
	seen[name] = true
	files[name] = bodyPath
	meta[name] = fm
	*skills = append(*skills, Skill{Name: name, Desc: desc, Path: sourceDir})
	return true
}

// skillSummary reads a skill's description from YAML front matter, falling
// back to the first prose line when no description is declared.
func skillSummary(path string) (string, error) {
	fm, body, err := readSkillFile(path)
	if err != nil {
		return "", err
	}
	if desc := strings.TrimSpace(fm.Description); desc != "" {
		return desc, nil
	}
	return firstDescription(body), nil
}

func firstDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t == "---" || strings.HasPrefix(t, "#") {
			continue
		}
		if len(t) > 120 {
			t = t[:backToRuneBoundary(t, 119)] + "…"
		}
		return t
	}
	return "(no description)"
}

func readSkillFile(path string) (skillFrontMatter, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return skillFrontMatter{}, "", err
	}
	fm, body, _, err := parseSkillFile(string(data))
	return fm, body, err
}

// parseSkillFile handles the small YAML front-matter dialect used by skills.
// It intentionally understands YAML scalars and block scalars rather than
// looking for a literal prefix: `description: >` is metadata, not a
// description consisting of the character `>`.
func parseSkillFile(text string) (skillFrontMatter, string, bool, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return skillFrontMatter{}, text, false, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" || strings.TrimSpace(lines[i]) == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		// Treat an unfinished header as an ordinary markdown file. A half-written
		// skill should remain loadable while it is being edited.
		return skillFrontMatter{}, text, false, nil
	}

	var fm skillFrontMatter
	for i := 1; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) != 0 {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "description":
			if isBlockScalar(value) {
				var block string
				block, i = readBlockScalar(lines, i+1, value)
				fm.Description = block
			} else {
				fm.Description = parseYAMLScalar(value)
			}
		case "allowed-tools", "allowed_tools":
			fm.AllowedToolsSet = true
			if value == "" {
				var list []string
				list, i = readYAMLList(lines, i+1, end)
				fm.AllowedTools = list
			} else {
				fm.AllowedTools = splitToolSpecs(parseYAMLListValue(value))
			}
		}
	}

	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n")
	return fm, body, true, nil
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}

func isBlockScalar(value string) bool {
	return strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|")
}

func readBlockScalar(lines []string, start int, marker string) (string, int) {
	var raw []string
	minIndent := 1 << 30
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			raw = append(raw, "")
			continue
		}
		indent := leadingSpaces(line)
		if indent == 0 {
			break
		}
		if indent < minIndent {
			minIndent = indent
		}
		raw = append(raw, line)
	}
	if minIndent == 1<<30 {
		return "", i - 1
	}
	content := make([]string, len(raw))
	for n, line := range raw {
		if line == "" {
			continue
		}
		if len(line) >= minIndent {
			content[n] = line[minIndent:]
		} else {
			content[n] = ""
		}
	}

	chomp := ""
	if strings.HasSuffix(marker, "-") {
		chomp = "-"
	} else if strings.HasSuffix(marker, "+") {
		chomp = "+"
	}
	var value string
	if strings.HasPrefix(marker, ">") {
		value = foldYAMLLines(content)
	} else {
		value = strings.Join(content, "\n")
	}
	if chomp != "-" {
		value += "\n"
	}
	return value, i - 1
}

func foldYAMLLines(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			if lines[i-1] == "" || line == "" {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(line)
	}
	return b.String()
}

func readYAMLList(lines []string, start, end int) ([]string, int) {
	var out []string
	i := start
	for ; i < end; i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if leadingSpaces(line) == 0 {
			break
		}
		value := strings.TrimSpace(line)
		if !strings.HasPrefix(value, "-") {
			break
		}
		out = append(out, parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(value, "-"))))
	}
	return out, i - 1
}

func parseYAMLListValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func parseYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

// splitToolSpecs splits comma-separated allowed-tools values while keeping a
// comma inside a function-style Bash(...) spec intact.
func splitToolSpecs(value string) []string {
	var out []string
	start, depth := 0, 0
	quoted := byte(0)
	flush := func(end int) {
		if spec := strings.TrimSpace(value[start:end]); spec != "" {
			out = append(out, parseYAMLScalar(spec))
		}
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\'', '"':
			if quoted == 0 {
				quoted = value[i]
			} else if quoted == value[i] {
				quoted = 0
			}
		case '(':
			if quoted == 0 {
				depth++
			}
		case ')':
			if quoted == 0 && depth > 0 {
				depth--
			}
		case ',':
			if quoted == 0 && depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(value))
	return out
}

// Index returns the skills for the system prompt.
func (s *SkillSet) Index() []Skill {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Skill(nil), s.skills...)
}

// Names lists the skill names.
func (s *SkillSet) Names() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.skills))
	for i, sk := range s.skills {
		out[i] = sk.Name
	}
	return out
}

// Source returns the owning directory for a named skill.
func (s *SkillSet) Source(name string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, skill := range s.skills {
		if skill.Name == name {
			return skill.Path
		}
	}
	return ""
}

// SetOnLoad receives the immutable tool policy declared by the next loaded
// skill. It is wired by the agent layer so tools stays independent of agent.
func (s *SkillSet) SetOnLoad(fn func(*ToolPolicy)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onLoad = fn
	s.mu.Unlock()
}

func (s *SkillSet) activate(name string) {
	s.mu.RLock()
	fm, ok := s.meta[name]
	fn := s.onLoad
	s.mu.RUnlock()
	if fn == nil {
		return
	}
	if !ok || !fm.AllowedToolsSet {
		fn(nil)
		return
	}
	fn(NewToolPolicy(name, fm.AllowedTools))
}

// Body loads a skill and rereads it whenever its file mtime changes. The
// source directory is included in the returned tool text so sibling material
// is discoverable instead of being an invisible implementation detail.
func (s *SkillSet) Body(name string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("skills are not configured")
	}
	s.mu.RLock()
	path := s.files[name]
	cached, hasCached := s.bodies[name]
	s.mu.RUnlock()
	if path == "" {
		return "", fmt.Errorf("no skill named %q (available: %s)",
			name, strings.Join(s.Names(), ", "))
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if hasCached && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		return formatSkillBody(s.Source(name), cached.body), nil
	}

	fm, body, err := readSkillFile(path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.bodies[name] = cachedSkillBody{body: body, modTime: info.ModTime(), size: info.Size()}
	s.meta[name] = fm
	s.mu.Unlock()
	return formatSkillBody(s.Source(name), body), nil
}

func formatSkillBody(source, body string) string {
	if source == "" {
		return body
	}
	return "Skill directory: " + source + "\n\n" + body
}

// stripFrontMatter removes a leading YAML block, which is metadata for the
// index rather than instructions for the model.
func stripFrontMatter(s string) string {
	_, body, has, err := parseSkillFile(s)
	if err == nil && has {
		return body
	}
	return s
}

// NewSkillTool builds the on-demand loader.
func NewSkillTool(set *SkillSet) Tool {
	return Tool{
		Name: "skill",
		Desc: "Load a skill's full instructions and supporting context. The system prompt " +
			"lists available skills by name; call this only when the skill is relevant to " +
			"the current request, and do not load unrelated skills. Loading a skill is not " +
			"a reason to pause implementation: use its instructions in the current task. " +
			"Treat the loaded body as instructions only within that skill's stated scope.",
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
			set.activate(a.Name)
			return Result{Output: body, Intent: "loaded skill " + a.Name}, nil
		},
	}
}

// Embedder is the small part of provider.Provider needed for optional skill
// retrieval. Keeping it here avoids coupling tools to memory or a frontend.
type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

// SkillRetrievalThreshold is deliberately higher than memory's broad recall
// cutoff: a skill suggestion is an instruction-adjacent nudge, so a weak match
// should stay silent rather than add prompt noise.
const SkillRetrievalThreshold = 0.72

// Relevant returns a prompt tail for the strongest semantically matching skill,
// or an empty string when no match is strong enough. Summaries are embedded and
// cached; the full body remains one skill-tool call away.
func (s *SkillSet) Relevant(ctx context.Context, query string, emb Embedder) string {
	if s == nil || emb == nil || strings.TrimSpace(query) == "" {
		return ""
	}
	s.mu.RLock()
	snapshot := append([]Skill(nil), s.skills...)
	missing := make([]Skill, 0, len(snapshot))
	for _, skill := range snapshot {
		cached, ok := s.vectors[skill.Name]
		if !ok || cached.summary != skill.Desc || len(cached.vector) == 0 {
			missing = append(missing, skill)
		}
	}
	s.mu.RUnlock()

	if len(missing) > 0 {
		texts := make([]string, len(missing))
		for i, skill := range missing {
			texts[i] = skill.Desc
		}
		vectors, err := emb.Embed(ctx, texts)
		if err != nil || len(vectors) != len(missing) {
			return ""
		}
		s.mu.Lock()
		for i, skill := range missing {
			s.vectors[skill.Name] = cachedSkillVector{summary: skill.Desc, vector: vectors[i]}
		}
		s.mu.Unlock()
	}

	queryVectors, err := emb.Embed(ctx, []string{query})
	if err != nil || len(queryVectors) == 0 {
		return ""
	}
	queryVector := queryVectors[0]
	var best Skill
	bestScore := 0.0
	s.mu.RLock()
	for _, skill := range s.skills {
		score := cosine(queryVector, s.vectors[skill.Name].vector)
		if score > bestScore {
			best, bestScore = skill, score
		}
	}
	s.mu.RUnlock()
	if best.Name == "" || bestScore < SkillRetrievalThreshold {
		return ""
	}
	desc := strings.Join(strings.Fields(best.Desc), " ")
	return fmt.Sprintf("<relevant-skill>\n%s: %s\nThe full instructions are one `skill` tool call away; load %q before following it.\n</relevant-skill>",
		best.Name, desc, best.Name)
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}

// ToolPolicy is the immutable restriction a loaded skill declares. A nil
// policy means unrestricted; a non-nil policy is checked before execution.
type ToolPolicy struct {
	skill string
	rules []toolRule
	text  []string
}

type toolRule struct {
	name   string
	prefix string
}

// NewToolPolicy parses Claude-style allowed-tools names into evilcode tool
// names. In particular Bash(agent-browser:*) narrows the bash tool to the
// requested command prefix, rather than accidentally granting every shell.
func NewToolPolicy(skill string, specs []string) *ToolPolicy {
	p := &ToolPolicy{skill: skill, text: append([]string(nil), specs...)}
	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			continue
		}
		lower := strings.ToLower(spec)
		if strings.HasPrefix(lower, "bash(") && strings.HasSuffix(spec, ")") {
			command := strings.TrimSpace(spec[len("Bash(") : len(spec)-1])
			command = strings.TrimSuffix(command, ":*")
			command = strings.TrimSuffix(command, "*")
			p.rules = append(p.rules, toolRule{name: "bash", prefix: strings.TrimSpace(command)})
			continue
		}
		name := strings.ToLower(spec)
		switch name {
		case "read":
			name = "read"
		case "write":
			name = "write"
		case "edit":
			name = "edit"
		case "grep":
			name = "grep"
		case "glob":
			name = "glob"
		case "bash":
			name = "bash"
		default:
			name = strings.ReplaceAll(name, "-", "_")
		}
		p.rules = append(p.rules, toolRule{name: name})
	}
	return p
}

// Check returns an error when a call is outside the active skill's policy.
func (p *ToolPolicy) Check(call Call) error {
	if p == nil {
		return nil
	}
	// Loading another skill is always allowed so a policy cannot strand the
	// model in a skill it has finished with.
	if call.Name == "skill" {
		return nil
	}
	for _, rule := range p.rules {
		if rule.name != call.Name {
			continue
		}
		if rule.name != "bash" || rule.prefix == "" {
			return nil
		}
		var args struct {
			Cmd     string `json:"cmd"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return fmt.Errorf("tool %q is blocked by skill %q: invalid command arguments", call.Name, p.skill)
		}
		command := strings.TrimSpace(args.Cmd)
		if command == "" {
			command = strings.TrimSpace(args.Command)
		}
		if bashCommandMatchesPrefix(command, rule.prefix) {
			return nil
		}
	}
	return fmt.Errorf("tool %q is not allowed by skill %q (allowed: %s)",
		call.Name, p.skill, strings.Join(p.text, ", "))
}

// bashCommandMatchesPrefix accepts one simple shell command whose leading
// words exactly match the allowed prefix. A raw string prefix check also grants
// every shell program chained after `;`, `&&`, `&`, a redirect, or command
// substitution, which turns a narrow Bash(agent-browser:*) declaration into
// unrestricted shell access.
func bashCommandMatchesPrefix(command, prefix string) bool {
	commandSegments := commandrisk.SplitSegments(commandrisk.Tokenize(command))
	prefixSegments := commandrisk.SplitSegments(commandrisk.Tokenize(prefix))
	if len(commandSegments) != 1 || len(prefixSegments) != 1 {
		return false
	}
	commandTokens, prefixTokens := commandSegments[0], prefixSegments[0]
	if len(prefixTokens) == 0 || len(commandTokens) < len(prefixTokens) {
		return false
	}
	for _, token := range commandTokens {
		if token.Malformed || token.IsOperator || token.IsRedirectTarget ||
			strings.Contains(token.Text, "$(") || strings.Contains(token.Text, "`") {
			return false
		}
	}
	for index, token := range prefixTokens {
		if token.Malformed || token.IsOperator || token.IsRedirectTarget ||
			commandTokens[index].Text != token.Text {
			return false
		}
	}
	return true
}
