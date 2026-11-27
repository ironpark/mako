package gui

import (
	"context"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// AppProbe drives the full Mako GUI model headlessly under ggui.Probe for E2E tests.
type AppProbe struct {
	m      *model
	p      *ggui.Probe
	cancel context.CancelFunc
}

// NewAppProbe initializes a headless Mako GUI window backed by ggui.Probe.
// When addr is non-empty it connects to that external daemon socket without
// spawning an embedded daemon.
func NewAppProbe(cfg *config.Config, addr string, size ggui.Size) *AppProbe {
	if cfg == nil {
		cfg = config.Default()
	}
	if size.W <= 0 || size.H <= 0 {
		size = ggui.Sz(1280, 800)
	}
	m := newModel(cfg, addr)
	if addr != "" {
		m.Prefs.Update(func(p prefs) prefs {
			p.DaemonMode = "external"
			p.Daemon = addr
			return p
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, size)
	p.Setup(func() {
		m.post = p.Post
		setup(m)
		if addr != "" {
			m.start(ctx, p.Post)
		}
	})
	shortcuts(p, m)
	p.Frame()
	return &AppProbe{m: m, p: p, cancel: cancel}
}

func (a *AppProbe) Probe() *ggui.Probe { return a.p }

func (a *AppProbe) SidebarPage() string {
	if a.m == nil || a.m.SidebarPage == nil {
		return ""
	}
	return ggui.Peek(a.m.SidebarPage)
}

func (a *AppProbe) OpenSidebarPage(id string) {
	if a.m == nil {
		return
	}
	a.m.openSidebarPage(id)
	a.p.Frame()
}

func (a *AppProbe) SelectedSession() string {
	if a.m == nil || a.m.Selected == nil {
		return ""
	}
	return ggui.Peek(a.m.Selected)
}

func (a *AppProbe) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.p != nil {
		a.p.Close()
	}
	if a.m != nil {
		a.m.shutdown()
	}
}

// NewUiTreeProbe renders an arbitrary protocol.UiNode tree headlessly with theme
// tokens, persistent blockForm state, and an event callback for E2E verification.
func NewUiTreeProbe(tree *protocol.UiNode, onEvent func(id, kind, value string), size ggui.Size) *ggui.Probe {
	if size.W <= 0 || size.H <= 0 {
		size = ggui.Sz(800, 600)
	}
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		form.redraw()
		v := newTreeView(nil, "e2e:probe", "", form, tree)
		v.onEvent = func(_, id, kind, value string, _ map[string]string) {
			if onEvent != nil {
				onEvent(id, kind, value)
			}
		}
		if tree == nil {
			return ggui.Widget(v.node(&protocol.UiNode{Kind: "column"}, false))
		}
		return probeTreeRoot{
			child: v.node(tree, false),
			fill:  tree.GetFill(),
		}
	}, size)
	p.Setup(func() {
		uitheme.Set(skin.Theme(skin.Light))
	})
	p.Frame()
	return p
}

// probeTreeRoot mirrors real plugin block sizing: fill=true trees expand to the
// viewport constraints, while non-fill trees measure with MinH=0 so maxHeight
// constraints are reflected in the laid-out height under ggui.Probe's Tight(size).
type probeTreeRoot struct {
	child ggui.Widget
	fill  bool
}

func (p probeTreeRoot) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	if p.fill {
		return p.child.Layout(c, env)
	}
	loose := ggui.Constraints{MinW: c.MinW, MaxW: c.MaxW, MinH: 0, MaxH: c.MaxH}
	return loose.Constrain(p.child.Layout(loose, env))
}

func (p probeTreeRoot) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(p.child, r)
}
