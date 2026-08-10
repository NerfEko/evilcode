// Package resumecmd imports a foreign transcript and enters the normal TUI
// resume path. The import is deliberately a one-time conversion: after it is
// written, the session is an ordinary evilcode JSONL session.
package resumecmd

import (
	"flag"
	"fmt"
	"strings"

	"evilcode/internal/config"
	"evilcode/internal/session"
	"evilcode/internal/tuicmd"
)

// Run implements `evilcode resume --from claude <id>`.
func Run(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	from := fs.String("from", "", "external session source: claude, codex, or opencode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*from) == "" {
		return fmt.Errorf("resume requires --from claude, codex, or opencode")
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return fmt.Errorf("usage: evilcode resume --from claude <session-id>")
	}

	info, err := session.ImportExternal(config.DataDir(), session.ExternalSource(*from), fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("imported %s session %s as %s; resuming\n", info.Source, info.SourceID, info.Name)
	return tuicmd.Run([]string{"-resume", info.Name})
}

// Usage prints the subcommand's syntax.
func Usage() string {
	return "evilcode resume --from claude|codex|opencode <session-id-or-path>"
}
