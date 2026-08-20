// Package probecmd implements the `evilcode probe` subcommand: the parts of the
// self-test rig (plan.md §14) that live inside the binary, so probe.sh only has
// to drive tmux and shell out here.
package probecmd

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"evilcode/internal/ansirender"
)

const usage = `evilcode probe — self-test rig

usage: evilcode probe <command> [args]

commands:
  render <in.ansi> <out.png>   render captured ANSI output to a PNG
  text   <in.ansi>             print the parsed frame as plain text
  fonts                        report which design glyphs the fonts can draw
  hello                        boot a minimal bubbletea app (rig smoke test)
`

// Run dispatches a probe subcommand.
func Run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "render":
		return runRender(args[1:])
	case "text":
		return runText(args[1:])
	case "fonts":
		return runFonts()
	case "hello":
		return runHello(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown probe command %q", args[0])
	}
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("probe render", flag.ContinueOnError)
	size := fs.Float64("size", ansirender.DefaultFontSize,
		"font em size in pixels; raise it to read fine detail by eye")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: evilcode probe render [-size N] <in.ansi> <out.png>")
	}
	src, dst := fs.Arg(0), fs.Arg(1)
	if src == "-" {
		in, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		dir := filepath.Dir(dst)
		out, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".*")
		if err != nil {
			return err
		}
		tmp := out.Name()
		defer os.Remove(tmp)
		if err := ansirender.WritePNGSize(out, string(in), *size); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	}
	return ansirender.RenderFileSize(src, dst, *size)
}

// runFonts reports the loaded faces and every design glyph none of them can
// draw. A missing glyph renders as a tofu box in probe PNGs, so this is the
// answer to "why is that a square".
func runFonts() error {
	fmt.Println("fonts loaded (primary first):")
	for _, f := range ansirender.LoadedFonts() {
		fmt.Println("  " + f)
	}
	fmt.Println()

	total, missing := 0, 0
	for _, group := range ansirender.GlyphVocabulary {
		var gone []string
		for _, r := range group.Glyphs {
			total++
			if !ansirender.Resolve(r) {
				gone = append(gone, string(r))
				missing++
			}
		}
		status := "ok"
		if len(gone) > 0 {
			status = "missing: " + strings.Join(gone, " ")
		}
		fmt.Printf("  %-10s %2d glyphs  %s\n", group.Name, len(group.Glyphs), status)
	}

	fmt.Printf("\n%d/%d glyphs drawable\n", total-missing, total)
	if missing > 0 {
		fmt.Printf("\nMissing glyphs are almost always color emoji: their artwork lives in\n"+
			"COLR/CBDT tables that the renderer cannot rasterize. Install a monochrome\n"+
			"emoji font (Noto Emoji) or point %s at one:\n"+
			"  %s=/path/to/NotoEmoji-Regular.ttf evilcode probe fonts\n",
			ansirender.FallbackFontsEnv, ansirender.FallbackFontsEnv)
	}
	return nil
}

func runText(args []string) error {
	fs := flag.NewFlagSet("probe text", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: evilcode probe text <in.ansi>")
	}
	var in []byte
	var err error
	if fs.Arg(0) == "-" {
		in, err = io.ReadAll(os.Stdin)
	} else {
		in, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return err
	}
	text := ansirender.Parse(string(in)).Text()
	_, err = fmt.Println(strings.TrimRight(text, "\n"))
	return err
}
