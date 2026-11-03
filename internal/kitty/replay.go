package kitty

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ironpark/gostty"
)

// A terminal that attaches to a session is fed the screen as text, which
// holds none of the images on it, so the images placed there are sent again
// after it: each image once, then each placement of it where it was.

// ReplayBudget bounds the pixels sent again for one screen, in bytes: a
// screen of a few images is far less, and one past it shows what fit.
const ReplayBudget = 32 << 20

// chunk is how much of an image's base64 goes in one escape sequence, as
// the protocol asks of a program.
const chunk = 4096

// Replay is what shows the images on term's screen again on a terminal fed
// the screen's text, the cursor left where it was; nothing when there are
// none. A placement scrolled partly above the screen is left out.
func Replay(term *gostty.Terminal) string {
	images, err := gostty.NewKittyImages()
	if err != nil {
		return ""
	}
	defer images.Close()
	if images.Update(term) != nil {
		return ""
	}
	placements := make([]gostty.KittyPlacement, images.PlacementCount())
	n, err := images.Placements(placements)
	if err != nil || n == 0 {
		return ""
	}
	var b strings.Builder
	sent := map[uint32]bool{}
	budget := ReplayBudget
	for _, p := range placements[:n] {
		if !p.Virtual && (p.ViewportRow < 0 || p.ViewportCol < 0) {
			continue
		}
		if !sent[p.ImageID] {
			size, ok := transmit(&b, term, p.ImageID, budget)
			if !ok {
				continue
			}
			sent[p.ImageID] = true
			budget -= size
		}
		place(&b, p)
	}
	if b.Len() == 0 {
		return ""
	}
	return "\x1b7" + b.String() + "\x1b8"
}

// transmit writes image id of term to b, as RGB or RGBA, when it fits in
// budget, and reports its size.
func transmit(b *strings.Builder, term *gostty.Terminal, id uint32, budget int) (int, bool) {
	info, ok := term.KittyImage(id)
	if !ok || int(info.DataLen) > budget {
		return 0, false
	}
	data := make([]byte, info.DataLen)
	n, err := term.KittyImageData(id, data)
	if err != nil || n == 0 {
		return 0, false
	}
	data = data[:n]
	format := 24
	switch {
	case info.Compression != gostty.KittyCompressionNone:
		return 0, false
	case info.Format == gostty.KittyFormatRgb:
	case info.Format == gostty.KittyFormatRgba:
		format = 32
	default:
		img := toImage(info, data)
		if img == nil {
			return 0, false
		}
		data, format = img.Pix, 32
	}
	enc := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(enc); i += chunk {
		more := 0
		if i+chunk < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(b, "\x1b_Ga=t,f=%d,s=%d,v=%d,i=%d,q=2,m=%d;", format, info.Width, info.Height, id, more)
		} else {
			fmt.Fprintf(b, "\x1b_Gm=%d;", more)
		}
		b.WriteString(enc[i:min(i+chunk, len(enc))])
		b.WriteString("\x1b\\")
	}
	return len(data), true
}

// place writes placement p again: at the cell it was at, the cursor left
// there, or as the virtual placement placeholder cells draw.
func place(b *strings.Builder, p gostty.KittyPlacement) {
	id := ""
	if p.PlacementID != 0 {
		id = fmt.Sprintf(",p=%d", p.PlacementID)
	}
	if p.Virtual {
		fmt.Fprintf(b, "\x1b_Ga=p,U=1,i=%d%s,c=%d,r=%d,q=2\x1b\\", p.ImageID, id, p.GridCols, p.GridRows)
		return
	}
	fmt.Fprintf(b, "\x1b[%d;%dH", p.ViewportRow+1, p.ViewportCol+1)
	fmt.Fprintf(b, "\x1b_Ga=p,i=%d%s,x=%d,y=%d,w=%d,h=%d,X=%d,Y=%d,z=%d,C=1,q=2",
		p.ImageID, id, p.SourceX, p.SourceY, p.SourceWidth, p.SourceHeight, p.XOffset, p.YOffset, p.Z)
	// Drawn at its own size, it is placed at it again; else in the cells it
	// covered, which the terminal it goes to may size otherwise.
	if p.PixelWidth != p.SourceWidth || p.PixelHeight != p.SourceHeight {
		fmt.Fprintf(b, ",c=%d,r=%d", p.GridCols, p.GridRows)
	}
	b.WriteString("\x1b\\")
}
