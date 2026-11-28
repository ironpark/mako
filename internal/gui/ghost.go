package gui

import (
	"image/color"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Ghost text input for the Chat composer: shows an agent CLI's inline prompt
// suggestion (Claude Code's post-turn prompt suggestion) as dimmed text
// inside the composer. When the draft is empty, the full suggestion replaces
// the default placeholder; while typing a matching prefix with the caret at
// the end, the remaining suffix appears right after it.
//
// Keys, with the caret at the end, no selection, and no slash or template
// menu open:
//   - Tab or →: accepts the full suggestion into the draft.
//   - Alt+→: accepts the next word of the suggestion.
//   - Esc: dismisses the current suggestion until the agent offers a new one.

// ghostSuffix returns the dimmed completion suffix to display for draft given
// suggestion, or "" when draft does not prefix-match suggestion. Matching is
// Unicode case-insensitive on rune boundaries so typing "run " continues
// "Run the tests" cleanly as "the tests".
func ghostSuffix(draft, suggestion string) string {
	suggestion = strings.TrimSpace(suggestion)
	if suggestion == "" {
		return ""
	}
	cut := matchPrefixBytes(draft, suggestion)
	if cut < 0 || cut >= len(suggestion) {
		return ""
	}
	return suggestion[cut:]
}

// matchPrefixBytes returns the byte offset in suggestion right after the runes
// that case-insensitively match draft, or -1 if draft is not a prefix.
func matchPrefixBytes(draft, suggestion string) int {
	di, si := 0, 0
	for di < len(draft) {
		if si >= len(suggestion) {
			return -1
		}
		dr, dsz := utf8.DecodeRuneInString(draft[di:])
		sr, ssz := utf8.DecodeRuneInString(suggestion[si:])
		if unicode.ToLower(dr) != unicode.ToLower(sr) {
			return -1
		}
		di += dsz
		si += ssz
	}
	return si
}

// nextGhostWord returns the leading whitespace and the next word of suffix,
// for word-by-word acceptance (Alt+→).
func nextGhostWord(suffix string) string {
	i := 0
	for i < len(suffix) {
		r, sz := utf8.DecodeRuneInString(suffix[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += sz
	}
	for i < len(suffix) {
		r, sz := utf8.DecodeRuneInString(suffix[i:])
		if unicode.IsSpace(r) {
			break
		}
		i += sz
	}
	return suffix[:i]
}

// ghostFor is the ghost suffix session id's composer shows now: none unless
// the session is attached and its agent rests at its prompt, for a
// suggestion the user dismissed, or while the slash command or template menu
// lists anything. It reads reactively, for the chrome that follows it.
func (m *model) ghostFor(id string, state *chatState) string {
	if !slices.Contains(m.Attached.Get(), id) {
		return ""
	}
	if s, ok := sessionIn(m.Sessions.Get(), id); !ok || !restsAtPrompt(s) {
		return ""
	}
	suggestion := state.suggestion.Get()
	if suggestion == "" || suggestion == state.dismissedSuggestion.Get() {
		return ""
	}
	if len(m.menuItems(id, state)) > 0 {
		return ""
	}
	return ghostSuffix(state.draft.Get(), suggestion)
}

// restsAtPrompt reports whether s waits at its prompt for the next message:
// running, neither at work nor compacting, asking nothing of the user in an
// approval, a dialog or a question, and signed in.
func restsAtPrompt(s *protocol.SessionInfo) bool {
	status := s.GetStatus()
	return s.GetState() == protocol.StateRunning && !protocol.AtWork(status) && status != protocol.AgentWaiting &&
		s.GetApproval() == nil && s.GetDialog() == nil && s.GetAuthError() == nil
}

// takesGhost reports whether input, the composer's editor, would take ghost
// text as it stands: its caret at the end, nothing selected, and no input
// method composing.
func takesGhost(input *widget.TextInputWidget) bool {
	st := input.EditingState()
	return !st.Composing && st.Anchor == st.Caret && st.Caret == len(st.Text)
}

// activeGhost is ghostFor read once, for keys and painting, and none while
// input, the composer's editor when there is one, composes with an input
// method, selects, or has its caret anywhere but at the end.
func (m *model) activeGhost(id string, state *chatState, input *widget.TextInputWidget) string {
	if input != nil && !takesGhost(input) {
		return ""
	}
	return ggui.Untrack(func() string { return m.ghostFor(id, state) })
}

// acceptGhost accepts the full active ghost suffix into session id's draft.
func (m *model) acceptGhost(id string, state *chatState, input *widget.TextInputWidget) bool {
	suffix := m.activeGhost(id, state, input)
	if suffix == "" {
		return false
	}
	m.takeGhost(id, input, suffix)
	state.dismissedSuggestion.Set("")
	return true
}

// takeGhost puts text, what is taken of the suggestion, after session id's
// draft: through input, the composer's editor, as typing does, so the caret
// goes after it and undo takes it back, or straight into the draft when
// there is no editor.
func (m *model) takeGhost(id string, input *widget.TextInputWidget, text string) {
	if input != nil {
		input.Replace(text)
		return
	}
	m.setComposerDraft(id, m.composerDraft(id)+text)
}

// acceptGhostWord accepts the next word of the active ghost suffix into draft.
func (m *model) acceptGhostWord(id string, state *chatState, input *widget.TextInputWidget) bool {
	suffix := m.activeGhost(id, state, input)
	if suffix == "" {
		return false
	}
	word := nextGhostWord(suffix)
	if word == "" {
		return false
	}
	m.takeGhost(id, input, word)
	return true
}

// dismissGhost hides the current suggestion until the agent offers a new one.
func (m *model) dismissGhost(state *chatState) bool {
	suggestion := ggui.Peek(state.suggestion)
	if suggestion == "" || suggestion == ggui.Peek(state.dismissedSuggestion) {
		return false
	}
	state.dismissedSuggestion.Set(suggestion)
	return true
}

// ghostClaims keeps Tab and Escape with the composer when ghost text is active
// so Tab accepts the suggestion rather than moving focus away.
func (m *model) ghostClaims(id string, state *chatState, input *widget.TextInputWidget, ev ggui.KeyEvent) bool {
	if !plainKey(ev) {
		return false
	}
	if ev.Key != ggui.KeyTab && ev.Key != ggui.KeyEscape {
		return false
	}
	return m.activeGhost(id, state, input) != ""
}

// ghostKey handles Tab, →, Alt+→, and Esc for ghost text suggestions.
func (m *model) ghostKey(id string, state *chatState, input *widget.TextInputWidget, ev ggui.KeyEvent) bool {
	if m.activeGhost(id, state, input) == "" {
		return false
	}
	switch {
	case plainKey(ev) && (ev.Key == ggui.KeyTab || ev.Key == ggui.KeyArrowRight):
		return m.acceptGhost(id, state, input)
	case ev.Key == ggui.KeyArrowRight && ev.Mods.Alt && !ev.Mods.Ctrl && !ev.Mods.Meta && !ev.Mods.Shift:
		return m.acceptGhostWord(id, state, input)
	case plainKey(ev) && ev.Key == ggui.KeyEscape:
		return m.dismissGhost(state)
	}
	return false
}

// ghostAcceptChip is a clickable "Tab" keycap hint in the composer bar while
// Tab would take ghost text, and takes no space otherwise. A click takes the
// suggestion as Tab does, and gives input, the composer's editor, the focus
// back with its caret after the text.
func ghostAcceptChip(m *model, id string, state *chatState, input *widget.TextInputWidget, disabled ggui.Readable[bool]) ggui.Widget {
	active := ggui.Derived(func() bool { return state.ghostCaret.Get() && m.ghostFor(id, state) != "" })
	return ggui.View(active, func(show bool) ggui.Widget {
		if !show {
			return widget.Box()
		}
		badge := widget.Row(
			widget.Box(widget.Text("Tab").Size(10.5).Color(skin.Muted).NoWrap()).
				Fill(skin.Surface2).Border(1, skin.Border).Radius(4).Pad(1, 5),
			widget.Text(T("Accept")).Size(11.5).Color(skin.ExtraMuted).NoWrap(),
		).Gap(5).Align(widget.AlignCenter)
		btn := ui.ButtonOf(badge, func() {
			if m.acceptGhost(id, state, input) {
				state.composerFocus.Focus()
			}
		}).Ghost().Pad(2, 5).Radius(6).Name(T("Accept suggestion")).BindDisabled(disabled)
		return ui.Tooltip(btn, T("Accept suggestion (Tab or →, Esc to dismiss)"))
	})
}

// ghostField wraps a composer TextFieldWidget and paints the active ghost text
// over the editor's text area: in place of the placeholder while the draft is
// empty, or right after the draft while the draft prefix-matches the
// suggestion.
type ghostField struct {
	m                  *model
	id                 string
	state              *chatState
	child              ggui.Widget
	field              *ui.TextFieldWidget
	input              *widget.TextInputWidget
	defaultPlaceholder string
	lastPlaceholder    string
	env                ggui.Env
	// ghost is the paragraph painted over the editor, kept between frames
	// so a repaint that changed no text shapes nothing again.
	ghost *widget.RichTextWidget
}

func newGhostField(m *model, id string, state *chatState, field *ui.TextFieldWidget, child ggui.Widget, defaultPlaceholder string) *ghostField {
	return &ghostField{
		m:                  m,
		id:                 id,
		state:              state,
		child:              child,
		field:              field,
		input:              field.Input(),
		defaultPlaceholder: defaultPlaceholder,
		lastPlaceholder:    defaultPlaceholder,
		ghost:              widget.RichText(),
	}
}

func (g *ghostField) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.env = env
	// Read reactively, so the field lays out and repaints as a suggestion
	// arrives, changes, or clears.
	suffix := g.m.ghostFor(g.id, g.state)
	wantPlaceholder := g.defaultPlaceholder
	if suffix != "" && ggui.Peek(g.state.draft) == "" {
		wantPlaceholder = ""
	}
	if wantPlaceholder != g.lastPlaceholder {
		g.lastPlaceholder = wantPlaceholder
		g.field.Placeholder(wantPlaceholder)
	}
	return g.child.Layout(c, env)
}

func (g *ghostField) Baseline() (float64, bool) { return ggui.BaselineOf(g.child) }

// ghostBreak is a zero-width space: a line may break after it. Put between
// the draft and the rest of the suggestion, it lets the paragraph break
// where the draft ends, as the editor, which wraps the draft alone, does
// when a word the suggestion finishes would not fit on its line.
const ghostBreak = "\u200b"

// Paint draws the field, then the draft again in no color followed by the
// rest of the suggestion, as one paragraph at the editor's width. The
// paragraph wraps as the editor does, so the rest starts where the draft
// ends and runs on to the rows below, as far as the field shows them.
func (g *ghostField) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(g.child, r)
	// The chip follows whether the editor would take the suggestion, which
	// a click or an arrow key changes without a write to any state.
	g.state.ghostCaret.Set(takesGhost(g.input))

	suffix := g.m.activeGhost(g.id, g.state, g.input)
	if suffix == "" || dst == nil {
		return
	}
	t := uitheme.Of(g.env)
	padTop, padX := t.Space, t.FieldPad.Left
	textW := max(r.Size.W-2*padX, 1)
	textH := max(r.Size.H-2*padTop, 1)
	at := ggui.Rct(ggui.Pt(r.Origin.X+padX, r.Origin.Y+padTop), ggui.Sz(textW, textH))
	g.ghost.Spans(
		widget.Span(g.input.EditingState().Text+ghostBreak).Color(color.Transparent),
		widget.Span(suffix).Color(skin.ExtraMuted),
	)
	sz := g.ghost.Layout(ggui.Constraints{MaxW: textW, MaxH: ggui.Unbounded}, g.env)
	// The paragraph sets its first line in the leading its line height
	// adds, the editor its first line at its top: the two baselines meet.
	origin := at.Origin
	if gb, ok := g.ghost.Baseline(); ok {
		if ib, ok := g.input.Baseline(); ok {
			origin.Y -= gb - ib
		}
	}
	dst.Clip(at).Paint(g.ghost, ggui.Rct(origin, sz))
}
