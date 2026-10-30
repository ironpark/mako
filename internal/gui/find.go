package gui

import (
	"fmt"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// ⌘F (Ctrl+F elsewhere) finds in the session in view: its terminal's
// scrollback, or its conversation. A bar over the session's top-right
// corner takes the words; Enter goes to the match before, further up and
// older, Shift+Enter to the one after, and Escape closes the bar. The
// words stay for the next find, in this session or another.

// findPlace is where a find is: the match shown, from 1, of how many; At
// is 0 when there is none.
type findPlace struct{ At, Count int }

// finder is what a session's view finds with: run goes to the match before
// the one shown, or after it with forward, starting afresh for new words,
// and end lets the last one go as the bar closes.
type finder struct {
	run func(needle string, forward bool) findPlace
	end func()
}

// openFind opens the find bar over the selected session, its field
// focused, and finds the words left in it.
func (m *model) openFind() {
	if !isSessionTab(ggui.Peek(m.Selected)) {
		return
	}
	m.findFocus = true
	m.FindOpen.Set(true)
}

// findBar is the find bar over session id's view, while the find is open
// and the session selected.
func findBar(m *model, id string, f finder) ggui.Widget {
	shown := ggui.Derived(func() bool { return m.FindOpen.Get() && m.Selected.Get() == id })
	return ggui.If(shown, func() ggui.Widget {
		place := ggui.State(findPlace{})
		run := func(forward bool) { place.Set(f.run(ggui.Peek(m.FindText), forward)) }
		closeBar := func() {
			f.end()
			m.FindOpen.Set(false)
		}
		ggui.OnCleanup(f.end)
		// The words the bar opens with are found at once.
		run(false)
		field := ui.TextField(m.FindText).Placeholder(T("Find")).Name(T("Find in session")).
			OnChange(func(string) { run(false) }).
			OnSubmit(func(string) { run(false) }).
			OnKey(func(ev ggui.KeyEvent) bool {
				if ev.Kind != ggui.KeyPress {
					return false
				}
				switch {
				case ev.Key == ggui.KeyEscape:
					closeBar()
					return true
				case ev.Key == ggui.KeyEnter && ev.Mods.Shift:
					run(true)
					return true
				}
				return false
			})
		focus := &focusOnce{child: ggui.Box(field).Width(220), target: field.Input(), want: func() bool {
			want := m.findFocus
			m.findFocus = false
			return want
		}}
		count := ggui.Map(place, func(p findPlace) string {
			switch {
			case ggui.Peek(m.FindText) == "":
				return ""
			case p.Count == 0:
				return T("No matches")
			case p.At == 0:
				return fmt.Sprint(p.Count)
			}
			return fmt.Sprintf("%d/%d", p.At, p.Count)
		})
		bar := ggui.Row(
			icon("search").Size(14).Color(tokMuted),
			focus,
			ggui.Box(muted(ggui.TextOf(count).Size(12).NoWrap())).Width(56),
			iconButton("chevron-up", T("Previous match"), func() { run(false) }),
			iconButton("chevron-down", T("Next match"), func() { run(true) }),
			iconButton("x", T("Close find"), closeBar),
		).Gap(6).Align(ggui.AlignCenter)
		return ggui.Align(ggui.Padding(ggui.Box(bar).Fill(tokSurface1).Border(1, tokBorder).Radius(8).Pad(4, 6, 4, 10), 8, 12)).At(1, 0)
	})
}

// terminalFinder finds in a terminal's scrollback.
func terminalFinder(conn *termConn) finder {
	if conn == nil {
		return finder{run: func(string, bool) findPlace { return findPlace{} }, end: func() {}}
	}
	return finder{
		run: func(needle string, forward bool) findPlace {
			at, count := conn.find(needle, forward)
			return findPlace{At: at, Count: count}
		},
		end: conn.endFind,
	}
}

// chatMatch is a match in a conversation: the turn it is in, and which
// of the turn's matches it is, from 0.
type chatMatch struct {
	key string
	n   int
}

// chatMatches are the matches of needle in the turns' words, the user's
// and the agent's, oldest first. A tool's call and output are left out:
// a folded one shows neither.
func chatMatches(turns []chatTurnItem, needle string) []chatMatch {
	if needle == "" {
		return nil
	}
	var out []chatMatch
	for _, t := range turns {
		if len(t.Messages) == 0 || t.Messages[0].GetRole() == "tool" {
			continue
		}
		text := t.Messages[0].GetText()
		for i, n := 0, 0; ; n++ {
			lo, hi := ggui.IndexFold(text, needle, i)
			if lo < 0 {
				break
			}
			out = append(out, chatMatch{key: t.Key, n: n})
			i = hi
		}
	}
	return out
}

// chatFinder finds in a conversation. Only the turns near the window are
// laid out, so a match is found in the turns' words, its turn scrolled to,
// and the match then selected in what was painted of it, as the turn
// comes to be. New words start from the latest match.
type chatFinder struct {
	turns  ggui.Readable[[]chatTurnItem]
	scroll *ggui.ScrollController
	area   *ggui.SelectionAreaWidget

	needle  string
	matches []chatMatch
	cur     int

	pending *chatMatch // scrolled to, to select once its turn is painted
	tries   int
	marks   map[string]rowMark // where each turn was last painted
	serial  int                // counts the paints of the feed
}

// rowMark is where a turn was painted, and in which paint of the feed.
type rowMark struct {
	at     ggui.Rect
	serial int
}

func (c *chatFinder) finder() finder {
	return finder{run: c.run, end: c.end}
}

func (c *chatFinder) run(needle string, forward bool) findPlace {
	turns := ggui.Peek(c.turns)
	matches := chatMatches(turns, needle)
	switch {
	case len(matches) == 0:
		c.needle, c.matches, c.pending = needle, nil, nil
		return findPlace{}
	case needle != c.needle || len(matches) != len(c.matches):
		c.cur = len(matches) - 1
	case forward:
		c.cur = (c.cur + 1) % len(matches)
	default:
		c.cur = (c.cur - 1 + len(matches)) % len(matches)
	}
	c.needle, c.matches = needle, matches
	m := matches[c.cur]
	c.pending, c.tries = &m, 0
	c.scroll.JumpToKey(m.key)
	// Counted from the latest, as the terminal's find counts.
	return findPlace{At: len(matches) - c.cur, Count: len(matches)}
}

func (c *chatFinder) end() { c.pending = nil }

// mark wraps a turn's row so that its place is noted as it is painted.
func (c *chatFinder) mark(key string, row ggui.Widget) ggui.Widget {
	return &markedRow{child: row, note: func(r ggui.Rect) { c.marks[key] = rowMark{at: r, serial: c.serial} }}
}

// painted runs after each paint of the feed: once the turn a match is in
// has been painted, the match is selected in it and brought into view.
func (c *chatFinder) painted(dst *ggui.Canvas) {
	defer func() { c.serial++ }()
	p := c.pending
	if p == nil {
		return
	}
	if mk, ok := c.marks[p.key]; ok && mk.serial == c.serial {
		if at, ok := c.area.Find(c.needle, mk.at.Origin.Y, p.n); ok {
			c.pending = nil
			const room = 24 // around the match, so it is not at the window's edge
			dst.RequestReveal(ggui.Rct(at.Origin.Sub(ggui.Pt(room, room)), ggui.Sz(at.Size.W+2*room, at.Size.H+2*room)))
			return
		}
	}
	// Not painted yet, as the scroll goes to an estimate of where it is:
	// a few frames more.
	if c.tries++; c.tries < 6 {
		c.scroll.JumpToKey(p.key)
	} else {
		c.pending = nil
	}
}

// markedRow notes where its child was painted.
type markedRow struct {
	child ggui.Widget
	note  func(ggui.Rect)
}

func (r *markedRow) Layout(c ggui.Constraints, env ggui.Env) ggui.Size { return r.child.Layout(c, env) }

func (r *markedRow) Paint(dst *ggui.Canvas, at ggui.Rect) {
	r.note(at)
	dst.Paint(r.child, at)
}

// afterPaint runs fn each time it is painted, after what is painted
// before it.
type afterPaint struct{ fn func(*ggui.Canvas) }

func (a *afterPaint) Layout(c ggui.Constraints, _ ggui.Env) ggui.Size {
	return c.Constrain(ggui.Size{})
}

func (a *afterPaint) Paint(dst *ggui.Canvas, _ ggui.Rect) { a.fn(dst) }
