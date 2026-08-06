package commandrisk

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RiskLevel is ordered from an immediately runnable command to a command that
// must never be confirmed interactively.
type RiskLevel uint8

const (
	Safe RiskLevel = iota
	Low
	Confirm
	Catastrophic
)

func (r RiskLevel) String() string {
	switch r {
	case Safe:
		return "safe"
	case Low:
		return "low"
	case Confirm:
		return "confirm"
	case Catastrophic:
		return "catastrophic"
	default:
		return "unknown"
	}
}

// Finding explains one reason the command was elevated. Target is retained
// for a useful reflection prompt and audit transcript.
type Finding struct {
	Level  RiskLevel
	Reason string
	Target string
}

type Assessment struct {
	Level    RiskLevel
	Findings []Finding
}

func (a Assessment) Explanation() string {
	if len(a.Findings) == 0 {
		return "no destructive target detected"
	}
	parts := make([]string, 0, len(a.Findings))
	seen := map[string]bool{}
	for _, finding := range a.Findings {
		part := finding.Reason
		if finding.Target != "" {
			part = fmt.Sprintf("%s (%s)", part, finding.Target)
		}
		if !seen[part] {
			parts = append(parts, part)
			seen[part] = true
		}
	}
	return strings.Join(parts, "; ")
}

func (a *Assessment) add(finding Finding) {
	if finding.Reason == "" {
		return
	}
	a.Findings = append(a.Findings, finding)
	if finding.Level > a.Level {
		a.Level = finding.Level
	}
}

func (a *Assessment) merge(other Assessment) {
	if other.Level > a.Level {
		a.Level = other.Level
	}
	a.Findings = append(a.Findings, other.Findings...)
}

func isFlag(text string) bool {
	return len(text) > 1 && strings.HasPrefix(text, "-")
}

func isRecursiveFlag(text string) bool {
	if text == "--recursive" {
		return true
	}
	if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
		return false
	}
	return strings.ContainsAny(strings.TrimPrefix(text, "-"), "rR")
}

func commandName(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexByte(text, '='); idx > 0 && !strings.HasPrefix(text, "./") && !strings.HasPrefix(text, "/") {
		// Environment assignments can precede a command.
		return ""
	}
	return filepath.Base(text)
}

var wrapperCommands = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nice": true, "ionice": true,
	"time": true, "timeout": true, "nohup": true, "xargs": true,
	"command": true, "builtin": true, "exec": true, "setsid": true,
	"stdbuf": true, "chroot": true, "su": true, "watch": true,
	"eval": true,
}

var shellCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
}

var directDestructive = map[string]bool{
	"rm": true, "rmdir": true, "shred": true, "unlink": true, "truncate": true,
	"srm": true, "mkfs": true, "mkfs.ext4": true, "mkfs.xfs": true,
	"fdisk": true, "parted": true, "wipefs": true,
}

func appendTarget(a *Assessment, raw string, recursive bool, ctx Context) {
	if finding := classifyTarget(raw, recursive, ctx); finding.Reason != "" {
		a.add(finding)
	}
}

func targetArgs(args []Token, skipFirst int) (targets []string, recursive bool) {
	for i := skipFirst; i < len(args); i++ {
		text := args[i].Text
		if text == "--" {
			continue
		}
		if isRecursiveFlag(text) {
			recursive = true
		}
		if isFlag(text) {
			continue
		}
		targets = append(targets, text)
	}
	return targets, recursive
}

func wrapperOptionTakesValue(name, option string) bool {
	option = strings.SplitN(option, "=", 2)[0]
	switch name {
	case "sudo", "doas", "su":
		return option == "-u" || option == "--user" || option == "-s" || option == "--shell" || option == "-c"
	case "env":
		return option == "-u" || option == "--unset" || option == "-S" || option == "--split-string"
	case "nice":
		return option == "-n" || option == "--adjustment"
	case "ionice":
		return option == "-c" || option == "-n" || option == "-p"
	case "timeout":
		return option == "--signal" || option == "--kill-after" || option == "-s" || option == "-k"
	case "stdbuf":
		return option == "-i" || option == "-o" || option == "-e"
	case "xargs":
		return option == "-n" || option == "--max-args" || option == "-s" || option == "--max-chars" || option == "-P" || option == "--max-procs"
	default:
		return false
	}
}

func assessSegment(segment []Token, ctx Context) Assessment {
	var assessment Assessment
	if len(segment) == 0 {
		return assessment
	}
	for _, tok := range segment {
		if tok.Malformed {
			assessment.add(Finding{Level: Confirm, Reason: "command syntax could not be parsed completely"})
		}
		if tok.IsTruncatingRedirectTarget {
			appendTarget(&assessment, tok.Text, false, ctx)
		}
	}

	// Strip assignments and transparent wrappers until the command itself is
	// visible. Shell -c wrappers are recursively assessed as their own script.
	idx := 0
	sawAssignment := false
	for idx < len(segment) {
		name := commandName(segment[idx].Text)
		if name == "" || strings.Contains(segment[idx].Text, "=") && !strings.HasPrefix(segment[idx].Text, "./") {
			sawAssignment = true
			idx++
			continue
		}
		if shellCommands[name] {
			for j := idx + 1; j < len(segment); j++ {
				if segment[j].Text == "-c" || segment[j].Text == "--command" {
					if j+1 >= len(segment) {
						assessment.add(Finding{Level: Confirm, Reason: "shell wrapper has no complete script"})
						return assessment
					}
					if shellScriptHasUnknownExpansion(segment[j+1].Text) {
						assessment.add(Finding{Level: Confirm, Reason: "shell wrapper contains an unresolved runtime expansion"})
						return assessment
					}
					assessment.merge(Assess(segment[j+1].Text, ctx))
					return assessment
				}
			}
			assessment.add(Finding{Level: Confirm, Reason: "shell wrapper obscures the command being run"})
			return assessment
		}
		if wrapperCommands[name] {
			idx++
			if name == "command" && idx < len(segment) && (segment[idx].Text == "-v" || segment[idx].Text == "-V" || segment[idx].Text == "--verbose") {
				return assessment
			}
			if name == "eval" {
				if idx >= len(segment) {
					assessment.add(Finding{Level: Confirm, Reason: "eval wrapper has no complete script"})
					return assessment
				}
				if shellScriptHasUnknownExpansion(segment[idx].Text) {
					assessment.add(Finding{Level: Confirm, Reason: "eval wrapper contains an unresolved runtime expansion"})
					return assessment
				}
				assessment.merge(Assess(segment[idx].Text, ctx))
				return assessment
			}
			// These wrappers have one required positional argument before the
			// wrapped command (duration, chroot directory, or login user).
			requiredArgumentSkipped := false
			if name == "timeout" || name == "chroot" || name == "su" {
				if idx < len(segment) && !isFlag(segment[idx].Text) {
					idx++
					requiredArgumentSkipped = true
				}
			}
			// Most wrappers have optional flags. Skip them, and let the next
			// non-option word become the wrapped command. env also accepts
			// NAME=value assignments before that command.
			for idx < len(segment) {
				text := segment[idx].Text
				if (name == "timeout" || name == "chroot" || name == "su") && !requiredArgumentSkipped && !isFlag(text) {
					idx++
					requiredArgumentSkipped = true
					continue
				}
				if isFlag(text) {
					if (text == "-c" || text == "--command") && idx+1 < len(segment) && (name == "su" || name == "watch") {
						assessment.merge(Assess(segment[idx+1].Text, ctx))
						return assessment
					}
					consumes := wrapperOptionTakesValue(name, text) && !strings.Contains(text, "=")
					idx++
					if consumes && idx < len(segment) {
						idx++
					}
					continue
				}
				if name == "env" && strings.Contains(text, "=") {
					idx++
					continue
				}
				break
			}
			continue
		}
		break
	}
	if idx >= len(segment) {
		if sawAssignment {
			return assessment
		}
		if len(segment) > 0 {
			assessment.add(Finding{Level: Confirm, Reason: "wrapper does not reveal a complete command"})
		}
		return assessment
	}

	name := commandName(segment[idx].Text)
	args := segment[idx+1:]
	pipeFed := segment[idx].ReceivesPipe
	if name == "!" {
		nested := assessSegment(args, ctx)
		assessment.merge(nested)
		if len(nested.Findings) == 0 {
			assessment.add(Finding{Level: Confirm, Reason: "shell negation obscures the command being run"})
		}
		return assessment
	}
	if name == "xargs" {
		assessment.add(Finding{Level: Confirm, Reason: "pipe-fed command wrapper may invoke destructive arguments"})
		return assessment
	}

	if name == "git" {
		clean := -1
		for i, arg := range args {
			if arg.Text == "clean" {
				clean = i
				break
			}
		}
		if clean >= 0 {
			dryRun := false
			var targets []string
			for _, arg := range args[clean+1:] {
				if arg.Text == "-n" || arg.Text == "--dry-run" {
					dryRun = true
					continue
				}
				if isFlag(arg.Text) {
					continue
				}
				targets = append(targets, arg.Text)
			}
			if !dryRun {
				if len(targets) == 0 {
					assessment.add(Finding{Level: Confirm, Reason: "git clean can remove untracked workspace data"})
				} else {
					for _, target := range targets {
						appendTarget(&assessment, target, true, ctx)
					}
				}
			}
		}
	}

	if directDestructive[name] {
		targets, recursive := targetArgs(args, 0)
		// chmod/chown are handled below; the direct set contains only commands
		// whose non-option arguments are paths.
		for _, target := range targets {
			appendTarget(&assessment, target, recursive, ctx)
		}
		if len(targets) == 0 {
			assessment.add(Finding{Level: Confirm, Reason: "destructive command has no explicit target"})
		}
		if pipeFed {
			assessment.add(Finding{Level: Confirm, Reason: "destructive command receives data from a pipe"})
		}
		return assessment
	}

	if name == "dd" {
		foundTarget := false
		for _, arg := range args {
			for _, key := range []string{"of", "if", "seek"} {
				prefix := key + "="
				if strings.HasPrefix(arg.Text, prefix) {
					foundTarget = true
					appendTarget(&assessment, strings.TrimPrefix(arg.Text, prefix), true, ctx)
				}
			}
		}
		if !foundTarget {
			assessment.add(Finding{Level: Confirm, Reason: "dd has no explicit input or output target"})
		}
		if pipeFed {
			assessment.add(Finding{Level: Confirm, Reason: "destructive command receives data from a pipe"})
		}
		return assessment
	}

	if name == "find" {
		deleteAction := false
		actionIndex := -1
		var paths []string
		for i := 0; i < len(args); i++ {
			text := args[i].Text
			if text == "-delete" || text == "-exec" || text == "-execdir" {
				deleteAction = true
				actionIndex = i
				break
			}
			if isFlag(text) {
				// Common find options consume one following value.
				if text == "-maxdepth" || text == "-mindepth" || text == "-name" || text == "-path" || text == "-type" {
					i++
				}
				continue
			}
			paths = append(paths, text)
		}
		if deleteAction {
			if len(paths) == 0 {
				paths = []string{"."}
			}
			for _, target := range paths {
				appendTarget(&assessment, target, true, ctx)
			}
			if args[actionIndex].Text == "-exec" || args[actionIndex].Text == "-execdir" {
				end := actionIndex + 1
				for end < len(args) && args[end].Text != ";" {
					end++
				}
				if end == actionIndex+1 {
					assessment.add(Finding{Level: Confirm, Reason: "find execution action has no complete command"})
				} else {
					assessment.merge(assessSegment(args[actionIndex+1:end], ctx))
				}
			}
		}
	}

	if name == "chmod" || name == "chown" {
		targets, recursive := targetArgs(args, 0)
		if len(targets) > 0 {
			// The first non-option argument is a mode/owner, not a path.
			targets = targets[1:]
		}
		for _, target := range targets {
			appendTarget(&assessment, target, recursive, ctx)
		}
		if len(targets) == 0 {
			assessment.add(Finding{Level: Confirm, Reason: "permission-changing command has no explicit target"})
		}
		if pipeFed {
			assessment.add(Finding{Level: Confirm, Reason: "destructive command receives data from a pipe"})
		}
	}
	return assessment
}

func shellScriptHasUnknownExpansion(script string) bool {
	if strings.Contains(script, "`") {
		return true
	}
	for _, match := range envPattern.FindAllStringSubmatch(script, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if name != "HOME" {
			return true
		}
	}
	return false
}

func nestedCommands(command string) (commands []string, malformed bool) {
	for i := 0; i < len(command); i++ {
		if command[i] == '`' {
			start := i + 1
			end := start
			for end < len(command) && command[end] != '`' {
				if command[end] == '\\' && end+1 < len(command) {
					end++
				}
				end++
			}
			if end >= len(command) {
				return commands, true
			}
			commands = append(commands, command[start:end])
			i = end
			continue
		}
		if command[i] != '$' || i+1 >= len(command) || command[i+1] != '(' {
			continue
		}
		start := i + 2
		depth := 1
		quote := byte(0)
		end := start
		for end < len(command) {
			c := command[end]
			if quote != 0 {
				if c == '\\' && end+1 < len(command) {
					end += 2
					continue
				}
				if c == quote {
					quote = 0
				}
				end++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
			} else if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
			end++
		}
		if depth != 0 {
			return commands, true
		}
		commands = append(commands, command[start:end])
		i = end
	}
	return commands, malformed
}

// Assess performs a conservative, lexical risk assessment. It never touches
// the filesystem and it treats malformed/opaque shell syntax as confirmation-
// required rather than guessing that the command is harmless.
func Assess(command string, ctx Context) Assessment {
	var assessment Assessment
	ctx = ctx.normalized()
	for _, segment := range SplitSegments(Tokenize(command)) {
		assessment.merge(assessSegment(segment, ctx))
	}
	if nested, malformed := nestedCommands(command); malformed {
		assessment.add(Finding{Level: Confirm, Reason: "command substitution could not be parsed completely"})
	} else {
		for _, script := range nested {
			assessment.merge(Assess(script, ctx))
		}
	}
	return assessment
}
