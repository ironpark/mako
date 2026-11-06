package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/yuin/goldmark/text"

	"github.com/ironpark/mako/internal/gui/svgdraw"
)

// A paragraph of images alone, a README's badges in their links or an
// <img> among them, shows as images; one with words among them does not.
func TestAParagraphOfImagesShowsThem(t *testing.T) {
	r := &mdRenderer{}
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
	v := svgImage([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20"><rect width="40" height="20" fill="#f00"/></svg>`))
	if v.Img == nil || v.Size != ggui.Sz(40, 20) {
		t.Fatalf("drawn %+v", v)
	}
	if b := v.Img.Bounds(); b.Dx() != 40*svgdraw.Scale || b.Dy() != 20*svgdraw.Scale {
		t.Fatalf("drawn %v pixels", b)
	}
	if svgImage([]byte("not svg")).Img != nil {
		t.Fatal("drew what is no SVG")
	}
}

// An image on the web is read from it: a picture decoded, an SVG kept to
// be drawn, an address that fails an error.
func TestAnImageOnTheWebIsRead(t *testing.T) {
	var pic bytes.Buffer
	m := image.NewRGBA(image.Rect(0, 0, 3, 2))
	m.Set(0, 0, color.White)
	_ = png.Encode(&pic, m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.png":
			w.Write(pic.Bytes())
		case "/badge":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if d, err := readWebImage(srv.URL + "/a.png"); err != nil || d.Img == nil || d.Img.Bounds().Dx() != 3 {
		t.Fatalf("a picture: %v, %v", d, err)
	}
	if d, err := readWebImage(srv.URL + "/badge"); err != nil || string(d.SVG) != "<svg/>" {
		t.Fatalf("an SVG: %v, %v", d, err)
	}
	if _, err := readWebImage(srv.URL + "/gone.png"); err == nil {
		t.Fatal("an image not there was read")
	}
}

// An image beside a file on a Windows node resolves on that node, whatever
// this machine is: the page's folder by its backslashes, the link by
// either separator.
func TestAnImageBesideAWindowsFile(t *testing.T) {
	for _, c := range []struct{ doc, rel, want string }{
		{`C:\Users\me\mako\README.md`, "docs/shot.png", `C:\Users\me\mako\docs\shot.png`},
		{`C:\Users\me\mako\docs\guide.md`, "../assets/logo.svg", `C:\Users\me\mako\assets\logo.svg`},
		{`C:\Users\me\mako\README.md`, `.\img\a.png`, `C:\Users\me\mako\img\a.png`},
		{`C:\Users\me\mako\README.md`, `D:/pics/a.png`, `D:\pics\a.png`},
		{`\\srv\share\README.md`, "a.png", `\\srv\share\a.png`},
		{"/home/me/mako/README.md", "docs/shot.png", "/home/me/mako/docs/shot.png"},
	} {
		r := &mdRenderer{doc: docInfo{Path: c.doc}}
		if got := r.beside(c.rel); got != c.want {
			t.Errorf("%s beside %s = %s, want %s", c.rel, c.doc, got, c.want)
		}
	}
}
