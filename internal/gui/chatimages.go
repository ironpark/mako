package gui

import (
	"image"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	xdraw "golang.org/x/image/draw"

	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/gui/skin"
)

// Images in Chat: those a message holds, which its agent's log held inline
// and its node keeps as files, and those a message or a draft names by
// their paths, as a pasted or dropped image is handed to an agent. Each
// shows as a thumbnail, read from the session's node, that opens the image
// in the Files tab's preview.

// imageRefs are the paths of the images text names, as words of their
// own, each once, those relative to project resolved against it.
func imageRefs(text, project string) []string {
	var out []string
	for _, word := range strings.Fields(text) {
		bare := strings.Trim(word, "\"'`()[]<>,;")
		if !isImage(bare) {
			continue
		}
		path := bare
		if project != "" {
			path = resolvePath(project, bare)
		}
		if !slices.Contains(out, path) {
			out = append(out, path)
		}
	}
	return out
}

// thumbBox is the box a thumbnail fits in.
const (
	thumbW = 240
	thumbH = 180
)

// chatImage reads the image at path on node, once for the window, and
// shows it as a thumbnail. One that cannot be read shows its name, or
// nothing when it is named only: text may name a file that is not there.
func chatImage(m *model, node, path string, named bool) ggui.Widget {
	key := node + "\x00" + path
	img, ok := m.chatImages.get(key)
	if !ok {
		img = m.chatImages.add(key)
		fetchImage(m, node, path, func(d decodedImage, err error) {
			go func() {
				s := shrinkImage(d, ggui.Sz(2*thumbW, 2*thumbH)) // for a screen of twice the pixels
				m.post(func() { m.chatImages.set(key, img, s.mdImage(err)) })
			}()
		})
	}
	name := baseName(path)
	open := func() { m.openItem(fileItem{Key: key, Node: node, Path: path, Name: name}, 0) }
	return ggui.View(img, func(v mdview.Image) ggui.Widget {
		var w ggui.Widget
		switch {
		case v.Loading:
			w = ggui.Box(ui.Spinner()).Size(64, 64)
		case v.Img == nil && named:
			return ggui.Box()
		case v.Img == nil:
			w = ggui.Box(ggui.Row(icon("file-image").Size(14).Color(skin.Muted), muted(ggui.Text(name).Size(12).NoWrap().Ellipsis())).Gap(6)).Pad(8, 10).MaxWidth(thumbW)
		default:
			// Size, not the pixels: an SVG's are drawn at twice it.
			scale := min(1, thumbW/max(v.Size.W, 1), thumbH/max(v.Size.H, 1))
			w = ggui.Image(v.Img).Alt(name).Size(v.Size.W*scale, v.Size.H*scale)
		}
		return ggui.Tap(ggui.Box(w).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(2), open)
	})
}

// chatImages shows the images at paths on node, wrapped in rows: those
// held, then those named.
func chatImages(m *model, node string, held, named []string) ggui.Widget {
	var thumbs []ggui.Widget
	for _, p := range held {
		thumbs = append(thumbs, chatImage(m, node, p, false))
	}
	for _, p := range named {
		if !slices.Contains(held, p) {
			thumbs = append(thumbs, chatImage(m, node, p, true))
		}
	}
	return ggui.Wrap(thumbs...).Gap(6).RunGap(6)
}

// attachmentStrip shows the files a session's draft goes with, over the
// composer's text: an image as its thumbnail, another file as its name,
// each with a button that takes it off.
func attachmentStrip(m *model, id string, state *chatState) ggui.Widget {
	return ggui.View(state.attached, func(paths []string) ggui.Widget {
		if len(paths) == 0 {
			return ggui.Box()
		}
		node := ggui.Untrack(func() string { s, _ := m.session(id); return s.GetNodeId() })
		items := make([]ggui.Widget, len(paths))
		for i, p := range paths {
			remove := ui.ButtonOf(icon("x").Size(11).Color(skin.Muted), func() { m.detachFrom(id, p) }).
				Ghost().Pad(2).Radius(4).Name(Tf("Remove %s", baseName(p)))
			var item ggui.Widget
			if isImage(p) {
				item = chatImage(m, node, p, false)
			} else {
				item = ggui.Box(ggui.Row(
					fileTypeIcon(baseName(p), false, false).Size(14),
					ggui.Text(baseName(p)).Size(12).NoWrap().Ellipsis(),
					ggui.Box().Width(14), // under the corner's button
				).Gap(6).Align(ggui.AlignCenter)).Fill(skin.Surface2).Border(1, skin.Border).Radius(8).Pad(7, 8).MaxWidth(thumbW)
			}
			items[i] = &cornered{child: item, corner: ggui.Box(remove).Fill(skin.Surface1).Radius(5)}
		}
		return ggui.Box(ggui.Wrap(items...).Gap(6).RunGap(6)).Pad(0, 0, 4, 0)
	})
}

// cornered is its child, as large as the child is, with corner over its
// top-right corner, inside it.
type cornered struct {
	child, corner ggui.Widget
	cornerSize    ggui.Size
}

func (c *cornered) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	size := c.child.Layout(cs.Loosen(), env)
	c.cornerSize = c.corner.Layout(ggui.Loose(size), env)
	return size
}

func (c *cornered) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(c.child, r)
	if r.Size.W < c.cornerSize.W || r.Size.H < c.cornerSize.H {
		return // a thumbnail too small to hold it, as one not there
	}
	dst.Paint(c.corner, ggui.Rct(ggui.Pt(r.Origin.X+r.Size.W-c.cornerSize.W-2, r.Origin.Y+2), c.cornerSize))
}

// Images read for Chat and for pages are kept for the window, the latest
// used of them, up to imageCacheBytes of pixels: a session's images, or a
// page's from the web, would otherwise stay for as long as it runs. An
// image let go of while it shows stays on show, and is read again once it
// is asked for. Each is kept no larger than it shows (shrinkImage).
const imageCacheBytes = 128 << 20

// imageCache is the images read for Chat and pages, by node and path or by
// address. UI goroutine only.
type imageCache struct {
	entries    map[string]*cachedImage
	bytes, max int
	clock      uint64
}

type cachedImage struct {
	img   *ggui.StateValue[mdview.Image]
	bytes int
	used  uint64
}

func newImageCache() *imageCache {
	return &imageCache{entries: map[string]*cachedImage{}, max: imageCacheBytes}
}

// get is the image kept under key, noting it used.
func (c *imageCache) get(key string) (*ggui.StateValue[mdview.Image], bool) {
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.clock++
	e.used = c.clock
	return e.img, true
}

// add keeps a new image under key, loading until set.
func (c *imageCache) add(key string) *ggui.StateValue[mdview.Image] {
	img := ggui.State(mdview.Image{Loading: true})
	c.clock++
	c.entries[key] = &cachedImage{img: img, used: c.clock}
	return img
}

// set shows v as img, the image added under key, and lets go of the images
// used longest ago while those kept hold too many pixels.
func (c *imageCache) set(key string, img *ggui.StateValue[mdview.Image], v mdview.Image) {
	img.Set(v)
	e, ok := c.entries[key]
	if !ok || e.img != img {
		return // let go of while it was read
	}
	c.bytes -= e.bytes
	e.bytes = 0
	if v.Img != nil {
		b := v.Img.Bounds()
		e.bytes = b.Dx() * b.Dy() * 4
	}
	c.bytes += e.bytes
	for c.bytes > c.max && len(c.entries) > 1 {
		oldest := ""
		for k, o := range c.entries {
			if k != key && (oldest == "" || o.used < c.entries[oldest].used) {
				oldest = k
			}
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
}

// shrunkImage is an image read, its pixels made no larger than it shows,
// with the size it has as read.
type shrunkImage struct {
	decodedImage
	size ggui.Size // 0 for one not shrunk
}

// shrinkImage scales an image read down to fit within box, keeping its
// shape, as it is read: off the window's goroutine. A drawn SVG keeps its
// size, as its pixels are drawn into it.
func shrinkImage(d decodedImage, box ggui.Size) shrunkImage {
	s := shrunkImage{decodedImage: d}
	if d.Img != nil {
		b := d.Img.Bounds()
		if small, ok := scaledToFit(d.Img, box); ok {
			s.Img, s.size = small, ggui.Sz(float64(b.Dx()), float64(b.Dy()))
		}
	}
	if d.Drawn != nil {
		if small, ok := scaledToFit(d.Drawn.Pixels, box); ok {
			drawn := *d.Drawn
			drawn.Pixels = small
			s.Drawn = &drawn
		}
	}
	return s
}

// mdImage is the image for a page, as asMDImage makes it, its size the one
// it had as read. It makes its texture, and so runs on the window's
// goroutine.
func (s shrunkImage) mdImage(err error) mdview.Image {
	v := asMDImage(s.decodedImage, err)
	if s.size.W > 0 && v.Img != nil && s.Img != nil {
		v.Size = s.size
	}
	return v
}

// scaledToFit is img scaled down to fit within box, keeping its shape, or
// false for one that fits already.
func scaledToFit(img image.Image, box ggui.Size) (*image.RGBA, bool) {
	b := img.Bounds()
	scale := min(box.W/max(float64(b.Dx()), 1), box.H/max(float64(b.Dy()), 1))
	if scale >= 1 {
		return nil, false
	}
	w, h := max(int(float64(b.Dx())*scale), 1), max(int(float64(b.Dy())*scale), 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst, true
}
