package tui

import (
	"strings"
	"testing"
	"time"
)

// H3.13: the model list was fetched inside Update — a network call with a
// five-second timeout — so opening the picker froze every frame until the
// provider answered. The picker must open now and fill in later.
func TestOpeningTheModelPickerDoesNotBlock(t *testing.T) {
	m := newTestModel(t)

	done := make(chan struct{})
	var cmd func() any
	go func() {
		defer close(done)
		if c := m.openPicker(); c != nil {
			cmd = func() any { return c() }
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("opening the picker blocked the update loop")
	}

	if !m.pickerOpen {
		t.Fatal("the picker did not open")
	}
	if len(m.picker.Entries) == 0 {
		t.Error("the picker opened with nothing in it; it should show the current model")
	}
	if cmd == nil {
		t.Fatal("no command was returned to fetch the model list")
	}

	// And the fetch, when it lands, replaces what was shown.
	msg, ok := cmd().(modelsLoaded)
	if !ok {
		t.Fatalf("the command produced %T", cmd())
	}
	m.applyModels(msg)
	if len(m.picker.Entries) == 0 {
		t.Error("the picker is empty after the model list arrived")
	}
}

// H3.12: the clipboard read shelled out from inside Update. A hung clipboard
// tool froze the interface with no way to type past it.
func TestPastingDoesNotBlockTheUpdateLoop(t *testing.T) {
	m := newTestModel(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.pasteImage()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pasting blocked the update loop on a clipboard tool")
	}
	if !strings.Contains(m.notice, "clipboard") {
		t.Errorf("notice = %q, want it to say the read is under way", m.notice)
	}
}

// A stale search result must not replace what the user has typed since.
func TestASearchResultForAnOldQueryIsIgnored(t *testing.T) {
	m := newTestModel(t)
	m.sessions.Filter = "retry gate"

	m.applySemanticHits(semanticHits{query: "auth flow", hits: nil})
	if len(m.sessions.Semantic) != 0 {
		t.Error("a result for a query the user has typed past was shown")
	}
}
