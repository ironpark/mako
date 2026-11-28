package gui

import (
	"image/color"
	"maps"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"github.com/ironpark/ggwd"

	"github.com/ironpark/mako/internal/gui/skin"
)

// shortcutSpec defines one customizable window shortcut: its stable ID for
// prefs, its built-in default chord, and its English label for settings and
// i18n.
type shortcutSpec struct {
	ID     string
	Chord  string
	Action string
}

// shortcutList is what the settings page shows under Shortcuts and what
// bindShortcuts registers.
var shortcutList = []shortcutSpec{
	{ID: "new_session", Chord: "cmd+t", Action: "New session"},
	{ID: "close_tab", Chord: "cmd+w", Action: "Close tab and session"},
	{ID: "save_file", Chord: "cmd+s", Action: "Save file"},
	{ID: "command_palette", Chord: "cmd+k", Action: "Command palette"},
	{ID: "toggle_preview", Chord: "cmd+shift+v", Action: "Toggle Markdown preview"},
	{ID: "toggle_chat", Chord: "cmd+shift+j", Action: "Toggle chat and terminal"},
	{ID: "next_waiting", Chord: "cmd+shift+a", Action: "Next waiting session"},
	{ID: "allow_approval", Chord: "cmd+enter", Action: "Allow the approval asked for"},
	{ID: "deny_approval", Chord: "cmd+.", Action: "Deny the approval asked for"},
	{ID: "toggle_sidebar", Chord: "cmd+b", Action: "Toggle sidebar"},
	{ID: "toggle_details", Chord: "cmd+shift+d", Action: "Toggle details panel"},
	{ID: "open_settings", Chord: "cmd+,", Action: "Open settings"},
	{ID: "quick_open", Chord: "cmd+p", Action: "Go to file"},
	{ID: "search_project", Chord: "cmd+shift+f", Action: "Search the project"},
	{ID: "find_in_session", Chord: "cmd+f", Action: "Find in the session"},
	{ID: "next_diff", Chord: "alt+cmd+down", Action: "Next changed file"},
	{ID: "prev_diff", Chord: "alt+cmd+up", Action: "Previous changed file"},
	{ID: "open_inbox", Chord: "cmd+shift+i", Action: "Open Needs You inbox"},
}

// secondaryPaletteChord keeps Cmd+Shift+P opening the command palette unless
// the user customized the palette shortcut or bound Cmd+Shift+P elsewhere.
const secondaryPaletteChord = "cmd+shift+p"

type shortcutState struct {
	host         ggui.Host
	handles      []*ggui.ShortcutHandle
	registered   map[string]string
	escapeHandle *ggui.ShortcutHandle

	recording     *ggui.StateValue[string] // action ID being recorded, or ""
	recordingHint *ggui.StateValue[string] // validation note while recording
}

var (
	shortcutStatesMu sync.Mutex
	shortcutStates   = map[*model]*shortcutState{}
)

func shortcutStateOf(m *model) *shortcutState {
	shortcutStatesMu.Lock()
	defer shortcutStatesMu.Unlock()
	st := shortcutStates[m]
	if st == nil {
		st = &shortcutState{
			recording:     ggui.State(""),
			recordingHint: ggui.State(""),
		}
		shortcutStates[m] = st
	}
	return st
}

func (m *model) cleanupShortcuts() {
	shortcutStatesMu.Lock()
	st := shortcutStates[m]
	delete(shortcutStates, m)
	shortcutStatesMu.Unlock()
	if st == nil {
		return
	}
	for _, h := range st.handles {
		h.Remove()
	}
	st.handles = nil
	if st.escapeHandle != nil {
		st.escapeHandle.Remove()
		st.escapeHandle = nil
	}
}

func shortcutByID(id string) (shortcutSpec, bool) {
	for _, s := range shortcutList {
		if s.ID == id {
			return s, true
		}
	}
	return shortcutSpec{}, false
}

// shortcutChordOf returns the effective chord for action id under p.
func shortcutChordOf(p prefs, id string) string {
	if custom := p.Shortcuts[id]; custom != "" {
		if canon, ok := canonicalShortcutChord(custom); ok {
			return canon
		}
	}
	if spec, ok := shortcutByID(id); ok {
		return spec.Chord
	}
	return ""
}

// shortcutChord returns the model's current effective chord for action id.
func (m *model) shortcutChord(id string) string {
	return shortcutChordOf(ggui.Peek(m.Prefs), id)
}

// setShortcut persists a custom chord for action id and updates live bindings
// immediately. Setting an action back to its default clears the override.
func (m *model) setShortcut(id, chord string) bool {
	spec, ok := shortcutByID(id)
	if !ok {
		return false
	}
	canon, ok := canonicalShortcutChord(chord)
	if !ok {
		return false
	}
	m.Prefs.Update(func(p prefs) prefs {
		next := maps.Clone(p.Shortcuts)
		if sameShortcutChord(canon, spec.Chord) {
			delete(next, id)
		} else {
			if next == nil {
				next = map[string]string{}
			}
			next[id] = canon
		}
		if len(next) == 0 {
			next = nil
		}
		p.Shortcuts = next
		return p.normalized()
	})
	m.syncShortcuts()
	return true
}

// resetShortcut restores action id to its built-in default chord.
func (m *model) resetShortcut(id string) {
	if _, ok := shortcutByID(id); !ok {
		return
	}
	m.Prefs.Update(func(p prefs) prefs {
		if len(p.Shortcuts) == 0 {
			return p
		}
		next := maps.Clone(p.Shortcuts)
		delete(next, id)
		if len(next) == 0 {
			next = nil
		}
		p.Shortcuts = next
		return p.normalized()
	})
	m.syncShortcuts()
}

// resetAllShortcuts restores every shortcut to its built-in default.
func (m *model) resetAllShortcuts() {
	m.stopRecordingShortcut()
	m.Prefs.Update(func(p prefs) prefs {
		p.Shortcuts = nil
		return p.normalized()
	})
	m.syncShortcuts()
}

func (m *model) isRecordingShortcut() bool {
	shortcutStatesMu.Lock()
	st := shortcutStates[m]
	shortcutStatesMu.Unlock()
	return st != nil && ggui.Peek(st.recording) != ""
}

func (m *model) stopRecordingShortcut() {
	shortcutStatesMu.Lock()
	st := shortcutStates[m]
	shortcutStatesMu.Unlock()
	if st == nil {
		return
	}
	st.recording.Set("")
	st.recordingHint.Set("")
}

func (st *shortcutState) startRecording(id string) {
	st.recordingHint.Set("")
	st.recording.Set(id)
}

func (st *shortcutState) cancelRecording(id string) {
	if ggui.Peek(st.recording) == id {
		st.recording.Set("")
		st.recordingHint.Set("")
	}
}

// bindShortcuts installs the customizable window shortcuts and the
// Escape-from-Settings handler on host.
func bindShortcuts(host ggui.Host, m *model) {
	st := shortcutStateOf(m)
	st.host = host
	st.registered = nil
	if st.escapeHandle != nil {
		st.escapeHandle.Remove()
	}
	st.escapeHandle = host.Shortcut("escape", func() {
		m.handleEscape(host)
	})
	m.syncShortcuts()
}

// syncShortcuts updates host.Shortcut registrations to match the current prefs.
func (m *model) syncShortcuts() {
	st := shortcutStateOf(m)
	if st.host == nil {
		return
	}
	p := ggui.Peek(m.Prefs)
	target := make(map[string]string, len(shortcutList)+1)
	secondaryTaken := false
	for _, spec := range shortcutList {
		chord := shortcutChordOf(p, spec.ID)
		target[spec.ID] = chord
		if sameShortcutChord(chord, secondaryPaletteChord) {
			secondaryTaken = true
		}
	}
	if !secondaryTaken && sameShortcutChord(target["command_palette"], "cmd+k") {
		target["command_palette_alt"] = secondaryPaletteChord
	}
	if maps.Equal(st.registered, target) {
		return
	}
	for _, h := range st.handles {
		h.Remove()
	}
	st.handles = nil
	for _, spec := range shortcutList {
		action := m.shortcutAction(spec.ID)
		if action == nil {
			continue
		}
		chord := target[spec.ID]
		if _, err := ggui.ParseChord(chord); err != nil {
			continue
		}
		fn := action
		st.handles = append(st.handles, st.host.Shortcut(chord, func() {
			if m.isRecordingShortcut() {
				return
			}
			fn()
		}))
	}
	if alt, ok := target["command_palette_alt"]; ok {
		st.handles = append(st.handles, st.host.Shortcut(alt, func() {
			if m.isRecordingShortcut() {
				return
			}
			m.openPalette()
		}))
	}
	st.registered = target
}

func (m *model) shortcutAction(id string) func() {
	switch id {
	case "new_session":
		return func() { m.openNew("", "") }
	case "close_tab":
		return func() {
			if id := ggui.Peek(m.Selected); id != "" {
				m.askKill(id)
			}
		}
	case "save_file":
		return m.saveSelected
	case "command_palette":
		return m.openPalette
	case "toggle_preview":
		return m.toggleSelectedPreview
	case "toggle_chat":
		return m.toggleChat
	case "open_inbox":
		return m.toggleInbox
	case "next_waiting":
		return m.nextWaiting
	case "allow_approval":
		return func() { m.answerSelected("allow") }
	case "deny_approval":
		return func() { m.answerSelected("deny") }
	case "toggle_sidebar":
		return func() { ggui.Toggle(m.SidebarOpen) }
	case "toggle_details":
		return func() { ggui.Toggle(m.DetailsOpen) }
	case "open_settings":
		return func() { m.openSettings("") }
	case "quick_open":
		return m.openQuickOpen
	case "search_project":
		return m.openSearch
	case "find_in_session":
		return m.openFind
	case "next_diff":
		return func() { m.stepDiff(1) }
	case "prev_diff":
		return func() { m.stepDiff(-1) }
	default:
		return nil
	}
}

func (m *model) closeOpenGraphCtxPopup() bool {
	return m.closeOpenGraphCtxPopupIn(nil)
}

func (m *model) closeOpenGraphCtxPopupIn(sw *spaceWindowState) bool {
	if m == nil || m.plugins == nil {
		return false
	}
	closed := false
	for _, cached := range m.plugins.blockCache {
		if cached == nil || cached.form == nil {
			continue
		}
		for _, st := range cached.form.graphs {
			cs := st.peekCtxState(sw)
			if cs == nil {
				continue
			}
			if cs.popup != nil && cs.popup.IsOpen() {
				cs.popup.Hide()
				closed = true
			} else if cs.open != nil && ggui.Peek(cs.open) {
				cs.open.Set(false)
				closed = true
			}
		}
	}
	return closed
}

// handleEscape returns from Settings or an active sidebar plugin page to the
// workspace when Escape is pressed and no dialog, text input, dropdown/menu, or
// shortcut recorder owns Escape.
func (m *model) handleEscape(host ggui.Host) {
	if m.isRecordingShortcut() {
		return
	}
	if m.closeOpenGraphCtxPopup() {
		return
	}
	if host != nil {
		if sem := host.Semantics(); sem != nil && escapeCapturedInSemantics(sem) {
			return
		}
	}
	if ggui.Peek(m.Page) == "settings" {
		m.Page.Set("workspace")
		return
	}
	if ggui.Peek(m.Page) == "workspace" && m.SidebarPage != nil && ggui.Peek(m.SidebarPage) != "" {
		m.clearSidebarPage()
	}
}

// escapeCapturedInSemantics reports whether the last painted accessibility tree
// has an open dialog/popover, an open dropdown/menu, or a focused text/number
// input that should keep Escape from leaving Settings.
func escapeCapturedInSemantics(sem *ggui.SemTree) bool {
	if _, ok := sem.Find(ggui.RoleDialog, ""); ok {
		return true
	}
	for _, n := range sem.All() {
		switch n.Role {
		case ggui.RoleSelect, ggui.RoleMenu, ggui.RoleCombobox, ggui.RoleMenuItem:
			if n.Expanded.Open() {
				return true
			}
		case ggui.RoleOption:
			return true
		}
	}
	if focused, ok := sem.Focused(); ok {
		switch focused.Role {
		case ggui.RoleTextField, ggui.RoleSpinButton, ggui.RoleCombobox:
			return true
		}
	}
	return false
}

// Chord normalization & validation -------------------------------------------

func normalizeShortcuts(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	var out map[string]string
	for id, raw := range in {
		spec, ok := shortcutByID(id)
		if !ok {
			continue
		}
		canon, ok := canonicalShortcutChord(raw)
		if !ok || sameShortcutChord(canon, spec.Chord) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[id] = canon
	}
	return out
}

func canonicalShortcutChord(raw string) (string, bool) {
	ch, err := ggui.ParseChord(raw)
	if err != nil {
		return "", false
	}
	if !ch.Modified() && !isFunctionKey(ch.Key) {
		return "", false
	}
	return ch.String(), true
}

func sameShortcutChord(a, b string) bool {
	ca, errA := ggui.ParseChord(a)
	cb, errB := ggui.ParseChord(b)
	if errA != nil || errB != nil {
		return false
	}
	wa, wb := ggwd.Chord(ca), ggwd.Chord(cb)
	return wa.Key == wb.Key && wa.Held() == wb.Held()
}

func isModifierKey(k ggui.KeyboardKey) bool {
	switch k {
	case ggui.KeyShift, ggui.KeyShiftLeft, ggui.KeyShiftRight,
		ggui.KeyControl, ggui.KeyControlLeft, ggui.KeyControlRight,
		ggui.KeyAlt, ggui.KeyAltLeft, ggui.KeyAltRight,
		ggui.KeyMeta, ggui.KeyMetaLeft, ggui.KeyMetaRight,
		ggui.KeyCapsLock, ggui.KeyNumLock, ggui.KeyScrollLock:
		return true
	}
	return false
}

func isFunctionKey(k ggui.KeyboardKey) bool {
	return k >= ggui.KeyF1 && k <= ggui.KeyF24
}

// chordFromKeyEvent turns a captured KeyPress into a canonical chord string.
// It rejects modifier-only presses and bare non-function keys.
func chordFromKeyEvent(ev ggui.KeyEvent) (string, bool) {
	if ev.Kind != ggui.KeyPress || isModifierKey(ev.Key) {
		return "", false
	}
	if !ev.Mods.Ctrl && !ev.Mods.Alt && !ev.Mods.Meta && !isFunctionKey(ev.Key) {
		return "", false
	}
	mods := ev.Mods
	cmd := false
	if goruntime.GOOS == "darwin" {
		if mods.Meta {
			cmd = true
			mods.Meta = false
		}
	} else {
		if mods.Ctrl {
			cmd = true
			mods.Ctrl = false
		}
	}
	c := ggwd.Chord{Key: ev.Key, Mods: mods, Cmd: cmd}
	return canonicalShortcutChord(c.String())
}

// shortcutConflicts returns, for every action ID whose effective chord is
// shared by at least one other action, the other conflicting action IDs.
func shortcutConflicts(p prefs) map[string][]string {
	type binding struct {
		id    string
		chord string
	}
	bindings := make([]binding, len(shortcutList))
	for i, s := range shortcutList {
		bindings[i] = binding{id: s.ID, chord: shortcutChordOf(p, s.ID)}
	}
	var out map[string][]string
	for i, a := range bindings {
		for j, b := range bindings {
			if i == j {
				continue
			}
			if sameShortcutChord(a.chord, b.chord) {
				if out == nil {
					out = map[string][]string{}
				}
				out[a.id] = append(out[a.id], b.id)
			}
		}
	}
	return out
}

// Settings UI ----------------------------------------------------------------

func shortcutsSettings(m *model) ggui.Widget {
	st := shortcutStateOf(m)
	type viewState struct {
		prefs     prefs
		recording string
		hint      string
	}
	state := ggui.Derived(func() viewState {
		return viewState{
			prefs:     m.Prefs.Get(),
			recording: st.recording.Get(),
			hint:      st.recordingHint.Get(),
		}
	})

	windowSection := ggui.View(state, func(vs viewState) ggui.Widget {
		conflicts := shortcutConflicts(vs.prefs)
		rows := make([]ggui.Widget, 0, len(shortcutList))
		for _, spec := range shortcutList {
			effective := shortcutChordOf(vs.prefs, spec.ID)
			recording := vs.recording == spec.ID
			customized := !sameShortcutChord(effective, spec.Chord)
			otherIDs := conflicts[spec.ID]
			rows = append(rows, shortcutRow(m, st, spec, effective, recording, customized, otherIDs, vs.hint))
		}

		var children []ggui.Widget
		resetAll := ui.Button(T("Reset all to defaults"), m.resetAllShortcuts).Outline().Disabled(len(vs.prefs.Shortcuts) == 0)
		children = append(children, widget.Row(widget.Spacer(), resetAll))
		if len(conflicts) > 0 {
			banner := widget.Box(widget.Row(
				iconColored("shield-alert", skin.DotWaiting).Size(15),
				widget.Expanded(widget.Text(T("Some shortcuts use the same key combination. Change one so both can be triggered separately.")).Size(12.5).Color(skin.DotWaiting)),
			).Gap(10).Align(widget.AlignCenter)).Pad(10, 14).Fill(skin.Surface1).Border(1, skin.DotWaiting).Radius(8)
			children = append(children, banner)
		}
		children = append(children, card(rows...))
		return section(T("Window"), children...)
	})

	chord := func(c string) ggui.Widget { return ui.Kbd(chordLabel(c)) }
	term := []ggui.Widget{
		row(T("Copy selection"), "", chord(termChord("c"))),
		row(T("Paste"), T("Bracketed when the program asks for it."), chord(termChord("v"))),
		row(T("Select all"), T("The whole scrollback."), chord(termChord("a"))),
		row(T("Select a word, a line"), T("Double-click, triple-click. Alt+drag selects a block."), ui.Kbd("2× / 3×")),
		row(T("Select in a mouse program"), T("Hold Shift to select where the program has the mouse."), ui.Kbd("⇧ drag")),
	}
	termSection := section(T("Terminal"), card(term...))
	return widePage(
		T("Shortcuts"),
		T("Keys the window keeps. Click a shortcut to change it. Every other key, Tab and the Ctrl chords included, goes to the terminal."),
		twoColumns([]ggui.Widget{windowSection, termSection}, []int{0}, []int{1}),
	)
}

func shortcutRow(m *model, st *shortcutState, spec shortcutSpec, chord string, recording, customized bool, conflictIDs []string, hint string) ggui.Widget {
	text := []ggui.Widget{widget.Text(T(spec.Action)).Size(13.5)}
	switch {
	case recording && hint != "":
		text = append(text, widget.Text(hint).Size(12).Color(skin.DotWaiting))
	case recording:
		text = append(text, widget.Text(T("Press a key combination · Esc to cancel · Backspace to reset")).Size(12).Color(skin.AccentBright))
	case len(conflictIDs) > 0:
		names := make([]string, 0, len(conflictIDs))
		for _, id := range conflictIDs {
			if other, ok := shortcutByID(id); ok {
				names = append(names, T(other.Action))
			}
		}
		text = append(text, widget.Text(Tf("Conflicts with %s", strings.Join(names, ", "))).Size(12).Color(skin.DotWaiting))
	case customized:
		text = append(text, muted(widget.Text(Tf("Default: %s", chordLabel(spec.Chord))).Size(12)))
	}

	var controls []ggui.Widget
	if customized {
		id := spec.ID
		controls = append(controls,
			ui.Button(T("Reset"), func() { m.resetShortcut(id) }).Ghost().Pad(4, 8).Radius(6).Name(Tf("Reset %s", T(spec.Action))),
		)
	}
	controls = append(controls, newShortcutRecorder(m, st, spec, chord, recording, len(conflictIDs) > 0))
	return widget.Box(widget.Row(
		widget.Expanded(widget.Column(text...).Gap(3).Stretch()),
		widget.Row(controls...).Gap(8).Align(widget.AlignCenter),
	).Gap(24).Align(widget.AlignCenter)).Pad(14, 16)
}

// termChord is the terminal's clipboard chord for a letter, as isCopy and
// the others match it.
func termChord(letter string) string {
	if goruntime.GOOS == "darwin" {
		return "cmd+" + letter
	}
	return "ctrl+shift+" + letter
}

// shortcutRecorder widget ----------------------------------------------------

type shortcutRecorderID string

// shortcutRecorder is a clickable key badge that enters key-capture mode when
// activated, claiming all chords until a valid shortcut, Escape, or Backspace
// is pressed.
type shortcutRecorder struct {
	ggui.Interactive

	m        *model
	st       *shortcutState
	spec     shortcutSpec
	chord    string
	conflict bool
	consumed bool
	box      *widget.BoxWidget
}

func newShortcutRecorder(m *model, st *shortcutState, spec shortcutSpec, chord string, recording, conflict bool) *shortcutRecorder {
	r := &shortcutRecorder{
		m:        m,
		st:       st,
		spec:     spec,
		chord:    chord,
		conflict: conflict,
	}
	r.Role = ggui.RoleButton
	r.SetKey(shortcutRecorderID(spec.ID))
	r.SetName(Tf("Shortcut for %s", T(spec.Action)))

	var inner ggui.Widget
	var fill, border color.Color
	if recording {
		inner = widget.Text(T("Press keys…")).Size(12).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap()
		fill = skin.Surface2
		border = skin.AccentBright
	} else {
		inner = ui.Kbd(chordLabel(chord))
		fill = color.Transparent
		border = skin.Border
		if conflict {
			border = skin.DotWaiting
		}
	}
	r.box = widget.Box(inner).Pad(4, 8).Radius(6).Fill(fill).Border(1, border)
	return r
}

func (r *shortcutRecorder) isRecording() bool {
	return ggui.Peek(r.st.recording) == r.spec.ID
}

func (r *shortcutRecorder) Adopt(prev any) {
	r.Interactive.Adopt(prev)
	if q, ok := prev.(*shortcutRecorder); ok {
		r.consumed = q.consumed
	}
}

func (r *shortcutRecorder) Describe() ggui.Node {
	val := chordLabel(r.chord)
	if r.isRecording() {
		val = T("Press keys…")
	}
	return ggui.Node{
		Role:     ggui.RoleButton,
		Name:     r.SemanticName(),
		Value:    val,
		Selected: r.isRecording(),
		Actions:  ggui.ActionPress | ggui.ActionFocus,
	}
}

func (r *shortcutRecorder) Act(a ggui.Action) bool {
	if a.Kind == ggui.ActionPress {
		r.toggleRecording()
		return true
	}
	return false
}

func (r *shortcutRecorder) toggleRecording() {
	if r.isRecording() {
		r.st.cancelRecording(r.spec.ID)
	} else {
		r.st.startRecording(r.spec.ID)
	}
}

func (r *shortcutRecorder) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return r.box.Layout(c, env)
}

func (r *shortcutRecorder) Paint(dst *ggui.Canvas, rect ggui.Rect) {
	if !r.isRecording() {
		if r.Hovered {
			r.box.Fill(skin.Surface2)
		} else {
			r.box.Fill(color.Transparent)
		}
	}
	r.Hit(dst, rect, r, ggui.CursorShapePointer)
	if r.isRecording() && !r.Focused {
		dst.RequestFocus(r)
	}
	dst.Paint(r.box, rect)
	if r.FocusVisible && !r.isRecording() {
		r.FocusRing(dst, rect, 6, skin.AccentBright)
	}
}

func (r *shortcutRecorder) HandlePointer(ev ggui.PointerEvent) bool {
	return r.Pointer(ev, r.toggleRecording)
}

// ClaimsChord implements ggui.ChordClaimer: while recording, every key press
// (including Tab and existing global shortcuts) is delivered here instead of
// running shortcuts or moving focus.
func (r *shortcutRecorder) ClaimsChord(ev ggui.KeyEvent) bool {
	return ev.Kind == ggui.KeyPress && r.isRecording()
}

// ConsumesKey implements ggui.KeyConsumer: while recording (or on the Escape
// press that just cancelled recording), keep the key from bubbling up to
// Settings' Escape handler.
func (r *shortcutRecorder) ConsumesKey(ev ggui.KeyEvent) bool {
	if ev.Kind != ggui.KeyPress {
		return false
	}
	return r.consumed || r.isRecording() || ggui.Activates(ev)
}

func (r *shortcutRecorder) HandleKey(ev ggui.KeyEvent) {
	switch ev.Kind {
	case ggui.KeyFocus:
		r.Focused = true
		return
	case ggui.KeyBlur:
		r.Focused = false
		r.FocusVisible = false
		r.consumed = false
		r.st.cancelRecording(r.spec.ID)
		return
	case ggui.KeyPress:
		r.consumed = false
		if !r.isRecording() {
			if ggui.Activates(ev) {
				r.consumed = true
				r.st.startRecording(r.spec.ID)
			}
			return
		}
		r.consumed = true
		if isModifierKey(ev.Key) {
			return
		}
		if ev.Key == ggui.KeyEscape && ev.Mods == (ggui.Mods{}) {
			r.st.cancelRecording(r.spec.ID)
			return
		}
		if (ev.Key == ggui.KeyBackspace || ev.Key == ggui.KeyDelete) && ev.Mods == (ggui.Mods{}) {
			r.m.resetShortcut(r.spec.ID)
			r.st.cancelRecording(r.spec.ID)
			return
		}
		chord, ok := chordFromKeyEvent(ev)
		if !ok {
			r.st.recordingHint.Set(T("Use a modifier key (Cmd, Ctrl, Alt) or a function key."))
			return
		}
		r.m.setShortcut(r.spec.ID, chord)
		r.st.cancelRecording(r.spec.ID)
	}
}
