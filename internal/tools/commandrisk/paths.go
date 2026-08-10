package commandrisk

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Context supplies the filesystem boundaries used by the policy. No path is
// stat'ed: the classifier is lexical so a missing target cannot evade it.
type Context struct {
	WorkspaceRoot string
	WorkingDir    string
	HomeDir       string
	ConfigDir     string
	DataDir       string
}

func ContextFromPaths(workspaceRoot, workingDir, configDir, dataDir string) Context {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return Context{
		WorkspaceRoot: absolutePath(workspaceRoot, workingDir),
		WorkingDir:    absolutePath(workingDir, workspaceRoot),
		HomeDir:       absolutePath(home, ""),
		ConfigDir:     absolutePath(configDir, home),
		DataDir:       absolutePath(dataDir, home),
	}
}

func (c Context) normalized() Context {
	if c.WorkingDir == "" && c.WorkspaceRoot != "" {
		c.WorkingDir = c.WorkspaceRoot
	}
	if c.WorkingDir == "" {
		c.WorkingDir, _ = os.Getwd()
	}
	if c.WorkspaceRoot == "" {
		c.WorkspaceRoot = c.WorkingDir
	}
	if c.HomeDir == "" {
		c.HomeDir, _ = os.UserHomeDir()
	}
	c.WorkingDir = absolutePath(c.WorkingDir, "")
	c.WorkspaceRoot = absolutePath(c.WorkspaceRoot, c.WorkingDir)
	c.HomeDir = absolutePath(c.HomeDir, "")
	c.ConfigDir = absolutePath(c.ConfigDir, c.HomeDir)
	c.DataDir = absolutePath(c.DataDir, c.HomeDir)
	return c
}

func absolutePath(value, base string) string {
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		if base == "" {
			if cwd, err := os.Getwd(); err == nil {
				base = cwd
			}
		}
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// hasOpaqueExpansion recognizes shell expansion forms that are not safely
// represented by envPattern. Treating these as unresolved is important: a
// default/indirect parameter expansion can otherwise look like an ordinary
// relative filename even though the shell computes a different target.
func hasOpaqueExpansion(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '$' || i+1 >= len(raw) {
			continue
		}
		switch raw[i+1] {
		case '(', '\'', '"', '?', '@', '*', '#', '!', '$':
			return true
		case '{':
			end := strings.IndexByte(raw[i+2:], '}')
			if end < 0 {
				return true
			}
			body := raw[i+2 : i+2+end]
			if body != "HOME" {
				return true
			}
			i += end + 2
		default:
			if raw[i+1] >= '0' && raw[i+1] <= '9' {
				return true
			}
		}
	}
	return false
}

func expandTarget(raw string, ctx Context) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if strings.Contains(raw, "`") || hasOpaqueExpansion(raw) {
		return raw, true
	}
	unresolved := false
	raw = envPattern.ReplaceAllStringFunc(raw, func(match string) string {
		parts := envPattern.FindStringSubmatch(match)
		name := parts[1]
		if name == "" {
			name = parts[2]
		}
		if name == "HOME" && ctx.HomeDir != "" {
			return ctx.HomeDir
		}
		unresolved = true
		return match
	})
	if strings.HasPrefix(raw, "~") {
		if raw == "~" || strings.HasPrefix(raw, "~/") {
			if ctx.HomeDir != "" {
				rest := strings.TrimPrefix(raw, "~")
				rest = strings.TrimPrefix(rest, "/")
				raw = filepath.Join(ctx.HomeDir, rest)
			} else {
				unresolved = true
			}
		} else {
			unresolved = true
		}
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(ctx.WorkingDir, raw)
	}
	return filepath.Clean(raw), unresolved
}

func hasGlob(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

const maxBraceTargets = 64

func braceExpansionStart(value string) int {
	for offset := 0; offset < len(value); {
		index := strings.IndexByte(value[offset:], '{')
		if index < 0 {
			return -1
		}
		index += offset
		if index == 0 || value[index-1] != '$' {
			return index
		}
		offset = index + 1
	}
	return -1
}

func hasBraceExpansionSyntax(value string) bool {
	// Remove the one parameter-expansion form that expandTarget understands;
	// its braces are not shell brace expansion. Opaque ${...} forms remain and
	// are conservatively handled as unresolved syntax.
	value = envPattern.ReplaceAllString(value, "")
	return strings.ContainsAny(value, "{}")
}

// expandBraceTargets resolves comma-style shell brace expansion without
// touching the filesystem. A bounded expansion lets the classifier see that
// /{tmp,etc} includes /etc instead of treating the literal brace text as one
// merely external path. Sequence forms and malformed/explosive expansions are
// deliberately unresolved so the gate fails closed.
func expandBraceTargets(raw string) ([]string, bool) {
	var expand func(string) ([]string, bool)
	expand = func(value string) ([]string, bool) {
		start := braceExpansionStart(value)
		if start < 0 {
			if hasBraceExpansionSyntax(value) {
				return nil, false
			}
			return []string{value}, true
		}
		depth := 0
		end := -1
		for i := start; i < len(value); i++ {
			switch value[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
					i = len(value)
				}
				if depth < 0 {
					return nil, false
				}
			}
		}
		if end < 0 {
			return nil, false
		}

		body := value[start+1 : end]
		var alternatives []string
		partStart, nested := 0, 0
		for i := 0; i <= len(body); i++ {
			if i == len(body) || body[i] == ',' && nested == 0 {
				alternatives = append(alternatives, body[partStart:i])
				partStart = i + 1
				continue
			}
			switch body[i] {
			case '{':
				nested++
			case '}':
				nested--
				if nested < 0 {
					return nil, false
				}
			}
		}
		if nested != 0 || len(alternatives) < 2 {
			return nil, false
		}

		prefix, suffix := value[:start], value[end+1:]
		var out []string
		for _, alternative := range alternatives {
			expanded, ok := expand(prefix + alternative + suffix)
			if !ok || len(out)+len(expanded) > maxBraceTargets {
				return nil, false
			}
			out = append(out, expanded...)
		}
		return out, true
	}
	return expand(raw)
}

func globPrefix(path string) string {
	if idx := strings.IndexAny(path, "*?["); idx >= 0 {
		prefix := path[:idx]
		prefix = strings.TrimRight(prefix, string(filepath.Separator))
		if prefix == "" {
			return string(filepath.Separator)
		}
		return filepath.Clean(prefix)
	}
	return filepath.Clean(path)
}

func within(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func equalOrWithin(root, target string) bool {
	return root != "" && (filepath.Clean(root) == filepath.Clean(target) || within(root, target))
}

func hasPathComponent(target, component string) bool {
	for _, part := range strings.Split(filepath.Clean(target), string(filepath.Separator)) {
		if part == component {
			return true
		}
	}
	return false
}

var catastrophicSystemPrefixes = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/sbin",
	"/sys", "/usr", "/var/lib", "/System", "/Library",
}

var catastrophicSystemExact = []string{
	"/", "/opt", "/root", "/srv", "/var", "/Applications", "/Users", "/home",
}

var catastrophicCredentialNames = []string{".ssh", ".gnupg", ".aws", ".kube", ".docker"}

var catastrophicHomeRoots = []string{
	".config", ".jcode", ".claude", ".local", ".local/share", "Documents", "Desktop",
}

func isTempPath(target string) bool {
	return equalOrWithin("/tmp", target) || equalOrWithin("/var/tmp", target) || equalOrWithin("/private/tmp", target)
}

func isSystemProtected(target string) bool {
	for _, root := range catastrophicSystemPrefixes {
		if equalOrWithin(root, target) {
			return true
		}
	}
	for _, root := range catastrophicSystemExact {
		if filepath.Clean(root) == filepath.Clean(target) {
			return true
		}
	}
	return false
}

func isHomeProtected(target string, ctx Context) bool {
	if ctx.HomeDir == "" || !equalOrWithin(ctx.HomeDir, target) {
		return false
	}
	if filepath.Clean(ctx.HomeDir) == filepath.Clean(target) {
		return true
	}
	rel, err := filepath.Rel(ctx.HomeDir, target)
	if err != nil {
		return false
	}
	first := strings.Split(rel, string(filepath.Separator))[0]
	for _, name := range catastrophicCredentialNames {
		if first == name {
			return true
		}
	}
	for _, name := range catastrophicHomeRoots {
		if filepath.Clean(target) == filepath.Join(ctx.HomeDir, name) {
			return true
		}
	}
	return false
}

func isDevice(target string) bool {
	if !equalOrWithin("/dev", target) {
		return false
	}
	for _, allowed := range []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/fd"} {
		if equalOrWithin(allowed, target) {
			return false
		}
	}
	return true
}

func isSafeDevice(target string) bool {
	if !equalOrWithin("/dev", target) {
		return false
	}
	for _, allowed := range []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/fd"} {
		if equalOrWithin(allowed, target) {
			return true
		}
	}
	return false
}

func protectedProjectPath(target string) bool {
	return hasPathComponent(target, ".git")
}

func classifyTarget(raw string, recursive bool, ctx Context) Finding {
	ctx = ctx.normalized()
	if raw == "" || raw == "-" {
		return Finding{}
	}
	if hasBraceExpansionSyntax(raw) {
		targets, ok := expandBraceTargets(raw)
		if !ok {
			return Finding{Level: Confirm, Reason: "target contains an unresolved shell brace expansion", Target: raw}
		}
		var strongest Finding
		for _, target := range targets {
			finding := classifyTarget(target, recursive, ctx)
			if finding.Level > strongest.Level || strongest.Reason == "" {
				strongest = finding
			}
		}
		return strongest
	}
	target, unresolved := expandTarget(raw, ctx)
	if unresolved {
		return Finding{Level: Confirm, Reason: "target contains an unresolved variable or command substitution", Target: raw}
	}
	probe := target
	if hasGlob(probe) {
		probe = globPrefix(probe)
		if isSafeDevice(probe) || isTempPath(probe) {
			return Finding{Level: Confirm, Reason: "target contains a glob, so the exact set of affected files is unknown", Target: raw}
		}
		if isDevice(probe) || isSystemProtected(probe) || isHomeProtected(probe, ctx) {
			return Finding{Level: Catastrophic, Reason: "glob reaches a protected system, device, or credential path", Target: raw}
		}
		if protectedProjectPath(probe) {
			return Finding{Level: Confirm, Reason: "glob reaches a repository metadata directory", Target: raw}
		}
		return Finding{Level: Confirm, Reason: "target contains a glob, so the exact set of affected files is unknown", Target: raw}
	}
	if isSafeDevice(probe) {
		return Finding{}
	}
	if isDevice(probe) || isSystemProtected(probe) || isHomeProtected(probe, ctx) {
		return Finding{Level: Catastrophic, Reason: "target reaches a protected system, device, or credential path", Target: target}
	}
	if protectedProjectPath(probe) {
		return Finding{Level: Confirm, Reason: "target reaches a repository metadata directory", Target: target}
	}
	if recursive && filepath.Clean(probe) == filepath.Clean(ctx.WorkspaceRoot) {
		return Finding{Level: Confirm, Reason: "recursive target is the workspace root and includes repository metadata", Target: target}
	}
	if equalOrWithin(ctx.ConfigDir, probe) || equalOrWithin(ctx.DataDir, probe) {
		return Finding{Level: Confirm, Reason: "target reaches an application configuration or data directory", Target: target}
	}
	if isTempPath(probe) {
		return Finding{}
	}
	if !within(ctx.WorkspaceRoot, probe) {
		return Finding{Level: Confirm, Reason: "target is outside the active workspace", Target: target}
	}
	if recursive {
		return Finding{Level: Low, Reason: "recursive target is confined to the active workspace", Target: target}
	}
	return Finding{}
}
