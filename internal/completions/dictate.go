package completions

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"evilcode/internal/config"
)

// DictateEnv names the speech-to-text command. It is an environment variable
// and a config key rather than a bundled engine: STT setups are personal — a
// local whisper.cpp, a cloud key, a wrapper script — and evilcode has no
// business having an opinion about which.
const DictateEnv = "EVILCODE_DICTATE"

// DefaultDictateCommand is what runs when nothing is configured. It is a guess
// at the most common local setup, and its absence is reported as a suggestion
// rather than an error about a missing feature.
var DefaultDictateCommand = []string{"whisper-cli", "--no-timestamps", "-"}

// DictateCommand resolves the configured command.
func DictateCommand() []string {
	if env := strings.TrimSpace(os.Getenv(DictateEnv)); env != "" {
		return strings.Fields(env)
	}
	if cfg, err := config.Load(); err == nil && len(cfg.Dictate) > 0 {
		return cfg.Dictate
	}
	return DefaultDictateCommand
}

// RunDictate runs the speech-to-text command and prints what it heard.
//
// The transcript goes to stdout so it composes: `evilcode run "$(evilcode
// dictate)"` works, and so does binding the whole thing to a key. The TUI
// consumes it the same way, which is why this is a subcommand rather than
// something buried in the composer.
func RunDictate(args []string) error {
	command := DictateCommand()
	if len(args) > 0 {
		command = args
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return fmt.Errorf(
			"%s is not installed. Set %s to your speech-to-text command — "+
				"it should read audio and print the transcript, e.g. %s='whisper-cli -f - '",
			command[0], DictateEnv, DictateEnv)
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", command[0], err)
	}

	text := strings.TrimSpace(out.String())
	if text == "" {
		return fmt.Errorf("%s heard nothing", command[0])
	}
	fmt.Println(text)
	return nil
}
