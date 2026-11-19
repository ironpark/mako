package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
)

func newShortcutsTestModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1", Name: "test", Agents: agentspec.Defaults()}, "")
	t.Cleanup(func() { m.prefsWriter.wait() })
	return m
}

func newShortcutsProbe(t *testing.T, m *model) *ggui.Probe {
	t.Helper()
	p := ggui.ProbeBuilder(func() ggui.Widget { return window(m) }, ggui.Sz(1280, 800))
	p.Setup(func() { setup(m) })
	shortcuts(p, m)
	p.Frame()
	t.Cleanup(p.Close)
	return p
}

func TestShortcutsPrefsPersistenceAndNormalize(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())

	raw := prefs{
		Shortcuts: map[string]string{
			"toggle_sidebar": "cmd+shift+b", // valid custom
			"new_session":    "cmd+t",       // equals default -> dropped
			"open_settings":  "cmd+comma",   // alias of default "cmd+," -> dropped
			"save_file":      "a",           // bare non-function key -> dropped
			"quick_open":     "f5",          // function key -> valid
			"unknown_action": "cmd+y",       // unknown ID -> dropped
			"close_tab":      "not+a+key",   // invalid chord -> dropped
		},
	}.normalized()

	if len(raw.Shortcuts) != 2 {
		t.Fatalf("normalized shortcuts = %#v, want 2 entries", raw.Shortcuts)
	}
	if !sameShortcutChord(raw.Shortcuts["toggle_sidebar"], "cmd+shift+b") {
		t.Fatalf("toggle_sidebar = %q, want cmd+shift+b", raw.Shortcuts["toggle_sidebar"])
	}
	if !sameShortcutChord(raw.Shortcuts["quick_open"], "f5") {
		t.Fatalf("quick_open = %q, want f5", raw.Shortcuts["quick_open"])
	}

	if err := savePrefs(raw); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	loaded := loadPrefs()
	if !sameShortcutChord(loaded.Shortcuts["toggle_sidebar"], "cmd+shift+b") || !sameShortcutChord(loaded.Shortcuts["quick_open"], "f5") {
		t.Fatalf("loaded shortcuts = %#v", loaded.Shortcuts)
	}

	// Clearing all custom bindings leaves Shortcuts nil (omitted in JSON).
	cleared := loaded
	cleared.Shortcuts = map[string]string{"toggle_sidebar": "cmd+b"}
	cleared = cleared.normalized()
	if cleared.Shortcuts != nil {
		t.Fatalf("expected nil Shortcuts when all default, got %#v", cleared.Shortcuts)
	}
	body, err := json.Marshal(cleared)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gui.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestShortcutsRebindAndImmediateDispatch(t *testing.T) {
	m := newShortcutsTestModel(t)
	p := newShortcutsProbe(t, m)

	// Default binding cmd+b toggles the sidebar.
	wasOpen := ggui.Peek(m.SidebarOpen)
	p.Key("cmd+b")
	if ggui.Peek(m.SidebarOpen) == wasOpen {
		t.Fatal("default cmd+b did not toggle SidebarOpen")
	}

	// Rebind toggle_sidebar to cmd+shift+b; change takes effect immediately.
	if !m.setShortcut("toggle_sidebar", "cmd+shift+b") {
		t.Fatal("setShortcut failed")
	}
	p.Frame()

	before := ggui.Peek(m.SidebarOpen)
	p.Key("cmd+b")
	if ggui.Peek(m.SidebarOpen) != before {
		t.Fatal("old chord cmd+b still triggered toggle_sidebar after rebinding")
	}
	p.Key("cmd+shift+b")
	if ggui.Peek(m.SidebarOpen) == before {
		t.Fatal("rebound chord cmd+shift+b did not trigger toggle_sidebar")
	}

	// Command palette lists the customized shortcut.
	idx := slices.IndexFunc(m.commands(), func(c command) bool { return c.ID == "Toggle sidebar" })
	if idx < 0 || !sameShortcutChord(m.commands()[idx].Shortcut, "cmd+shift+b") {
		t.Fatalf("palette Toggle sidebar shortcut = %q, want cmd+shift+b", m.commands()[idx].Shortcut)
	}

	// Secondary palette chord cmd+shift+p works while palette is default, and
	// yields if another action binds cmd+shift+p.
	p.Key("cmd+shift+p")
	if !ggui.Peek(m.pickers.PaletteOpen) {
		t.Fatal("secondary cmd+shift+p did not open command palette")
	}
	m.pickers.PaletteOpen.Set(false)
	p.Frame()

	m.setShortcut("toggle_sidebar", "cmd+shift+p")
	p.Frame()
	before = ggui.Peek(m.SidebarOpen)
	p.Key("cmd+shift+p")
	if ggui.Peek(m.pickers.PaletteOpen) {
		t.Fatal("cmd+shift+p still opened palette after another action claimed it")
	}
	if ggui.Peek(m.SidebarOpen) == before {
		t.Fatal("cmd+shift+p did not trigger rebound toggle_sidebar")
	}

	// Resetting restores cmd+b immediately.
	m.resetShortcut("toggle_sidebar")
	p.Frame()
	before = ggui.Peek(m.SidebarOpen)
	p.Key("cmd+b")
	if ggui.Peek(m.SidebarOpen) == before {
		t.Fatal("resetShortcut did not restore default cmd+b")
	}
}

func TestShortcutsRecorderUI(t *testing.T) {
	m := newShortcutsTestModel(t)
	m.Page.Set("settings")
	m.Section.Set("shortcuts")
	p := newShortcutsProbe(t, m)

	// Start recording Toggle sidebar shortcut.
	p.Tap(Tf("Shortcut for %s", T("Toggle sidebar")))
	if !m.isRecordingShortcut() {
		t.Fatal("clicking shortcut button did not enter recording mode")
	}

	// Pressing a bare non-function key keeps recording active with a hint.
	p.Type(ggui.Mods{}, ggui.KeyA)
	if !m.isRecordingShortcut() {
		t.Fatal("bare key a should not finish recording")
	}
	if !sameShortcutChord(m.shortcutChord("toggle_sidebar"), "cmd+b") {
		t.Fatalf("bare key changed shortcut to %q", m.shortcutChord("toggle_sidebar"))
	}

	// Escape cancels recording and does NOT leave Settings.
	p.Key("escape")
	if m.isRecordingShortcut() {
		t.Fatal("Escape did not cancel recording")
	}
	if ggui.Peek(m.Page) != "settings" {
		t.Fatalf("Escape while recording left settings: page = %q", ggui.Peek(m.Page))
	}

	// Record a new chord that collides with Save file (cmd+s): should capture
	// cmd+s without firing save, update the binding, and report conflict.
	p.Tap(Tf("Shortcut for %s", T("Toggle sidebar")))
	p.Key("cmd+s")
	if m.isRecordingShortcut() {
		t.Fatal("valid chord cmd+s did not finish recording")
	}
	if !sameShortcutChord(m.shortcutChord("toggle_sidebar"), "cmd+s") {
		t.Fatalf("toggle_sidebar = %q, want cmd+s", m.shortcutChord("toggle_sidebar"))
	}
	conflicts := shortcutConflicts(ggui.Peek(m.Prefs))
	if !slices.Contains(conflicts["toggle_sidebar"], "save_file") || !slices.Contains(conflicts["save_file"], "toggle_sidebar") {
		t.Fatalf("expected conflict between toggle_sidebar and save_file, got %#v", conflicts)
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, T("Some shortcuts use the same key combination. Change one so both can be triggered separately.")); !ok {
		t.Fatal("conflict warning banner not rendered in UI")
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("Conflicts with %s", T("Save file"))); !ok {
		t.Fatal("per-row conflict warning not rendered in UI")
	}

	// Per-row Reset button restores the default chord.
	p.Tap(Tf("Reset %s", T("Toggle sidebar")))
	if !sameShortcutChord(m.shortcutChord("toggle_sidebar"), "cmd+b") {
		t.Fatalf("per-row Reset did not restore default cmd+b, got %q", m.shortcutChord("toggle_sidebar"))
	}
	if len(shortcutConflicts(ggui.Peek(m.Prefs))) != 0 {
		t.Fatalf("expected no conflicts after per-row reset, got %#v", shortcutConflicts(ggui.Peek(m.Prefs)))
	}

	// Recording again and pressing Backspace restores the default chord.
	m.setShortcut("toggle_sidebar", "cmd+shift+b")
	p.Frame()
	p.Tap(Tf("Shortcut for %s", T("Toggle sidebar")))
	p.Type(ggui.Mods{}, ggui.KeyBackspace)
	if m.isRecordingShortcut() {
		t.Fatal("Backspace did not exit recording")
	}
	if !sameShortcutChord(m.shortcutChord("toggle_sidebar"), "cmd+b") {
		t.Fatalf("Backspace did not restore default cmd+b, got %q", m.shortcutChord("toggle_sidebar"))
	}

	// Customize two shortcuts and use "Reset all to defaults".
	m.setShortcut("toggle_sidebar", "cmd+shift+b")
	m.setShortcut("toggle_details", "cmd+shift+e")
	p.Frame()
	p.Tap(T("Reset all to defaults"))
	if len(ggui.Peek(m.Prefs).Shortcuts) != 0 {
		t.Fatalf("Reset all to defaults left overrides: %#v", ggui.Peek(m.Prefs).Shortcuts)
	}
}

func TestSettingsEscapeReturnsToWorkspace(t *testing.T) {
	t.Run("returns to workspace on settings page", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("settings")
		m.Section.Set("general")
		p := newShortcutsProbe(t, m)

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "workspace" {
			t.Fatalf("page = %q, want workspace", got)
		}
	})

	t.Run("does nothing on workspace page", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("workspace")
		p := newShortcutsProbe(t, m)

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "workspace" {
			t.Fatalf("page = %q, want workspace", got)
		}
	})

	t.Run("modal dialog captures Escape first", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("settings")
		m.Section.Set("general")
		p := newShortcutsProbe(t, m)

		m.openPalette()
		p.Frame()
		if !ggui.Peek(m.pickers.PaletteOpen) {
			t.Fatal("palette did not open")
		}

		p.Key("escape")
		if ggui.Peek(m.pickers.PaletteOpen) {
			t.Fatal("first Escape did not close palette dialog")
		}
		if got := ggui.Peek(m.Page); got != "settings" {
			t.Fatalf("first Escape left settings while dialog was open: page = %q", got)
		}

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "workspace" {
			t.Fatalf("second Escape did not return to workspace: page = %q", got)
		}
	})

	t.Run("focused text input keeps Escape from leaving settings", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("settings")
		m.Section.Set("general")
		p := newShortcutsProbe(t, m)

		field, ok := p.FindRole(ggui.RoleTextField, T("Default working directory"))
		if !ok {
			t.Fatal("could not find Default working directory text field")
		}
		p.Click(field.Center())

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "settings" {
			t.Fatalf("Escape while text field focused left settings: page = %q", got)
		}
	})

	t.Run("open dropdown captures first Escape then second returns to workspace", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("settings")
		m.Section.Set("general")
		p := newShortcutsProbe(t, m)

		sel, ok := p.FindRole(ggui.RoleSelect, T("Language"))
		if !ok {
			t.Fatal("could not find Language select")
		}
		p.Click(sel.Center())

		// Verify dropdown opened before testing Escape.
		if node, ok := p.Semantics().Find(ggui.RoleSelect, T("Language")); !ok || !node.Expanded.Open() {
			t.Fatalf("Language dropdown did not open: ok=%v expanded=%v", ok, node.Expanded)
		}

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "settings" {
			t.Fatalf("first Escape left settings while dropdown was open: page = %q", got)
		}
		if node, ok := p.Semantics().Find(ggui.RoleSelect, T("Language")); ok && node.Expanded.Open() {
			t.Fatal("first Escape did not close Language dropdown")
		}

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "workspace" {
			t.Fatalf("second Escape did not return to workspace: page = %q", got)
		}
	})

	t.Run("shortcut recorder captures first Escape then second returns to workspace", func(t *testing.T) {
		m := newShortcutsTestModel(t)
		m.Page.Set("settings")
		m.Section.Set("shortcuts")
		p := newShortcutsProbe(t, m)

		p.Tap(Tf("Shortcut for %s", T("Toggle sidebar")))
		if !m.isRecordingShortcut() {
			t.Fatal("recorder did not start")
		}

		p.Key("escape")
		if m.isRecordingShortcut() {
			t.Fatal("first Escape did not stop recording")
		}
		if got := ggui.Peek(m.Page); got != "settings" {
			t.Fatalf("first Escape left settings while recording: page = %q", got)
		}

		p.Key("escape")
		if got := ggui.Peek(m.Page); got != "workspace" {
			t.Fatalf("second Escape did not return to workspace: page = %q", got)
		}
	})
}
