// Package completions generates shell completion scripts and answers the
// dynamic half of completion (plan.md Phase 5).
package completions

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"evilcode/internal/config"
	"evilcode/internal/session"
)

// Subcommands is the completion set for the first argument.
var Subcommands = []string{
	"tui", "run", "serve", "attach", "resume", "update", "probe", "dictate", "completions", "help",
}

// Flags maps a subcommand to the flags it accepts. Hand-maintained rather than
// reflected out of the flag sets, because reflecting them means constructing
// every subcommand's flags at completion time — which is startup work for a
// shell pressing Tab.
var Flags = map[string][]string{
	"tui":    {"-m", "-resume"},
	"run":    {"-m", "-q", "-resume", "-no-tools", "-remote", "-socket"},
	"serve":  {"-m", "-q", "-socket"},
	"attach": {"-l", "-socket"},
	"resume": {"-from"},
	"probe":  {"-size"},
}

// ProbeCommands is the second-level set under `probe`.
var ProbeCommands = []string{"render", "text", "fonts", "hello", "help"}

// Shells lists what `evilcode completions` can emit.
var Shells = []string{"bash", "zsh", "fish"}

// WriteCompletions emits a completion script for a shell.
//
// The scripts call back into `evilcode __complete` for anything dynamic —
// models and session names — rather than baking a list in. A completion script
// that lists yesterday's sessions is worse than one that lists none.
func WriteCompletions(w io.Writer, shell string) error {
	switch shell {
	case "bash":
		fmt.Fprint(w, bashScript)
	case "zsh":
		fmt.Fprint(w, zshScript)
	case "fish":
		fmt.Fprint(w, fishScript)
	default:
		return fmt.Errorf("unknown shell %q (want %s)", shell, strings.Join(Shells, ", "))
	}
	return nil
}

// Complete answers the dynamic half: what can follow the words typed so far.
//
// It is deliberately forgiving. A completion helper that errors leaves the user
// staring at a shell that beeps, so an unknown context simply completes nothing.
func Complete(args []string) []string {
	if len(args) == 0 {
		return Subcommands
	}

	sub := args[0]
	prev := ""
	if len(args) > 1 {
		prev = args[len(args)-1]
	}

	switch prev {
	case "-m":
		return modelRefs()
	case "-resume":
		return sessionNames()
	case "-socket":
		return nil // a path; the shell's own file completion is better
	}

	if sub == "completions" {
		return Shells
	}
	if sub == "probe" {
		return append(append([]string{}, ProbeCommands...), Flags[sub]...)
	}
	// Only flags once a subcommand has been chosen. Offering the subcommand
	// list again after `evilcode run ` suggests `evilcode run tui`, which is
	// not a thing.
	return Flags[sub]
}

// modelRefs lists `model@provider` references from the configuration. It never
// asks a provider what it hosts: pressing Tab must not make a network call.
func modelRefs() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	cfg.AddDiscoveredCodex()
	var out []string
	if cfg.DefaultModel != "" {
		out = append(out, cfg.DefaultModel)
	}
	for _, m := range cfg.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func sessionNames() []string {
	infos, err := session.List(config.DataDir())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// RunComplete is the `__complete` subcommand the scripts invoke.
func RunComplete(args []string) error {
	for _, c := range Complete(args) {
		fmt.Fprintln(os.Stdout, c)
	}
	return nil
}

// RunCompletions is the `completions <shell>` subcommand.
func RunCompletions(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: evilcode completions <%s>", strings.Join(Shells, "|"))
	}
	return WriteCompletions(os.Stdout, args[0])
}

const bashScript = `# evilcode bash completion
#   eval "$(evilcode completions bash)"
_evilcode() {
    local cur words
    cur="${COMP_WORDS[COMP_CWORD]}"
    words=("${COMP_WORDS[@]:1:COMP_CWORD-1}")
    COMPREPLY=($(compgen -W "$(evilcode __complete "${words[@]}" 2>/dev/null)" -- "$cur"))
}
complete -o default -F _evilcode evilcode
complete -o default -F _evilcode ec
`

const zshScript = `# evilcode zsh completion
#   eval "$(evilcode completions zsh)"
_evilcode() {
    local -a candidates
    # words[2,CURRENT-1] is everything after the command and before the cursor.
    candidates=(${(f)"$(evilcode __complete ${words[2,CURRENT-1]} 2>/dev/null)"})
    _describe 'evilcode' candidates && return
    _files
}
compdef _evilcode evilcode
compdef _evilcode ec
`

const fishScript = `# evilcode fish completion
#   evilcode completions fish > ~/.config/fish/completions/evilcode.fish
function __evilcode_complete
    set -l tokens (commandline -opc)
    evilcode __complete $tokens[2..-1] 2>/dev/null
end
complete -c evilcode -f -a '(__evilcode_complete)'
complete -c ec -f -a '(__evilcode_complete)'
`
