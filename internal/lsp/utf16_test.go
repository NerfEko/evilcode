package lsp

import (
	"testing"
	"unicode/utf8"
)

// H1.4: LSP character offsets are UTF-16 code units. Slicing a Go string with
// them as byte offsets corrupts every line with non-ASCII text to the left of
// the edit, and can cut a UTF-8 sequence in half.
func TestApplyEditsUsesUTF16Offsets(t *testing.T) {
	cases := []struct {
		name string
		text string
		// start/end are UTF-16 offsets of the identifier on line 0.
		start, end int
		newText    string
		want       string
	}{
		{
			// "héllo" is 5 UTF-16 units and 6 bytes: everything after it is
			// one byte further along than the protocol offset says.
			name:  "accented text before the edit",
			text:  `x := "héllo"; old := 1` + "\n",
			start: 14, end: 17, newText: "renamed",
			want: `x := "héllo"; renamed := 1` + "\n",
		},
		{
			// An emoji is one rune, four bytes, and *two* UTF-16 units.
			name:  "astral plane before the edit",
			text:  `x := "🔥"; old := 1` + "\n",
			start: 11, end: 14, newText: "renamed",
			want: `x := "🔥"; renamed := 1` + "\n",
		},
		{
			name:  "non-ascii inside the replaced range",
			text:  "vär := 1\n",
			start: 0, end: 3, newText: "v",
			want: "v := 1\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyEdits(tc.text, []TextEdit{
				{Range: Range{Position{0, tc.start}, Position{0, tc.end}}, NewText: tc.newText},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Error("the edit produced invalid UTF-8")
			}
		})
	}
}

func TestApplyEditsRefusesAnOffsetInsideARune(t *testing.T) {
	// Half of a surrogate pair names no byte position at all. Refusing keeps
	// rename atomic — the caller computes every file before writing any.
	if _, err := ApplyEdits("x := \"🔥\"\n", []TextEdit{
		{Range: Range{Position{0, 7}, Position{0, 8}}, NewText: "y"},
	}); err == nil {
		t.Error("an offset splitting a surrogate pair was accepted")
	}
}
