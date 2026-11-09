package svgdraw

import (
	"bytes"
	"math"
	"testing"
)

// A shape's markers are drawn after it, at its ends, turned as it runs
// there and sized by its stroke.
func TestMarkersAreDrawnAtAShapesEnds(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z"/></marker></defs>
<path d="M10,10 L50,10 L50,90" stroke-width="2" marker-end="url(#arrow)" marker-start="url(#arrow)"/></svg>`)
	out, _, _, err := clean(svg)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(out, []byte(`<path d="M0,0 L10,5 L0,10 z">`)); got != 2 {
		t.Fatalf("drew %d markers, want 2:\n%s", got, out)
	}
	if !bytes.Contains(out, []byte(`translate(50,90) rotate(90) scale(1.4,1.4) translate(-9,-5)`)) {
		t.Fatalf("the end's marker is not at the end, turned down:\n%s", out)
	}
	if !bytes.Contains(out, []byte(`translate(10,10) rotate(180)`)) {
		t.Fatalf("the start's marker is not reversed:\n%s", out)
	}
	if _, ok := Draw(svg); !ok {
		t.Fatal("not drawn")
	}
}

// A path's vertices come from its commands, relative and curved ones too.
func TestPathVertices(t *testing.T) {
	vs := pathVertices("m0 0 h10 v10 c0 5 5 10 10 10")
	if len(vs) != 4 {
		t.Fatalf("vertices %v", vs)
	}
	last := vs[3]
	if last.x != 20 || last.y != 20 || math.Abs(last.angle) > 1e-9 {
		t.Fatalf("the curve ends at %v, running %v", last, last.angle)
	}
	if a := vs[1].angle; math.Abs(a-45) > 1e-9 {
		t.Fatalf("the corner turns %v, want 45", a)
	}
}

// Text turned by a transform keeps its turn; text on a path starts where
// the path does, turned as it runs there.
func TestTextTurnsAndFollowsAPath(t *testing.T) {
	_, texts, _, err := clean([]byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 100 100">
<defs><path id="p" d="M20,80 L80,20"/></defs>
<text x="10" y="50" transform="rotate(-90 10 50)">Axis</text>
<text><textPath xlink:href="#p">Along</textPath></text></svg>`))
	if err != nil || len(texts) != 2 {
		t.Fatalf("texts %+v, %v", texts, err)
	}
	if a := texts[0].angle; math.Abs(a+math.Pi/2) > 1e-9 {
		t.Errorf("the turned text's angle is %v, want -π/2", a)
	}
	if p := texts[1]; p.x != 20 || p.y != 80 || math.Abs(p.angle+math.Pi/4) > 1e-9 {
		t.Errorf("the text on a path is at %v,%v turned %v", p.x, p.y, p.angle)
	}
}
