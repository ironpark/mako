package gui

import (
	"image/color"
	"math"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"rsc.io/qr"

	"github.com/ironpark/mako/internal/protocol"
)

// Pairing: a code, shown as a QR code, that a phone's camera reads to
// join. It says where this node is and carries a token the daemon made; a
// node that asks to be let in with it is let in at once, and the code is
// used up.

// pairingSettings is the Access page's pairing code: a button that makes
// one, or the code waiting to be read.
func pairingSettings(m *model, pairing *protocol.Pairing) ggui.Widget {
	if pairing == nil {
		return card(row(T("Pair a phone"), T("Show a QR code for mako's Android app to scan. The phone that reads it is let in at once, without a request to allow."),
			ui.Button(T("Show QR code"), func() {
				m.changeAccess(&protocol.Access{Op: "pair"}, "")
			})))
	}
	code, err := qr.Encode(pairing.GetLink(), qr.M)
	if err != nil {
		return card(row(T("Pair a phone"), err.Error(), ggui.Box()))
	}
	expires := protocol.TimeOf(pairing.GetExpires()).Local().Format("15:04")
	return card(ggui.Box(ggui.Row(
		&qrWidget{code: code, size: 196},
		ggui.Expanded(ggui.Column(
			ggui.Text(T("Scan with the phone")).Size(13.5),
			muted(ggui.Text(T("In mako's Android app, open Computers and tap Scan QR code. The phone that reads this code is let in, and the code is then used up.")).Size(12)),
			muted(ggui.Text(Tf("Valid until %s.", expires)).Size(12)),
			ggui.Box().Height(6),
			ggui.Row(
				ui.Button(T("New code"), func() {
					m.changeAccess(&protocol.Access{Op: "pair"}, "")
				}).Outline(),
				ui.Button(T("Hide"), func() {
					m.changeAccess(&protocol.Access{Op: "unpair"}, "")
				}).Ghost(),
			).Gap(8),
		).Gap(6).Stretch()),
	).Gap(20)).Pad(16, 16))
}

// qrWidget draws a QR code, dark on light whatever the theme, as cameras
// read it best, with the quiet zone around it the standard asks for.
type qrWidget struct {
	code *qr.Code
	size float64
}

// qrQuiet is the modules of light around the code.
const qrQuiet = 4

func (q *qrWidget) Layout(c ggui.Constraints, _ ggui.Env) ggui.Size {
	return c.Constrain(ggui.Sz(q.size, q.size))
}

func (q *qrWidget) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.FillRoundRect(r, 8, color.White)
	// Each module is a whole number of pixels, so none blurs into the next.
	scale := dst.Scale()
	n := q.code.Size + 2*qrQuiet
	px := math.Floor(math.Min(r.Size.W, r.Size.H) * scale / float64(n))
	if px < 1 {
		return
	}
	module := px / scale
	side := module * float64(n)
	x0 := math.Round((r.Origin.X+(r.Size.W-side)/2)*scale)/scale + module*qrQuiet
	y0 := math.Round((r.Origin.Y+(r.Size.H-side)/2)*scale)/scale + module*qrQuiet
	for y := range q.code.Size {
		// A run of dark modules in a row is one rectangle.
		for x := 0; x < q.code.Size; {
			if !q.code.Black(x, y) {
				x++
				continue
			}
			start := x
			for x < q.code.Size && q.code.Black(x, y) {
				x++
			}
			dst.FillRect(ggui.Rct(ggui.Pt(x0+float64(start)*module, y0+float64(y)*module), ggui.Sz(float64(x-start)*module, module)), color.Black)
		}
	}
}
