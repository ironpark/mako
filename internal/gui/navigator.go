package gui

import (
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// navigator is a folder shown and where it has been, as a file manager
// has them: a node's file browser, and the folder picker.
type navigator struct {
	Path    *ggui.StateValue[string]  // the folder shown
	History *ggui.StateValue[history] // the folders to go back and forward to
	shown   func(path string)         // reads the folder shown, as it changes
}

// history is where a navigator has been, and where it went back from.
type history struct{ Back, Ahead []string }

func newNavigator(path string, shown func(string)) *navigator {
	return &navigator{Path: ggui.State(path), History: ggui.State(history{}), shown: shown}
}

// goTo shows the folder path, remembering the one before to go back to.
func (n *navigator) goTo(path string) {
	cur := ggui.Peek(n.Path)
	if path == cur || path == "" {
		return
	}
	h := ggui.Peek(n.History)
	n.show(path, history{Back: append(h.Back, cur)})
}

// reset shows the folder path with no history.
func (n *navigator) reset(path string) { n.show(path, history{}) }

func (n *navigator) show(path string, h history) {
	n.History.Set(h)
	n.Path.Set(path)
	n.shown(path)
}

// goBack goes back to the folder before, or forward to the one gone back
// from, when there is one.
func (n *navigator) goBack(forward bool) {
	h := ggui.Peek(n.History)
	from, to := &h.Back, &h.Ahead
	if forward {
		from, to = to, from
	}
	if len(*from) == 0 {
		return
	}
	next := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	*to = append(*to, ggui.Peek(n.Path))
	n.show(next, h)
}

// goUp shows the folder the one shown is in.
func (n *navigator) goUp() {
	cur := ggui.Peek(n.Path)
	if up := parentDir(cur); up != "" && up != cur {
		n.goTo(up)
	}
}

// navButtons are the back, forward and up buttons.
func navButtons(n *navigator) ggui.Widget {
	return ggui.View(n.History, func(h history) ggui.Widget {
		btn := func(name, label string, off bool, fn func()) ggui.Widget {
			return ui.ButtonOf(icon(name).Size(15), fn).Ghost().Pad(5).Radius(6).Name(label).Disabled(off)
		}
		return ggui.Row(
			btn("arrow-left", T("Back"), len(h.Back) == 0, func() { n.goBack(false) }),
			btn("arrow-right", T("Forward"), len(h.Ahead) == 0, func() { n.goBack(true) }),
			btn("arrow-up-to-line", T("Enclosing folder"), false, n.goUp),
		).Gap(2)
	})
}
