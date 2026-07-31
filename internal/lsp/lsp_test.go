package lsp

import (
	"strings"
	"testing"
)

func TestApplyEditsAppliesLastFirst(t *testing.T) {
	// The protocol gives every edit against the *original* document, so
	// applying them in order would shift every position after the first.
	text := "alpha beta\ngamma delta\n"
	edits := []TextEdit{
		{Range: Range{Position{0, 0}, Position{0, 5}}, NewText: "ALPHA"},
		{Range: Range{Position{1, 6}, Position{1, 11}}, NewText: "DELTA"},
	}
	got, err := ApplyEdits(text, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ALPHA beta\ngamma DELTA\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplyEditsHandlesLengthChanges(t *testing.T) {
	// A rename usually changes the identifier's length, which is exactly when
	// order-of-application bugs show up.
	text := "x := old\ny := old\n"
	edits := []TextEdit{
		{Range: Range{Position{0, 5}, Position{0, 8}}, NewText: "muchLongerName"},
		{Range: Range{Position{1, 5}, Position{1, 8}}, NewText: "muchLongerName"},
	}
	got, err := ApplyEdits(text, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "x := muchLongerName\ny := muchLongerName\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplyEditsSpanningLines(t *testing.T) {
	text := "one\ntwo\nthree\n"
	edits := []TextEdit{{Range: Range{Position{0, 0}, Position{2, 0}}, NewText: "X\n"}}
	got, err := ApplyEdits(text, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "X\nthree\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplyEditsRefusesAnOutOfRangeEdit(t *testing.T) {
	// Refusing is what makes rename atomic: the caller computes every file
	// before writing any, so a bad edit stops the whole rename rather than
	// leaving a workspace half-renamed.
	if _, err := ApplyEdits("short\n", []TextEdit{
		{Range: Range{Position{9, 0}, Position{9, 1}}, NewText: "x"},
	}); err == nil {
		t.Error("an edit past the end of the file was accepted")
	}
	if _, err := ApplyEdits("short\n", []TextEdit{
		{Range: Range{Position{0, 0}, Position{0, 99}}, NewText: "x"},
	}); err == nil {
		t.Error("an edit past the end of a line was accepted")
	}
}

func TestURIRoundTrip(t *testing.T) {
	for _, path := range []string{"/tmp/a.go", "/tmp/with space/b.go", "/tmp/hash#1.go"} {
		if got := PathFromURI(URIFromPath(path)); got != path {
			t.Errorf("round trip of %q gave %q", path, got)
		}
	}
}

func TestWorkspaceEditReadsBothEncodings(t *testing.T) {
	// Servers pick one shape or the other; reading only one silently renames
	// nothing against half the ecosystem.
	w := WorkspaceEdit{
		Changes: map[string][]TextEdit{
			"file:///a.go": {{NewText: "x"}},
		},
	}
	w.DocumentChanges = append(w.DocumentChanges, struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Edits []TextEdit `json:"edits"`
	}{})
	w.DocumentChanges[0].TextDocument.URI = "file:///b.go"
	w.DocumentChanges[0].Edits = []TextEdit{{NewText: "y"}}

	got := w.Edits()
	if len(got) != 2 || len(got["/a.go"]) != 1 || len(got["/b.go"]) != 1 {
		t.Errorf("edits = %v", got)
	}
}

func TestLanguageIDCoversTheConfiguredServers(t *testing.T) {
	// Every language with a default command must be reachable from a filename,
	// or the server is configured and can never start.
	seen := map[string]bool{}
	for _, path := range []string{"a.go", "a.ts", "a.js", "a.py", "a.rs"} {
		seen[LanguageID(path)] = true
	}
	for lang := range DefaultCommands() {
		if !seen[lang] {
			t.Errorf("no filename maps to the configured language %q", lang)
		}
	}
}

func TestDecodeMarkupHandlesEveryHoverShape(t *testing.T) {
	cases := map[string]string{
		`{"kind":"markdown","value":"func F()"}`: "func F()",
		`"plain string"`:                         "plain string",
		`[{"value":"a"},{"value":"b"}]`:          "a\n\nb",
	}
	for in, want := range cases {
		if got := decodeMarkup([]byte(in)); got != want {
			t.Errorf("decodeMarkup(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeLocationsHandlesEveryShape(t *testing.T) {
	single := decodeLocations([]byte(`{"uri":"file:///a.go","range":{"start":{"line":1}}}`))
	if len(single) != 1 {
		t.Errorf("single location decoded to %d", len(single))
	}
	many := decodeLocations([]byte(`[{"uri":"file:///a.go"},{"uri":"file:///b.go"}]`))
	if len(many) != 2 {
		t.Errorf("array decoded to %d", len(many))
	}
	links := decodeLocations([]byte(`[{"targetUri":"file:///a.go","targetSelectionRange":{}}]`))
	if len(links) != 1 || !strings.HasSuffix(links[0].URI, "a.go") {
		t.Errorf("location links decoded to %v", links)
	}
	if got := decodeLocations([]byte("null")); got != nil {
		t.Errorf("null decoded to %v", got)
	}
}

func TestManagerReportsAMissingServer(t *testing.T) {
	// Not installed is the usual state, and it has to be a clear message rather
	// than a hang or a panic.
	m := NewManager(t.TempDir(), map[string][]string{"go": {"definitely-not-a-real-server"}})
	defer m.Close()

	for _, s := range m.Status() {
		if s.Language == "go" && !strings.Contains(s.Err, "not installed") {
			t.Errorf("status for a missing server = %q", s.Err)
		}
	}
}

func TestManagerRefusesAnUnconfiguredLanguage(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	defer m.Close()
	if _, err := m.For(t.Context(), "notes.txt"); err == nil {
		t.Error("a language with no server was accepted")
	}
}
