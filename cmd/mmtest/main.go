package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"evilcode/internal/graphics"
)

func main() {
	// Force the protocol the way a kitty terminal would be detected.
	os.Setenv("EVILCODE_GRAPHICS", "kitty")
	proto := graphics.Detect()
	fmt.Println("detected:", proto)
	if proto != graphics.ProtoKitty {
		os.Exit(1)
	}

	src := "graph TD\n  A[plan.md] --> B[implement]\n  B --> C{tests pass?}\n  C -->|yes| D[look at the PNG]\n  C -->|no| B\n  D --> E[commit]\n"
	png, err := graphics.RenderMermaid(context.Background(), src)
	if err != nil {
		fmt.Println("ERR:", err)
		os.Exit(1)
	}

	seq := graphics.KittySequence(graphics.Image{PNG: png, Cols: 40, Rows: 20, ID: 7})
	fmt.Println("sequence bytes:", len(seq))

	// The contract: it is a real kitty transmission, it carries the PNG in
	// chunks, and it occupies no printable cells.
	checks := map[string]bool{
		"starts a graphics command": strings.Contains(seq, "\x1b_G"),
		"declares PNG format (f=100)": strings.Contains(seq, "f=100"),
		"places into a 40x20 cell box": strings.Contains(seq, "c=40") && strings.Contains(seq, "r=20"),
		"carries an id":               strings.Contains(seq, "i=7"),
		"is chunked":                  strings.Count(seq, "\x1b_G") > 1,
		"terminates every chunk":      strings.Count(seq, "\x1b\\") == strings.Count(seq, "\x1b_G"),
		"last chunk says m=0":         strings.Contains(seq, "m=0"),
	}
	bad := 0
	for name, ok := range checks {
		if !ok {
			fmt.Println("FAIL:", name)
			bad++
		}
	}
	printable := strings.Map(func(r rune) rune {
		if r == 0x1b || r == '\\' {
			return -1
		}
		return r
	}, seq)
	_ = printable

	del := graphics.DeleteSequence(7)
	if !strings.Contains(del, "a=d") || !strings.Contains(del, "i=7") {
		fmt.Println("FAIL: delete sequence")
		bad++
	}
	if bad > 0 {
		os.Exit(1)
	}
	fmt.Println("kitty transmission: all checks pass")
	fmt.Printf("first chunk: %q\n", seq[:min(70, len(seq))])
}
