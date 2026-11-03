package gui

import (
	"image"
	"slices"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"

	"github.com/ironpark/mako/internal/kitty"
)

// A program shows images over the Kitty graphics protocol. The daemon's
// terminal answers the program; the one here keeps the images too, as it
// is fed the same bytes, and says where each is placed. A frame finds the
// placements and the textures they draw from, made again only when an
// image's pixels change; a paint draws them under the cells' backgrounds,
// between those and the text, or over the text, as each asks.

// imageTexture is an image the program sent, as a texture, with the
// generation of the pixels it was made from.
type imageTexture struct {
	img *ggfx.Image
	gen uint64
}

// placedImage is one placement to draw, with its texture.
type placedImage struct {
	gostty.RenderImagePlacement
	tex *ggfx.Image
}

// largeTexture is the side past which an image is a texture of its own
// rather than packed in the atlas, as ggui decides for its images.
const largeTexture = 512

// updatePlacementsLocked reads the placements the last state update found
// and makes the textures they need, letting go of those no longer placed.
// It runs on the UI goroutine, where textures are made.
func (t *termConn) updatePlacementsLocked() {
	t.images = t.images[:0]
	n := t.state.ImagePlacementCount()
	if n == 0 && len(t.textures) == 0 {
		return
	}
	t.placements = slices.Grow(t.placements[:0], int(n))[:n]
	if got, err := t.state.ImagePlacements(t.placements); err == nil {
		t.placements = t.placements[:got]
	} else {
		t.placements = t.placements[:0]
	}
	used := make(map[uint32]bool, len(t.placements))
	for _, p := range t.placements {
		tex := t.textureLocked(p.ImageID, p.ImageGeneration)
		if tex == nil {
			continue
		}
		used[p.ImageID] = true
		t.images = append(t.images, placedImage{RenderImagePlacement: p, tex: tex})
	}
	for id, tex := range t.textures {
		if !used[id] {
			tex.img.Deallocate()
			delete(t.textures, id)
		}
	}
}

// textureLocked is image id as a texture of its pixels of generation gen,
// made again when they changed; nil when they cannot be read.
func (t *termConn) textureLocked(id uint32, gen uint64) *ggfx.Image {
	if tex := t.textures[id]; tex != nil && tex.gen == gen {
		return tex.img
	}
	pixels := kitty.Pixels(t.vt.Terminal(), id)
	if pixels == nil {
		return nil
	}
	var img *ggfx.Image
	if b := pixels.Bounds(); b.Dx() > largeTexture || b.Dy() > largeTexture {
		img = ggfx.NewImageFromImageWithOptions(pixels, &ggfx.NewImageFromImageOptions{Unmanaged: true})
	} else {
		img = ggfx.NewImageFromImage(pixels)
	}
	if old := t.textures[id]; old != nil {
		old.img.Deallocate()
	}
	if t.textures == nil {
		t.textures = map[uint32]*imageTexture{}
	}
	t.textures[id] = &imageTexture{img: img, gen: gen}
	return img
}

// paintImages draws the frame's images in layer, in order. A placement's
// size and offsets are in the terminal's pixels, a cell of which is
// cellPx device pixels.
func (g grid) paintImages(dst *ggui.Canvas, f frameSnapshot, layer gostty.KittyLayer) {
	if len(f.images) == 0 {
		return
	}
	pxW, pxH := float64(f.cellPxW), float64(f.cellPxH)
	if pxW <= 0 || pxH <= 0 {
		pxW, pxH = g.cellW*g.scale, g.cellH*g.scale
	}
	kx, ky := g.cellW/pxW, g.cellH/pxH
	for _, p := range f.images {
		if p.Layer != layer || p.Width == 0 || p.Height == 0 {
			continue
		}
		at := ggui.Rct(
			ggui.Pt(g.origin.X+float64(p.X)*g.cellW+float64(p.OffsetX)*kx, g.origin.Y+float64(p.Y)*g.cellH+float64(p.OffsetY)*ky),
			ggui.Sz(float64(p.Width)*kx, float64(p.Height)*ky))
		img := p.tex
		src := image.Rect(int(p.SourceX), int(p.SourceY), int(p.SourceX+p.SourceWidth), int(p.SourceY+p.SourceHeight))
		if b := img.Bounds(); src != b && !src.Empty() {
			sub, ok := img.SubImage(src.Intersect(b)).(*ggfx.Image)
			if !ok {
				continue
			}
			img = sub
		}
		dst.DrawImage(img, at, ggui.ImageOptions{Fit: ggui.FitFill})
	}
}

// placeholder is the character a program fills cells with to place an
// image by them, which draws nothing itself.
const placeholder = 0x10EEEE
