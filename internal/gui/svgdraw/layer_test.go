package svgdraw

import "testing"

// An element drawn through a clip shows only where the clip's shapes are,
// and what is drawn after it lies over it; one through a mask shows as
// light as the mask is.
func TestClipsAndMasksAreApplied(t *testing.T) {
	pic, ok := Draw([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20">
<defs><clipPath id="r"><rect width="40" height="20" rx="8"/></clipPath>
<mask id="m"><rect width="20" height="20" fill="#fff"/><rect x="20" width="20" height="20" fill="#000"/></mask></defs>
<g clip-path="url(#r)"><rect width="40" height="20" fill="#f00"/></g>
<rect x="30" y="8" width="4" height="4" fill="#00f"/>
<g mask="url(#m)" transform="translate(0,0)"><rect y="16" width="40" height="4" fill="#0f0"/></g></svg>`))
	if !ok {
		t.Fatal("not drawn")
	}
	at := func(x, y float64) [4]uint8 {
		c := pic.Pixels.RGBAAt(int(x*Scale), int(y*Scale))
		return [4]uint8{c.R, c.G, c.B, c.A}
	}
	if c := at(0.2, 0.2); c[3] != 0 {
		t.Errorf("a clipped corner shows %v", c)
	}
	if c := at(20, 5); c != [4]uint8{255, 0, 0, 255} {
		t.Errorf("inside the clip %v, want red", c)
	}
	if c := at(32, 10); c != [4]uint8{0, 0, 255, 255} {
		t.Errorf("what is drawn after the clipped group %v, want blue over it", c)
	}
	if c := at(10, 17); c[1] != 255 {
		t.Errorf("under the mask's white %v, want green", c)
	}
	if c := at(30, 17); c[1] == 255 {
		t.Errorf("under the mask's black %v, want none of the green", c)
	}
}
