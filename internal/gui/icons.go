package gui

import (
	"embed"
	"image/color"
	"math"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/mako/internal/agents"
)

// Lucide icons (ISC, see icons/LICENSE), the set Paseo draws with, and the
// agents' own (icons/agent-*.svg, drawn for mako). Only the ones the window
// uses are bundled.
//
//go:embed icons/*.svg
var iconFiles embed.FS

var (
	iconMu    sync.Mutex
	iconCache = map[string]*icons.SVG{}
)

// icon returns a bundled Lucide icon at 16px in the muted text colour.
func icon(name string) *icons.Widget {
	iconMu.Lock()
	svg, ok := iconCache[name]
	if !ok {
		var err error
		svg, err = icons.Load(iconFiles, "icons/"+name+".svg")
		if err != nil {
			panic("mako: missing icon " + name + ": " + err.Error())
		}
		iconCache[name] = svg
	}
	iconMu.Unlock()
	return icons.New(svg).Size(16).Color(tokMuted)
}

func iconColored(name string, c color.Color) *icons.Widget { return icon(name).Color(c) }

// agentIcon is the glyph a session row and tab show for an agent.
func agentIcon(name string) string {
	if name == "shell" || name == "" {
		return "square-terminal"
	}
	if e, ok := agents.Lookup(name); ok && e.Info.Icon != "" {
		return e.Info.Icon
	}
	return "bot"
}

// agentGlyph is an agent's icon in its own colour, or in c for an agent
// without one.
func agentGlyph(name string, c color.Color) *icons.Widget {
	if e, ok := agents.Lookup(name); ok && e.Info.Color.A != 0 {
		c = e.Info.Color
	}
	return iconColored(agentIcon(name), c)
}

// busyGlyph hops an agent's icon while busy reports its program at work, as
// an agent thinking or a build printing; at rest, or with motion reduced, it
// stands still. busy is asked on every paint, so the hop starts with the
// output that wakes the window and stops on the frame after it ends.
type busyGlyph struct {
	icon    *icons.Widget
	busy    func() bool
	reduced bool
}

// hopPeriod is one hop up and back; hopHeight how far it lifts.
const (
	hopPeriod = 700 * time.Millisecond
	hopHeight = 2.5
)

func (g *busyGlyph) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.reduced = env.ReducedMotion()
	return g.icon.Layout(c, env)
}

func (g *busyGlyph) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if !g.reduced && g.busy() {
		// Reading Now asks for the next frame, which keeps the hop going.
		phase := float64(ggui.Now().UnixNano()%int64(hopPeriod)) / float64(hopPeriod)
		r.Origin.Y -= math.Abs(math.Sin(phase*math.Pi)) * hopHeight
	}
	dst.Paint(g.icon, r)
}

// dotState is what a session's dot says of it.
type dotState int

const (
	dotRest    dotState = iota // nothing to say
	dotWorking                 // its program at work
	dotWaiting                 // its agent asks the user something
	dotDone                    // its agent finished a turn the user has not seen
)

// activityDot is a session's status in the sidebar: grey at rest; a soft
// green that breathes, its halo swelling and fading, while its program is
// at work; amber, breathing, while its agent waits on the user; and blue
// once it finished a turn not yet looked at. With motion reduced it does
// not breathe.
type activityDot struct {
	state   func() dotState
	reduced bool
}

// glowPeriod is one breath of the dot; dotSize its diameter.
const (
	glowPeriod = 1600 * time.Millisecond
	dotSize    = 7
)

func (d *activityDot) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	d.reduced = env.ReducedMotion()
	return c.Constrain(ggui.Sz(dotSize, dotSize))
}

func (d *activityDot) Paint(dst *ggui.Canvas, r ggui.Rect) {
	center := ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	var c uitheme.Color
	switch d.state() {
	case dotWorking:
		c = tokDotRunning
	case dotWaiting:
		c = tokDotWaiting
	case dotDone:
		dst.FillCircle(center, dotSize/2+1, tokDotDone)
		return
	default:
		dst.FillCircle(center, dotSize/2, tokDotExited)
		return
	}
	glow := 1.0
	if !d.reduced {
		// Reading Now asks for the next frame, which keeps it breathing.
		phase := float64(ggui.Now().UnixNano()%int64(glowPeriod)) / float64(glowPeriod)
		glow = (1 - math.Cos(phase*2*math.Pi)) / 2
	}
	dst.FillCircle(center, dotSize/2+1.5+2*glow, c.Alpha(0.12+0.18*glow))
	dst.FillCircle(center, dotSize/2, c.Alpha(0.7+0.3*glow))
}
