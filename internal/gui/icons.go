package gui

import (
	"embed"
	"image/color"
	"math"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui/icons"
)

// Lucide icons (ISC, see icons/LICENSE), the set Paseo draws with, and the
// agents' own (icons/agent-*.svg, drawn for sanbo). Only the ones the window
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
			panic("sanbo: missing icon " + name + ": " + err.Error())
		}
		iconCache[name] = svg
	}
	iconMu.Unlock()
	return icons.New(svg).Size(16).Color(tokMuted)
}

func iconColored(name string, c color.Color) *icons.Widget { return icon(name).Color(c) }

// agentIcon is the glyph a session row and tab show for an agent.
func agentIcon(agent string) string {
	switch agent {
	case "shell", "":
		return "square-terminal"
	case "claude", "codex", "agy":
		return "agent-" + agent
	default:
		return "bot"
	}
}

// agentColors tint the agents that have a colour of their own.
var agentColors = map[string]color.Color{
	"claude": color.NRGBA{0xd9, 0x77, 0x57, 0xff},
	"codex":  color.NRGBA{0x7b, 0x6c, 0xf0, 0xff},
	"agy":    color.NRGBA{0x3e, 0x8b, 0xff, 0xff},
}

// agentGlyph is an agent's icon in its own colour, or in c for an agent
// without one.
func agentGlyph(agent string, c color.Color) *icons.Widget {
	if own, ok := agentColors[agent]; ok {
		c = own
	}
	return iconColored(agentIcon(agent), c)
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
