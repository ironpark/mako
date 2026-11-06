package mdview

import (
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/yuin/goldmark/text"

	"github.com/ironpark/mako/internal/gui/svgdraw"
)

// A paragraph of images alone, a README's badges in their links or an
// <img> among them, shows as images; one with words among them does not.
func TestAParagraphOfImagesShowsThem(t *testing.T) {
	r := &mdRenderer{host: Base{}}
	pics := func(src string) ([]mdPicture, bool) {
		r.src = []byte(src)
		doc := markdown.Parser().Parse(text.NewReader(r.src))
		return r.picturesOnly(doc.FirstChild())
	}
	got, ok := pics("[![CI](https://ci/badge.svg)](https://ci) ![logo](logo.png)\n<img src=\"shot.png\" width=\"300\">")
	if !ok || len(got) != 3 {
		t.Fatalf("pictures %+v, %v", got, ok)
	}
	if got[0].dest != "https://ci/badge.svg" || got[0].link != "https://ci" || got[0].alt != "CI" {
		t.Errorf("a badge in its link: %+v", got[0])
	}
	if got[1].dest != "logo.png" || got[1].link != "" {
		t.Errorf("an image: %+v", got[1])
	}
	if got[2].dest != "shot.png" || got[2].width != 300 {
		t.Errorf("an <img>: %+v", got[2])
	}
	if _, ok := pics("See ![logo](logo.png) here."); ok {
		t.Error("an image among words shows as an image")
	}
}

// Inline HTML tags style the text up to the tag that closes them.
func TestInlineTagsStyleTheirText(t *testing.T) {
	for tag, want := range map[string]func(mdStyle) bool{
		"<b>":                  func(s mdStyle) bool { return s.text.Weight == ggui.WeightBold },
		"<em>":                 func(s mdStyle) bool { return s.italic },
		"<kbd>":                func(s mdStyle) bool { return s.code },
		"<del>":                func(s mdStyle) bool { return s.strike },
		`<a href="https://x">`: func(s mdStyle) bool { return s.link == "https://x" },
	} {
		name, closing, ok := htmlTagName(tag)
		if !ok || closing || !want(mdStyle{}.tagged(name, tag)) {
			t.Errorf("%s: name %q, closing %v, ok %v", tag, name, closing, ok)
		}
	}
	if name, closing, ok := htmlTagName("</B>"); !ok || !closing || name != "b" {
		t.Errorf("a closing tag: %q %v %v", name, closing, ok)
	}
	if _, _, ok := htmlTagName("<br/>"); ok {
		t.Error("a tag that closes itself opens one")
	}
}

// An image is drawn at its own size, or the one asked for keeping its
// shape, and no wider than the page.
func TestAnImageFitsThePage(t *testing.T) {
	img := ggfx.NewImage(4, 2)
	defer img.Deallocate()
	for _, c := range []struct {
		size, want ggui.Size
		room       float64
		got        ggui.Size
	}{
		{ggui.Sz(400, 200), ggui.Size{}, 800, ggui.Sz(400, 200)},
		{ggui.Sz(2000, 1000), ggui.Size{}, 800, ggui.Sz(800, 400)},
		{ggui.Sz(400, 200), ggui.Sz(100, 0), 800, ggui.Sz(100, 50)},
		{ggui.Sz(400, 200), ggui.Sz(0, 50), 800, ggui.Sz(100, 50)},
	} {
		f := &fitImage{img: img, size: c.size, want: c.want}
		if got := f.Layout(ggui.Loose(ggui.Sz(c.room, 2000)), ggui.Env{}); got != c.got {
			t.Errorf("%v asked %v in %v: %v, want %v", c.size, c.want, c.room, got, c.got)
		}
	}
}

// An SVG is drawn from its paths, sized as its view box; one that cannot
// be drawn is none.
func TestAnSVGIsDrawn(t *testing.T) {
	v := ImageOf(nil, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20"><rect width="40" height="20" fill="#f00"/></svg>`))
	if v.Img == nil || v.Size != ggui.Sz(40, 20) {
		t.Fatalf("drawn %+v", v)
	}
	if b := v.Img.Bounds(); b.Dx() != 40*svgdraw.Scale || b.Dy() != 20*svgdraw.Scale {
		t.Fatalf("drawn %v pixels", b)
	}
	if ImageOf(nil, []byte("not svg")).Img != nil {
		t.Fatal("drew what is no SVG")
	}
}
