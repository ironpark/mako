package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/protocol"
)

// BenchmarkTerminalPaint paints a screen of a TUI: coloured ASCII, box
// drawing, blocks and Hangul.
func BenchmarkTerminalPaint(b *testing.B) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		b.Fatal(err)
	}
	view := newTerminalView(conn, defaultPrefs, false)
	p := ggui.NewProbe(view, ggui.Sz(800, 500))
	defer func() { p.Close(); close(link.in); conn.close() }()
	var s strings.Builder
	s.WriteString("\x1b[2J\x1b[H╭" + strings.Repeat("─", 60) + "╮\r\n")
	for i := range 20 {
		s.WriteString("│ \x1b[3" + string(rune('1'+i%6)) + "mline " + strings.Repeat("ab ", 5) + "\x1b[0m 안녕하세요 ▌▐ ✻ ❯ │\r\n")
	}
	s.WriteString("╰" + strings.Repeat("─", 60) + "╯")
	link.in <- &protocol.StreamEvent{Kind: protocol.EventOutput, Data: []byte(s.String())}
	img := ggfx.NewImage(800, 500)
	for range 50 {
		p.Draw(img)
		if strings.Contains(screenText(conn), "╰") {
			break
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p.Draw(img) // a probe paints each frame whole
	}
}
