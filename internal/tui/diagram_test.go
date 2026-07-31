package tui

import (
	"testing"
)

// H2.18: concurrent mermaid renders shared one atomic result slot, so a second
// result overwrote a first that had not been drained. The lost render's source
// stays mapped to "" — the sentinel meaning "already started" — which blocks
// any retry for the rest of the session: the diagram simply never appears.
func TestEveryDiagramResultIsDrained(t *testing.T) {
	m := newTestModel(t)
	m.diagrams = map[string]string{}

	sources := []string{"graph A", "graph B", "graph C"}
	for _, src := range sources {
		m.diagrams[src] = "" // as renderDiagrams marks them before starting
		m.finishDiagram(&mermaidRendered{Source: src, Path: "/tmp/" + src + ".png"})
	}

	// Every render that finished must reach the transcript. Draining is what
	// the render loop does between frames.
	for range sources {
		m.drainDiagrams()
	}

	for _, src := range sources {
		switch m.diagrams[src] {
		case "":
			t.Errorf("the render of %q was lost; its source is still marked started, "+
				"so it can never be retried", src)
		case "/tmp/" + src + ".png":
		default:
			t.Errorf("%q resolved to %q", src, m.diagrams[src])
		}
	}
}
