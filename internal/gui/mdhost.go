package gui

import (
	"image/color"
	"strings"

	"github.com/ironpark/ggui"
)

// mdHost is what a Markdown page needs of the window around it: where
// its links lead, where its pictures come from, how its code is shown,
// and what it keeps between drawings. Embed mdBase to take its defaults.
type mdHost interface {
	// Open follows a link the reader tapped: a web address, or a path
	// relative to the page.
	Open(dest string)
	// Image is the picture dest names, read once and kept; nil when the
	// host cannot read it, which the page shows as its link.
	Image(dest string) *ggui.StateValue[mdImage]
	// Code is a code block's text, highlighted in lang ("" for none),
	// selectable and not wrapping. The page puts it in its box.
	Code(src, lang string) ggui.Widget
	// Fold is whether the page's nth details is open, kept so it stays as
	// the reader left it; open is its first state.
	Fold(n int, open bool) *ggui.StateValue[bool]
	// Text is s in the window's language.
	Text(s string) string
	// Icon is the window's icon named name, at size, in c.
	Icon(name string, size float64, c color.Color) ggui.Widget
}

// mdBase is a host for a page with nowhere to go: links lead nowhere, no
// pictures are read, code is plain, folds are forgotten and text is as
// written.
type mdBase struct{}

func (mdBase) Open(string)                            {}
func (mdBase) Image(string) *ggui.StateValue[mdImage] { return nil }
func (mdBase) Fold(_ int, open bool) *ggui.StateValue[bool] {
	return ggui.State(open)
}
func (mdBase) Text(s string) string { return s }
func (mdBase) Icon(string, float64, color.Color) ggui.Widget {
	return ggui.Box()
}
func (mdBase) Code(src, _ string) ggui.Widget {
	return ggui.RichText(ggui.Span(strings.TrimSuffix(src, "\n"))).NoWrap().Selectable().Font(ggui.DefaultMonoFont())
}

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
func (h pageHost) Image(dest string) *ggui.StateValue[mdImage] {
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
			doc.images = map[string]*ggui.StateValue[mdImage]{}
		}
		img, ok := doc.images[where]
		if !ok {
			img = ggui.State(mdImage{Loading: true})
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
