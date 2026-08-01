package tui

import "testing"

func TestHighlightLinesIsCached(t *testing.T) {
	// The side panel re-renders its preview every frame. Lexing was cached but
	// styling was not, so a read preview of a long file cost 30ms a frame.
	src := "package main\n\nfunc main() { println(1) }\n"
	first := HighlightLines("go", src)
	second := HighlightLines("go", src)
	if len(first) == 0 {
		t.Fatal("no highlighted lines")
	}
	if &first[0] != &second[0] {
		t.Error("a repeated highlight re-styled the source instead of reusing it")
	}
}
