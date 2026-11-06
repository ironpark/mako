package gui

import (
	"image/color"
	"strings"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/mdview"
)

// pageHost is a Markdown page's window: the file it is, on its node, and
// the tab it shows in. Text written somewhere other than a file, as on
// GitHub, has no tab and no path, so nothing is beside it.
type pageHost struct {
	m   *model
	id  string  // the file's tab; "" for none
	doc docInfo // its node and path; Path is "" for none
}

// Open follows a link: a web address in the browser, a file beside this
// one in a tab of its own.
func (h pageHost) Open(dest string) {
	switch {
	case dest == "" || strings.HasPrefix(dest, "#"):
		return
	case strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:"):
		h.m.openWeb(h.doc.Node, dest) // a server a file on another node names, as a README's
		return
	}
	if h.doc.Path == "" {
		return // not a file's, with nothing beside it
	}
	if i := strings.IndexAny(dest, "#?"); i >= 0 {
		dest = dest[:i]
	}
	p := h.beside(dest)
	h.m.openItem(fileItem{Key: dirKey(h.doc.Node, p), Node: h.doc.Node, Path: p, Name: baseName(p)}, 0)
}

// beside is a relative path from the file's folder, on its node.
func (h pageHost) beside(rel string) string {
	return resolvePath(parentDir(h.doc.Path), rel)
}

// Image is a picture read once: for the window from the web, or for the
// file from beside it on its node.
func (h pageHost) Image(dest string) *ggui.StateValue[mdview.Image] {
	switch {
	case strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://"):
		return h.m.webImage(dest)
	case isImage(dest) || isSVG(dest):
		doc := h.m.docs[h.id]
		if doc == nil || h.doc.Path == "" {
			return nil // nothing it is beside
		}
		where := h.beside(dest)
		if doc.images == nil {
			doc.images = map[string]*ggui.StateValue[mdview.Image]{}
		}
		img, ok := doc.images[where]
		if !ok {
			img = ggui.State(mdview.Image{Loading: true})
			doc.images[where] = img
			fetchImage(h.m, h.doc.Node, where, func(d decodedImage, err error) { img.Set(asMDImage(d, err)) })
		}
		return img
	}
	return nil
}

// Code is a code block highlighted in its language, as the editor would.
func (h pageHost) Code(src, lang string) ggui.Widget {
	var l language
	ok := false
	if lang != "" {
		l, ok = h.m.hl.languageNamed(lang)
	}
	return codeText(h.m, src, l, ok, ggui.Peek(h.m.Prefs).FontSize)
}

// Fold keeps a details open or folded with the file, so it stays as the
// reader left it while the page is drawn again.
func (h pageHost) Fold(n int, open bool) *ggui.StateValue[bool] {
	doc := h.m.docs[h.id]
	if doc == nil {
		return ggui.State(open)
	}
	if doc.folds == nil {
		doc.folds = map[int]*ggui.StateValue[bool]{}
	}
	s, ok := doc.folds[n]
	if !ok {
		s = ggui.State(open)
		doc.folds[n] = s
	}
	return s
}

func (pageHost) Text(s string) string { return T(s) }

func (pageHost) Icon(name string, size float64, c color.Color) ggui.Widget {
	return icon(name).Size(size).Color(c)
}
