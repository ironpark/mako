package gui

import (
	"testing"

	mermaid "github.com/zkrebbekx/go-mermaid"
)

// A mermaid block is drawn, its text set over it; one that is not mermaid
// is not.
func TestAMermaidBlockIsDrawn(t *testing.T) {
	d, err := drawMermaid("flowchart LR\n  A[Start] --> B[End]", mermaid.Dark)
	if err != nil || d.Drawn == nil {
		t.Fatalf("drawn %v, %v", d.Drawn, err)
	}
	if len(d.Drawn.Labels) != 2 {
		t.Fatalf("labels %d, want 2", len(d.Drawn.Labels))
	}
	if _, err := drawMermaid("not a diagram", mermaid.Default); err == nil {
		t.Fatal("nonsense was drawn")
	}
}
