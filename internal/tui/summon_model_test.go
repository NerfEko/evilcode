package tui

import "testing"

func TestParseSummonArgs(t *testing.T) {
	task, model := parseSummonArgs("wire the auth flow")
	if task != "wire the auth flow" || model != "" {
		t.Fatalf("got task=%q model=%q", task, model)
	}
	task, model = parseSummonArgs("-m small@mock wire the auth flow")
	if task != "wire the auth flow" || model != "small@mock" {
		t.Fatalf("got task=%q model=%q", task, model)
	}
	task, model = parseSummonArgs("-m")
	if task != "-m" || model != "" {
		t.Fatalf("bare -m should stay a task, got task=%q model=%q", task, model)
	}
	if task, _ := parseSummonArgs(""); task != "" {
		t.Fatalf("empty should stay empty, got %q", task)
	}
}
