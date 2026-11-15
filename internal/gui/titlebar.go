package gui

import (
	"runtime"
	"time"

	"github.com/ironpark/ggui"
)

// On macOS the window has no title bar of its own: its content runs to the
// top, the traffic lights sit in the headers along it, which leave them
// room, and a press on a header where nothing else takes it moves the
// window, as a press on a title bar does. Elsewhere the window keeps its
// title bar.

// titlebarHidden says the window's own title bar is hidden.
var titlebarHidden = runtime.GOOS == "darwin"

// The traffic lights stand trafficLightsX from the window's left edge,
// centred in a header, and the headers keep trafficLightsRoom of their
// starts clear of them. A second press on a header within doubleClick of
// the first is a double-click.
const (
	trafficLightsX      = 16
	trafficLightsHeight = 14
	trafficLightsRoom   = 80
	doubleClick         = 400 * time.Millisecond
)

// trafficLights is where the window's traffic lights go, centred in the
// headers along its top.
func trafficLights() *ggui.Point {
	return &ggui.Point{X: trafficLightsX, Y: (headerHeight - trafficLightsHeight) / 2}
}

// titlebarRoom is how much of a header's start, at the window's left edge,
// the traffic lights take: none in full screen, where they are not there,
// or where the window has a title bar.
func (m *model) titlebarRoom() ggui.Readable[float64] {
	if !titlebarHidden || m.window == nil {
		return ggui.Const(0.0)
	}
	return ggui.Map(m.window.State(), func(s ggui.WindowState) float64 {
		if s == ggui.WindowFullscreen {
			return 0
		}
		return trafficLightsRoom
	})
}

// titlebar makes w, a header along the window's top, stand for its title
// bar: a press on it that nothing in it takes moves the window, and a
// double-click does what the system says one on a title bar does.
func titlebar(m *model, w ggui.Widget) ggui.Widget {
	if !titlebarHidden {
		return w
	}
	var last time.Time
	return ggui.Pointer(w).OnDown(func(ev ggui.PointerEvent) {
		if ev.Button != ggui.MouseButtonLeft || m.window == nil {
			return
		}
		if time.Since(last) < doubleClick {
			last = time.Time{}
			m.window.TitlebarDoubleClick()
			return
		}
		last = time.Now()
		m.window.StartDrag()
	})
}

// titlebarGap is the start of a header: pad wide, or as wide as the
// traffic lights need, room, when they stand there.
func titlebarGap(room ggui.Readable[float64], pad float64) ggui.Widget {
	return ggui.Box().BindWidth(ggui.Map(room, func(r float64) float64 { return max(r, pad) }))
}
