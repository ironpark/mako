// Package kitty is what mako adds to ghostty for the Kitty protocols a
// program on a session's terminal speaks.
//
// Graphics: ghostty parses and keeps the images on both terminals of a
// session, the daemon's and a front-end's; this decodes the PNGs they are
// sent as, turns an image's pixels into one a front-end draws, and writes
// the images shown on a screen again for a terminal that attaches to it
// after they were sent. Importing the package installs the PNG decoder,
// which ghostty needs to take PNGs at all: without one a program asking
// whether they work is told no.
//
// Keyboard: ghostty encodes keys as the program's flags say; this reads
// which flags it set, for a front-end that must know what to send and for
// a screen written again.
package kitty

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"

	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/sys"
)

// largestPNG bounds the PNGs decoded, in pixels: a program may say any
// size, and decoding allocates for it before reading a pixel.
const largestPNG = 10000 * 10000

func init() {
	if gostty.LoadError() != nil {
		return // every call into gostty panics; the program says why itself
	}
	sys.OnPNGDecodeRequest(decodePNG)
}

// decodePNG answers ghostty's request to decode data, a PNG, with its
// pixels; an answer it never gets fails the transmission.
func decodePNG(data []byte) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > largestPNG {
		return
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return
	}
	nrgba := toNRGBA(img)
	_ = sys.ReplyPNGImage(uint32(nrgba.Rect.Dx()), uint32(nrgba.Rect.Dy()), nrgba.Pix)
}

// toNRGBA is img as straight RGBA, four bytes a pixel, row after row.
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) && n.Stride == 4*n.Rect.Dx() {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, img, b.Min, draw.Src)
	return out
}

// Pixels is image id of term as one to draw, nil when there is no such
// image or its pixels cannot be read. ghostty keeps an image as it was
// sent, decompressed, and PNGs decoded.
func Pixels(term *gostty.Terminal, id uint32) *image.NRGBA {
	info, ok := term.KittyImage(id)
	if !ok || info.Width == 0 || info.Height == 0 || info.Compression != gostty.KittyCompressionNone {
		return nil
	}
	data := make([]byte, info.DataLen)
	n, err := term.KittyImageData(id, data)
	if err != nil {
		return nil
	}
	return toImage(info, data[:n])
}

// toImage is data, pixels laid out as info says, as straight RGBA.
func toImage(info gostty.KittyImage, data []byte) *image.NRGBA {
	w, h := int(info.Width), int(info.Height)
	var per int
	switch info.Format {
	case gostty.KittyFormatRgba:
		per = 4
	case gostty.KittyFormatRgb:
		per = 3
	case gostty.KittyFormatGrayAlpha:
		per = 2
	case gostty.KittyFormatGray:
		per = 1
	case gostty.KittyFormatPng:
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		return toNRGBA(img)
	default:
		return nil
	}
	if len(data) < w*h*per {
		return nil
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if per == 4 {
		copy(out.Pix, data)
		return out
	}
	for i, o := 0, 0; i < w*h*per; i, o = i+per, o+4 {
		switch per {
		case 3:
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = data[i], data[i+1], data[i+2], 0xff
		case 2:
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = data[i], data[i], data[i], data[i+1]
		case 1:
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = data[i], data[i], data[i], 0xff
		}
	}
	return out
}
