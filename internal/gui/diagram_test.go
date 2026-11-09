package gui

import (
	"testing"

	mermaid "github.com/zkrebbekx/go-mermaid"
)

// A mermaid block is drawn, its text set over it; one that is not mermaid
// is not.
func TestAMermaidBlockIsDrawn(t *testing.T) {
	d, err := drawDiagram(renderMermaid, "flowchart LR\n  A[Start] --> B[End]", mermaid.Dark)
	if err != nil || d.Drawn == nil {
		t.Fatalf("drawn %v, %v", d.Drawn, err)
	}
	if len(d.Drawn.Labels) != 2 {
		t.Fatalf("labels %d, want 2", len(d.Drawn.Labels))
	}
	if _, err := drawDiagram(renderMermaid, "not a diagram", mermaid.Default); err == nil {
		t.Fatal("nonsense was drawn")
	}
}

// A Graphviz block is drawn as its graph.
func TestADotBlockIsDrawn(t *testing.T) {
	d, err := drawDiagram(diagramLangs["dot"], "digraph { a -> b; b -> c }", mermaid.Default)
	if err != nil || d.Drawn == nil || len(d.Drawn.Labels) != 3 {
		t.Fatalf("drawn %v, %v", d.Drawn, err)
	}
}
