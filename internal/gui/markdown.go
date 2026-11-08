package gui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/mdview"
)

// A Markdown file's tab shows it rendered as well as its source: the
// header's switch flips between them. mdview lays the page out; the
// window is its Host, pageHost (mdhost.go).

// isMarkdown reports whether a file's name says it is Markdown.
func isMarkdown(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd", ".mdx":
		return true
	}
	return false
}

// markdownView is a Markdown file rendered, scrolling; see mdview.Page.
func markdownView(m *model, id string, src string) ggui.Widget {
	return mdview.Page(pageHost{m: m, id: id, doc: ggui.Peek(m.Docs)[id]}, src)
}

// markdownText is Markdown written somewhere other than a file, as on
// GitHub: its blocks, to set in a column of their own. Its relative links
// lead nowhere.
func markdownText(m *model, src string) ggui.Widget {
	return mdview.Column(pageHost{m: m}, src)
}

// webImageLimit bounds an image read from the web.
const webImageLimit = 20 << 20

// isSVG reports whether a name or address names an SVG.
func isSVG(name string) bool {
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	return strings.EqualFold(path.Ext(name), ".svg")
}

// asMDImage is an image file as read, for a page: an SVG drawn, anything
// else decoded as it came.
func asMDImage(d decodedImage, err error) mdview.Image {
	if err != nil {
		d.Img = nil // an SVG drawn shows all the same
	}
	return mdview.ImageOf(d.Img, d.Drawn)
}

// pageImageMax is the most pixels on its long side an image of a page is
// kept with: more than a page's width shows, at twice the pixels.
const pageImageMax = 2048

// webImage is the image at the web address url, read once for the window.
func (m *model) webImage(url string) *ggui.StateValue[mdview.Image] {
	key := "web\x00" + url
	if img, ok := m.chatImages.get(key); ok {
		return img
	}
	img := m.chatImages.add(key)
	go func() {
		d, err := readWebImage(url)
		s := shrinkImage(d, ggui.Sz(pageImageMax, pageImageMax))
		m.post(func() { m.chatImages.set(key, img, s.mdImage(err)) })
	}()
	return img
}

// readWebImage reads and decodes the image at url, drawing an SVG.
func readWebImage(url string) (decodedImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return decodedImage{}, err
	}
	req.Header.Set("User-Agent", "mako")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return decodedImage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodedImage{}, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webImageLimit+1))
	if err != nil {
		return decodedImage{}, err
	}
	if len(data) > webImageLimit {
		return decodedImage{}, fmt.Errorf("%s is too large", url)
	}
	if isSVG(url) || strings.Contains(resp.Header.Get("Content-Type"), "svg") {
		return drawnSVG(nil, data), nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return decodedImage{Img: img}, err
}
