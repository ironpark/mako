package gui

import (
	"cmp"
	"reflect"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
)

// keyedDialog builds a ui.Dialog whose controls support intuitive keyboard
// navigation in addition to ggui's built-in FocusTrap (Tab / Shift+Tab,
// Enter / Space activation, and Escape to close):
//   - Arrow keys (Left / Right / Up / Down) step through focusable controls
//     whenever the currently focused widget does not consume them (e.g.
//     buttons, checkboxes, switches — leaving text inputs, trees, and open
//     selects untouched).
//   - The initially focused control shows its keyboard focus ring as soon as
//     the dialog opens.
func keyedDialog(open ggui.Binding[bool], content ggui.Widget) *ui.DialogWidget {
	return ui.Dialog(open, newDialogKeyNav(open, content))
}

// alertAsk configures a confirmation dialog with Cancel and Confirm buttons.
type alertAsk struct {
	Open        ggui.Binding[bool]
	Title       string
	Body        string
	Action      string // confirm button label; defaults to T("Continue")
	Cancel      string // cancel button label; defaults to T("Cancel")
	Destructive bool
	Confirm     func()
	OnCancel    func()
}

// alertDialog builds a modal confirmation dialog matching ui.AlertDialog's
// layout and policy (non-dismissible scrim, no corner close button, Escape
// cancels, Cancel and Confirm in the footer band), wrapped with keyboard
// navigation across its action buttons.
func alertDialog(a alertAsk) ggui.Widget {
	answered := false
	finish := func(fn func()) {
		answered = true
		a.Open.Set(false)
		answered = false
		if fn != nil {
			fn()
		}
	}
	cancelBtn := ui.Button(cmp.Or(a.Cancel, T("Cancel")), func() { finish(a.OnCancel) }).Outline()
	confirmBtn := ui.Button(cmp.Or(a.Action, T("Continue")), func() { finish(a.Confirm) })
	if a.Destructive {
		confirmBtn.Destructive()
	}
	lines := []ggui.Widget{ui.Heading(a.Title).LineHeight(1.5)}
	if a.Body != "" {
		lines = append(lines, muted(widget.Text(a.Body)))
	}
	header := widget.Column(lines...).Gap(6).Align(widget.AlignStretch)
	actions := newDialogKeyNav(a.Open, widget.Row(cancelBtn, confirmBtn).Gap(8))
	return ui.Dialog(a.Open, header).
		Name(a.Title).
		Dismissible(false).
		CloseButton(false).
		OnClose(func() {
			if !answered && a.OnCancel != nil {
				a.OnCancel()
			}
		}).
		Footer(actions)
}

// dialogKeyNav wraps a dialog's content (or footer) inside its FocusTrap to
// track the enabled tab-stoppable controls painted each frame and step focus
// among them on unconsumed arrow keys.
type dialogKeyNav struct {
	open         ggui.Readable[bool]
	child        ggui.Widget
	raw          []ggui.KeyHandler
	stops        []ggui.KeyHandler
	focusedIdx   int
	pendingFocus ggui.KeyHandler
	pendingIdx   int
	wasOpen      bool
	primed       bool
}

func newDialogKeyNav(open ggui.Readable[bool], child ggui.Widget) *dialogKeyNav {
	return &dialogKeyNav{
		open:       open,
		child:      child,
		focusedIdx: -1,
		pendingIdx: -1,
	}
}

func (d *dialogKeyNav) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return d.child.Layout(c, env)
}

func (d *dialogKeyNav) Baseline() (float64, bool) { return ggui.BaselineOf(d.child) }

func (d *dialogKeyNav) Paint(dst *ggui.Canvas, r ggui.Rect) {
	open := d.open == nil || ggui.Untrack(d.open.Get)
	if !open {
		d.wasOpen = false
		d.primed = false
		d.pendingFocus = nil
		d.pendingIdx = -1
		d.focusedIdx = -1
		d.stops = d.stops[:0]
		dst.Paint(d.child, r)
		return
	}
	if !d.wasOpen {
		d.wasOpen = true
		d.primed = false
		d.pendingFocus = nil
		d.pendingIdx = -1
		d.focusedIdx = -1
	}

	mark := dst.HitMark()
	dst.Paint(d.child, r)

	// Collect unique enabled key handlers in paint order, matching ggui's
	// Tab traversal filtering for TabStopper widgets such as ui.Tree.
	d.raw = dst.KeysSince(mark, d.raw[:0])
	d.stops = d.stops[:0]
	d.focusedIdx = -1
	for _, h := range d.raw {
		if h == nil || d.stopIndex(h) >= 0 {
			continue
		}
		hasFocus := dst.HasFocus(h)
		if !hasFocus {
			if ts, ok := h.(ggui.TabStopper); ok && !ts.TabStop() {
				continue
			}
		}
		if hasFocus {
			d.focusedIdx = len(d.stops)
		}
		d.stops = append(d.stops, h)
	}

	// FocusTrap's initial focus enters without FocusVisible; reveal the ring
	// once on open so keyboard focus is immediately apparent.
	if !d.primed && d.focusedIdx >= 0 {
		d.primed = true
		if !dst.FocusVisible() && showFocusRing(d.stops[d.focusedIdx]) {
			ggui.RepaintAll()
		}
	}

	// Apply a pending arrow-key focus move requested during input dispatch.
	if d.pendingFocus != nil || d.pendingIdx >= 0 {
		target, idx := d.pendingFocus, d.pendingIdx
		d.pendingFocus, d.pendingIdx = nil, -1
		switch {
		case target != nil && d.stopIndex(target) >= 0:
			dst.RequestFocus(target)
		case idx >= 0 && idx < len(d.stops):
			dst.RequestFocus(d.stops[idx])
		}
	}

	// Register bare arrow shortcuts scoped to this dialog's FocusTrap.
	// Unmodified shortcuts run only when the focused control did not consume
	// the key, and only while this dialog is the topmost focus scope (so an
	// open Select dropdown or nested dialog takes precedence automatically).
	if len(d.stops) > 1 {
		dst.Shortcut(ggui.Chord{Key: ggui.KeyArrowLeft}, func() { d.step(-1) })
		dst.Shortcut(ggui.Chord{Key: ggui.KeyArrowRight}, func() { d.step(1) })
		dst.Shortcut(ggui.Chord{Key: ggui.KeyArrowUp}, func() { d.step(-1) })
		dst.Shortcut(ggui.Chord{Key: ggui.KeyArrowDown}, func() { d.step(1) })
	}
}

func (d *dialogKeyNav) stopIndex(h ggui.KeyHandler) int {
	for i, s := range d.stops {
		if sameHandler(s, h) {
			return i
		}
	}
	return -1
}

// currentFocusedIndex finds which stop currently holds keyboard focus,
// checking live control state first so the very first arrow key after open
// knows FocusTrap moved focus to stops[0].
func (d *dialogKeyNav) currentFocusedIndex() int {
	for i, s := range d.stops {
		if isHandlerFocused(s) {
			return i
		}
	}
	if d.focusedIdx >= 0 && d.focusedIdx < len(d.stops) {
		return d.focusedIdx
	}
	if !d.primed && len(d.stops) > 0 {
		return 0
	}
	return -1
}

func (d *dialogKeyNav) step(dir int) {
	n := len(d.stops)
	if n == 0 {
		return
	}
	cur := d.currentFocusedIndex()
	var next int
	switch {
	case cur >= 0:
		next = (cur + dir + n) % n
	case dir < 0:
		next = n - 1
	default:
		next = 0
	}
	d.pendingFocus = d.stops[next]
	d.pendingIdx = next
	d.primed = true
	ggui.RepaintAll()
}

func sameHandler(a, b ggui.KeyHandler) bool {
	if a == nil || b == nil {
		return a == b
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

func isHandlerFocused(h ggui.KeyHandler) bool {
	switch v := h.(type) {
	case interface{ Focused() bool }:
		return v.Focused()
	case *ui.ButtonWidget:
		return v.Focused
	case *ui.CheckboxWidget:
		return v.Focused
	case *ui.SwitchWidget:
		return v.Focused
	case *ui.SelectWidget[string]:
		return v.Focused
	}
	return false
}

func showFocusRing(h ggui.KeyHandler) bool {
	switch v := h.(type) {
	case *ui.ButtonWidget:
		if v.Focused && !v.FocusVisible {
			v.FocusVisible = true
			return true
		}
	case *ui.CheckboxWidget:
		if v.Focused && !v.FocusVisible {
			v.FocusVisible = true
			return true
		}
	case *ui.SwitchWidget:
		if v.Focused && !v.FocusVisible {
			v.FocusVisible = true
			return true
		}
	case *ui.SelectWidget[string]:
		if v.Focused && !v.FocusVisible {
			v.FocusVisible = true
			return true
		}
	}
	return false
}
