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
	"github.com/ironpark/mako/internal/protocol"
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

// nodeAgents is what the nodes' drivers say of the agents they launch, by
// name, so an agent this build has no driver for shows as its node says.
var nodeAgents sync.Map // name → *protocol.AgentInfo

// noteAgents takes what the nodes say of their agents.
func noteAgents(nodes []*protocol.Node) {
	for _, n := range nodes {
		for _, a := range n.GetAgents() {
			if a.GetIcon() != "" || a.GetColor() != 0 || a.GetCaps() != 0 {
				nodeAgents.Store(a.GetName(), a)
			}
		}
	}
}

// agentLook is what is known of the agent called name: its glyph, its
// colour (nil for none) and protocol's Cap bits, as a node's driver says
// or else as this build's does.
func agentLook(name string) (glyph string, c color.Color, caps uint32) {
	if v, ok := nodeAgents.Load(name); ok {
		a := v.(*protocol.AgentInfo)
		if a.GetColor() != 0 {
			r, g, b, alpha := a.Channels()
			c = color.NRGBA{r, g, b, alpha}
		}
		return a.GetIcon(), c, a.GetCaps()
	}
	if e, ok := agents.Lookup(name); ok {
		if e.Info.Color.A != 0 {
			c = e.Info.Color
		}
		return e.Info.Icon, c, e.Caps
	}
	return "", nil, 0
}

// agentIcon is the glyph a session row and tab show for an agent: its
// own, if this build has it.
func agentIcon(name string) string {
	glyph, _, _ := agentLook(name)
	return iconOf(name, glyph)
}

// iconOf is the icon bundled for the agent called name whose glyph is
// glyph: the shell's, the agent's own, or a generic one.
func iconOf(name, glyph string) string {
	switch {
	case name == "shell" || name == "":
		return "square-terminal"
	case glyph != "" && hasIcon(glyph):
		return glyph
	}
	return "bot"
}

// bundled remembers which icons are bundled, by name.
var bundled sync.Map

// hasIcon reports whether the icon called name is bundled.
func hasIcon(name string) bool {
	if ok, seen := bundled.Load(name); seen {
		return ok.(bool)
	}
	_, err := iconFiles.Open("icons/" + name + ".svg")
	bundled.Store(name, err == nil)
	return err == nil
}

// agentGlyph is an agent's icon in its own colour, or in c for an agent
// without one.
func agentGlyph(name string, c color.Color) *icons.Widget {
	glyph, own, _ := agentLook(name)
	if own != nil {
		c = own
	}
	return iconColored(iconOf(name, glyph), c)
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

// idleRate is how often the sidebar's slow motions, a hop and a breath,
// are drawn: 20 times a second, which they need no more than. Each frame
// paints the whole window, so at the display's 120 a single breathing
// dot kept the app on 1.5 cores.
const idleRate = time.Second / 20

// idleTick is the frame's time, for a slow motion to be drawn at, and asks
// for the frame idleRate after it, which keeps the motion going.
func idleTick() time.Time {
	now := ggui.FrameTime()
	ggui.WakeAt(now.Add(idleRate))
	return now
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
		phase := float64(idleTick().UnixNano()%int64(hopPeriod)) / float64(hopPeriod)
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
		phase := float64(idleTick().UnixNano()%int64(glowPeriod)) / float64(glowPeriod)
		glow = (1 - math.Cos(phase*2*math.Pi)) / 2
	}
	dst.FillCircle(center, dotSize/2+1.5+2*glow, c.Alpha(0.12+0.18*glow))
	dst.FillCircle(center, dotSize/2, c.Alpha(0.7+0.3*glow))
}
