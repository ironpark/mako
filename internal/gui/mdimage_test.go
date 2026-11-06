package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><rect width="8" height="4"/></svg>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if d, err := readWebImage(srv.URL + "/a.png"); err != nil || d.Img == nil || d.Img.Bounds().Dx() != 3 {
		t.Fatalf("a picture: %v, %v", d, err)
	}
	if d, err := readWebImage(srv.URL + "/badge"); err != nil || d.Drawn == nil || d.Drawn.Size.W != 8 {
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
		h := pageHost{doc: docInfo{Path: c.doc}}
		if got := h.beside(c.rel); got != c.want {
			t.Errorf("%s beside %s = %s, want %s", c.rel, c.doc, got, c.want)
		}
	}
}
