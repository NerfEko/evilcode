// Command evilcode is a personal AI coding agent harness.
//
// Subcommand dispatch is a plain switch over os.Args — stdlib flag, no cobra
// (plan.md §1.5).
package main

import (
	"fmt"
	"os"

	"evilcode/internal/attachcmd"
	"evilcode/internal/completions"
	"evilcode/internal/probecmd"
	"evilcode/internal/resumecmd"
	"evilcode/internal/runcmd"
	"evilcode/internal/servecmd"
	"evilcode/internal/tuicmd"
)

const usage = `evilcode — personal AI coding agent harness

usage: evilcode [subcommand] [flags]

subcommands:
  tui        interactive terminal UI (default when no subcommand is given)
  run        headless one-shot: evilcode run "prompt"
  serve      background daemon hosting sessions
  attach     attach a TUI to a running daemon session
  resume     import and resume a Claude, Codex, or OpenCode session
  update     fetch and atomically install the latest release binary
  completions print a shell completion script: bash | zsh | fish
  probe      self-test rig; see 'evilcode probe -h'
  dictate    speech-to-text into the composer

Run 'evilcode <subcommand> -h' for subcommand flags.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "evilcode:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	sub := "tui"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}

	switch sub {
	case "probe":
		return probecmd.Run(args)
	case "run":
		code, err := runcmd.Run(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "evilcode:", err)
		}
		os.Exit(code)
		return nil
	case "resume":
		return resumecmd.Run(args)
	case "tui":
		return tuicmd.Run(args)
	case "serve":
		return servecmd.Run(args)
	case "attach":
		return attachcmd.Run(args)
	case "update":
		return runUpdate()
	case "completions":
		return completions.RunCompletions(args)
	case "__complete":
		// Hidden: the completion scripts call it, people do not. Listing it in
		// the usage would put plumbing in front of every reader.
		return completions.RunComplete(args)
	case "dictate":
		return completions.RunDictate(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}
