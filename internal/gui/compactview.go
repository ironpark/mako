package gui

import (
	"encoding/json"
	"slices"
	"strconv"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// A phone does not lay the window's document out: it lists the
// document's blocks as pages (layoutdoc.Pages), those of its compact
// root, variants.compact, when it has one, else of its root, folded. The
// window shows that list for the document of the space shown, in Settings
// ▸ Spaces, as the phone will list it; and lets the user keep another:
// pages in another order, or some left out, written as the document's
// compact root through the window's own edit of it, which syncs to the
// phone as any edit of the workspace does. An agent or a plugin never
// writes it (agent_layout.go); this is the user's.

// compactRootID is the compact root's own ID.
const compactRootID = "phone"

// shownDoc is the document of the space shown, as the window reads it,
// and its name: the built-in default for none.
func (m *model) shownDoc(p spaceSet) (string, *layoutdoc.Doc) {
	name := p.shownLayoutName()
	if body := p.WindowLayouts[name]; len(body) > 0 {
		if d, err := layoutdoc.Parse(body, m.blocks); err == nil {
			return name, d
		}
	}
	return name, layoutdoc.Default()
}

// foldedPages are the pages of d's root, folded, ignoring its compact
// root: every block a phone could list.
func foldedPages(d *layoutdoc.Doc) []layoutdoc.Page {
	return layoutdoc.Pages(&layoutdoc.Doc{Root: d.Root})
}

// setCompactPages writes pages as the compact root of the document of the
// name, d as it stands: none when they are the root's own, folded.
func (m *model) setCompactPages(name string, d *layoutdoc.Doc, pages []layoutdoc.Page) {
	next := *d
	next.Compact = nil
	if !slices.EqualFunc(pages, foldedPages(d), samePage) {
		root := &layoutdoc.Node{Kind: layoutdoc.KindColumn, ID: compactRootID}
		for _, pg := range pages {
			root.Children = append(root.Children, &layoutdoc.Node{Kind: layoutdoc.KindBlock, ID: pg.ID, Block: pg.Block, Props: pg.Props})
		}
		next.Compact = root
	}
	body, err := json.Marshal(&next)
	if err != nil {
		m.tellWindowDoc(err)
		return
	}
	m.setWindowLayout(name, body)
}

func samePage(a, b layoutdoc.Page) bool { return a.ID == b.ID && a.Block == b.Block }

// pageTitle is a page's block as the window names it.
func (m *model) pageTitle(block string) string {
	if def, ok := m.blocks.lookup(block); ok && def.Title != "" {
		return T(def.Title)
	}
	return block
}

// compactSection shows the pages a phone lists of the document of the
// space shown, and lets the user move them, leave some out, and go back
// to the window's own order.
func compactSection(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		p := m.spaces()
		m.blocks.rev.Get() // a plugin's block's title comes and goes
		name, d := m.shownDoc(p)
		pages := layoutdoc.Pages(d)
		set := func(next []layoutdoc.Page) { m.setCompactPages(name, d, next) }
		var rows []ggui.Widget
		for i, pg := range pages {
			move := func(by int) func() {
				return func() {
					next := slices.Clone(pages)
					next[i], next[i+by] = next[i+by], next[i]
					set(next)
				}
			}
			up := ui.ButtonOf(icon("arrow-up").Size(14), move(-1)).Ghost().Name(T("Move up")).Disabled(i == 0)
			down := ui.ButtonOf(icon("arrow-down").Size(14), move(1)).Ghost().Name(T("Move down")).Disabled(i == len(pages)-1)
			hide := ui.Button(T("Hide"), func() { set(slices.Delete(slices.Clone(pages), i, i+1)) }).Outline().Disabled(len(pages) == 1)
			rows = append(rows, widget.Box(widget.Row(
				muted(widget.Text(strconv.Itoa(i+1)).Size(12).Font(ggui.DefaultMonoFont())),
				widget.Expanded(widget.Text(m.pageTitle(pg.Block)).Size(13).NoWrap().Ellipsis()),
				muted(widget.Text(pg.Block).Size(11.5).Font(ggui.DefaultMonoFont()).NoWrap()),
				up, down, hide,
			).Gap(8).Align(widget.AlignCenter)).Pad(8, 14))
		}
		for _, pg := range foldedPages(d) {
			// A compact root the user wrote by hand may give a block an ID
			// of its own: it is shown by the block it shows.
			if slices.ContainsFunc(pages, func(x layoutdoc.Page) bool { return x.Block == pg.Block }) {
				continue
			}
			rows = append(rows, widget.Box(widget.Row(
				icon("circle-slash").Size(14).Color(skin.ExtraMuted),
				widget.Expanded(muted(widget.Text(m.pageTitle(pg.Block)).Size(13).NoWrap().Ellipsis())),
				ui.Button(T("Show"), func() { set(append(slices.Clone(pages), pg)) }).Outline(),
			).Gap(8).Align(widget.AlignCenter)).Pad(8, 14))
		}
		head := Tf("The pages a phone lists of the document %s, which the space shown uses, in order.", name)
		if d.Compact != nil {
			head += " " + T("The document keeps an order of its own for a phone.")
		}
		var foot ggui.Widget = widget.Box()
		if d.Compact != nil {
			foot = widget.Box(widget.Align(ui.Button(T("Use the window's order"), func() { set(foldedPages(d)) }).Outline()).Left()).Pad(10, 14)
		}
		return card(append([]ggui.Widget{widget.Box(muted(widget.Text(head).Size(12.5))).Pad(12, 14)}, append(rows, foot)...)...)
	})
}
