package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// settleClosed runs a closed-state frame + input dispatch so ggui's FocusTrap
// observes the dialog closing before the same test opens it again.
func settleClosed(p *ggui.Probe) {
	p.Advance(2 * motionDuration)
	p.Move(ggui.Pt(0, 0))
}

func TestConfirmDialogKeyboardNav(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")

	confirmed := 0
	bgClicks := 0
	open := func() {
		m.askConfirm(confirmation{
			Title:  "Kill session?",
			Body:   "Running work will stop.",
			Action: "Kill",
			Run:    func() { confirmed++ },
		})
	}

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Stack(
			widget.Column(
				ui.Button("Open confirm", open),
				ui.Button("Background", func() { bgClicks++ }),
			).Gap(8),
			confirmDialog(m),
		).Expand()
	}, ggui.Sz(640, 480))
	defer p.Close()
	p.Frame()

	// Open via keyboard on the opener button so focus-return is exercised too.
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyEnter)
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("confirm dialog did not open")
	}

	// Default focus is on Cancel (safe default); Enter cancels without confirming.
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 0 {
		t.Fatalf("Enter on initial focus: title=%q confirmed=%d, want closed and 0", ggui.Peek(m.Confirmation).Title, confirmed)
	}

	// Focus returned to the opener button, so Enter re-opens the dialog.
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("focus did not return to opener after confirmDialog closed")
	}

	// ArrowRight moves from Cancel -> Kill; Enter confirms and closes.
	p.Type(ggui.Mods{}, ggui.KeyArrowRight, ggui.KeyEnter)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 1 {
		t.Fatalf("ArrowRight+Enter: title=%q confirmed=%d, want closed and 1", ggui.Peek(m.Confirmation).Title, confirmed)
	}

	// ArrowRight -> ArrowLeft moves to Kill and back to Cancel; Space cancels.
	settleClosed(p)
	open()
	p.Type(ggui.Mods{}, ggui.KeyArrowRight, ggui.KeyArrowLeft, ggui.KeySpace)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 1 {
		t.Fatalf("ArrowRight+ArrowLeft+Space: title=%q confirmed=%d, want closed and 1", ggui.Peek(m.Confirmation).Title, confirmed)
	}

	// Tab + Space confirms; focus stays trapped (Tab never escapes to Background).
	settleClosed(p)
	open()
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyTab, ggui.KeyTab, ggui.KeySpace)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 2 {
		t.Fatalf("Tab cycle + Space: title=%q confirmed=%d, want closed and 2", ggui.Peek(m.Confirmation).Title, confirmed)
	}
	if bgClicks != 0 {
		t.Fatalf("background button received %d clicks while dialog was open", bgClicks)
	}

	// Shift+Tab wraps from Cancel to Kill; Space confirms.
	settleClosed(p)
	open()
	p.Type(ggui.Mods{Shift: true}, ggui.KeyTab)
	p.Type(ggui.Mods{}, ggui.KeySpace)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 3 {
		t.Fatalf("Shift+Tab + Space: title=%q confirmed=%d, want closed and 3", ggui.Peek(m.Confirmation).Title, confirmed)
	}

	// Scrim click must NOT dismiss AlertDialog, and Escape cancels cleanly afterward.
	settleClosed(p)
	open()
	p.Click(ggui.Pt(5, 5))
	if ggui.Peek(m.Confirmation).Title == "" {
		t.Fatal("clicking the scrim dismissed confirmDialog")
	}
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(m.Confirmation).Title != "" || confirmed != 3 {
		t.Fatalf("Escape after scrim click: title=%q confirmed=%d, want closed and 3", ggui.Peek(m.Confirmation).Title, confirmed)
	}
}

func TestNameDialogKeyboardNav(t *testing.T) {
	open := ggui.State(false)
	text := ggui.State("")
	errMsg := ggui.State("")
	submitted := []string{}
	bgClicks := 0

	dlg := nameDialog(nameAsk{
		Open:        open,
		Text:        text,
		Err:         errMsg,
		Title:       "Rename session",
		Field:       "Session name",
		Action:      "Rename",
		Placeholder: "Name",
		Submit: func() {
			submitted = append(submitted, ggui.Peek(text))
			open.Set(false)
		},
	})

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Stack(
			ui.Button("Background", func() { bgClicks++ }),
			dlg,
		).Expand()
	}, ggui.Sz(640, 480))
	defer p.Close()
	p.Frame()

	// Opening moves focus into the TextField; arrow keys edit text, not dialog buttons.
	open.Set(true)
	p.Text("ac")
	p.Type(ggui.Mods{}, ggui.KeyArrowLeft)
	p.Text("b")
	if got := ggui.Peek(text); got != "abc" {
		t.Fatalf("text after ArrowLeft edit = %q, want %q", got, "abc")
	}

	// Enter inside the single-line TextField submits.
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if ggui.Peek(open) || len(submitted) != 1 || submitted[0] != "abc" {
		t.Fatalf("Enter in TextField: open=%v submitted=%v", ggui.Peek(open), submitted)
	}

	// Tab to Cancel, ArrowRight to Rename, Space submits.
	settleClosed(p)
	text.Set("next")
	open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyArrowRight, ggui.KeySpace)
	if ggui.Peek(open) || len(submitted) != 2 || submitted[1] != "next" {
		t.Fatalf("Tab+ArrowRight+Space: open=%v submitted=%v", ggui.Peek(open), submitted)
	}

	// Tab to Cancel, ArrowRight to Rename, ArrowLeft back to Cancel, Enter cancels.
	settleClosed(p)
	text.Set("cancelled")
	open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyArrowRight, ggui.KeyArrowLeft, ggui.KeyEnter)
	if ggui.Peek(open) || len(submitted) != 2 {
		t.Fatalf("ArrowLeft back to Cancel: open=%v submitted=%v", ggui.Peek(open), submitted)
	}

	// Escape while TextField is focused dismisses the dialog.
	settleClosed(p)
	open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(open) {
		t.Fatal("Escape in TextField did not close nameDialog")
	}
	if bgClicks != 0 {
		t.Fatalf("background button received %d clicks", bgClicks)
	}
}

func TestHomeMigrateDialogKeyboardNav(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	h := m.HomeMigrate
	h.TargetIsolate.Set(true)
	h.Info.Set(&protocol.AgentHomeInfo{
		Supported:     true,
		FromDir:       "/Users/me",
		ToDir:         "/Users/me/.mako/home",
		SavedSessions: 2,
		HasConfig:     true,
	})
	h.CopyConfig.Set(true)
	h.MoveSessions.Set(true)
	h.Busy.Set(false)
	h.Err.Set("")

	bgClicks := 0
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Stack(
			ui.Button("Background", func() { bgClicks++ }),
			homeMigrateDialog(m),
		).Expand()
	}, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()

	// Open: focus lands on the first checkbox (Copy configuration and credentials).
	// Space toggles it off; ArrowDown moves to Move Mako sessions; Enter toggles it off.
	h.Open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeySpace, ggui.KeyArrowDown, ggui.KeyEnter)
	if ggui.Peek(h.CopyConfig) {
		t.Fatal("Space did not toggle CopyConfig checkbox")
	}
	if ggui.Peek(h.MoveSessions) {
		t.Fatal("ArrowDown+Enter did not toggle MoveSessions checkbox")
	}

	// ArrowDown moves to Cancel, ArrowRight to Switch without migrating,
	// ArrowLeft back to Cancel, Space closes without migrating.
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyArrowRight, ggui.KeyArrowLeft, ggui.KeySpace)
	if ggui.Peek(h.Open) {
		t.Fatal("activating Cancel via keyboard did not close homeMigrateDialog")
	}

	// Re-open, navigate via Tab and ArrowRight to "Switch without migrating" and activate.
	// Stops: [0: CopyConfig, 1: MoveSessions, 2: Cancel, 3: Switch without migrating, 4: Migrate & apply]
	settleClosed(p)
	h.Open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyTab, ggui.KeyArrowRight, ggui.KeyEnter)
	if ggui.Peek(h.Open) {
		t.Fatal("activating Switch without migrating did not close homeMigrateDialog")
	}
	if !ggui.Peek(m.IsolateHome) {
		t.Fatal("Switch without migrating did not persist IsolateHome=true")
	}

	// While Busy is true, disabled buttons cannot be activated; Escape still closes.
	settleClosed(p)
	h.Busy.Set(true)
	h.Open.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyArrowDown, ggui.KeyEnter)
	if !ggui.Peek(h.Open) {
		t.Fatal("disabled Cancel button closed homeMigrateDialog while Busy")
	}
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(h.Open) {
		t.Fatal("Escape did not close homeMigrateDialog")
	}
	if bgClicks != 0 {
		t.Fatalf("background button received %d clicks", bgClicks)
	}
}

func TestAddFolderDialogSelectAndKeys(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n1", Name: "local", Source: "self"}})
	m.Nodes.Set([]*protocol.Node{
		{Id: "n1", Name: "local", Source: "self"},
		{Id: "n2", Name: "remote", Source: "lan"},
	})
	m.Proj.AddNode.Set("n1")
	m.Proj.AddPath.Set("")
	m.Proj.AddErr.Set("")

	p := ggui.ProbeBuilder(func() ggui.Widget { return addFolderDialog(m) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()

	// Open: focus lands on Node Select. ArrowDown opens the dropdown (NOT stolen by dialog nav),
	// Escape closes only the dropdown, leaving the dialog open.
	m.Proj.AddOpen.Set(true)
	p.Type(ggui.Mods{}, ggui.KeyArrowDown)
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if !ggui.Peek(m.Proj.AddOpen) {
		t.Fatal("first Escape while Select popup was open closed the entire dialog")
	}

	// Second Escape closes the dialog.
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(m.Proj.AddOpen) {
		t.Fatal("second Escape did not close addFolderDialog")
	}
}

func TestForgeDialogKeyboardNav(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")

	p := ggui.ProbeBuilder(func() ggui.Widget { return forgeDialogs(m) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()

	m.askPR()
	// Title -> Tab -> Description -> Tab -> Base branch -> Tab -> Draft checkbox -> Space toggles Draft.
	p.Text("My PR")
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Text("Details")
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Text("main")
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeySpace)

	if got := ggui.Peek(m.Forge.PRTitle); got != "My PR" {
		t.Fatalf("PRTitle = %q, want %q", got, "My PR")
	}
	if got := ggui.Peek(m.Forge.PRBody); got != "Details" {
		t.Fatalf("PRBody = %q, want %q", got, "Details")
	}
	if got := ggui.Peek(m.Forge.PRBase); got != "main" {
		t.Fatalf("PRBase = %q, want %q", got, "main")
	}
	if !ggui.Peek(m.Forge.PRDraft) {
		t.Fatal("Space on Draft checkbox did not toggle PRDraft")
	}

	// ArrowDown from Draft moves to Cancel; Enter closes.
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyEnter)
	if ggui.Peek(m.Forge.PROpen) {
		t.Fatal("ArrowDown+Enter on Cancel did not close forgeDialogs")
	}
}

func TestFileDeleteDialogKeyboardNav(t *testing.T) {
	ops := newFileOps()
	submitted := 0
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ops.dialogs(func() { submitted++ })
	}, ggui.Sz(640, 480))
	defer p.Close()
	p.Frame()

	ops.ask(protocol.FileDelete, fileItem{Name: "notes.txt", Path: "/tmp/notes.txt"})
	// ArrowRight moves Cancel -> Delete; Enter confirms and closes.
	p.Type(ggui.Mods{}, ggui.KeyArrowRight, ggui.KeyEnter)
	if ggui.Peek(ops.DeleteOpen) || submitted != 1 {
		t.Fatalf("Delete confirm: open=%v submitted=%d, want false and 1", ggui.Peek(ops.DeleteOpen), submitted)
	}
}

func TestShellDialogKeyboardNav(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:    "s1",
		State: protocol.StateRunning,
		Tasks: []*protocol.BackgroundTask{{
			Id:      "t1",
			Command: "echo hi",
			Started: time.Now().Unix(),
		}},
	}})

	p := ggui.ProbeBuilder(func() ggui.Widget { return shellDialog(m) }, ggui.Sz(760, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	m.Shell.open("s1", "t1")
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(m.Shell.Open) {
		t.Fatal("Escape did not close shellDialog")
	}
}

func TestFolderPickerInlineNamingEscape(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Nodes.Set([]*protocol.Node{{Id: "n1", Name: "box", Source: "lan"}})
	m.Pick.Node = "n1"
	m.Pick.Home = "/home/me"

	p := ggui.ProbeBuilder(func() ggui.Widget { return folderPickerDialog(m) }, ggui.Sz(840, 600))
	defer p.Close()
	m.post = p.Post
	m.Pick.reset("/home/me")
	m.Pick.On.Set(true)
	p.Frame()

	// Tap "New folder" to enter inline folder naming; focus moves into the new
	// folder TextField automatically, and Esc cancels naming without closing picker.
	p.Tap(T("New folder"))
	if !ggui.Peek(m.Pick.Naming) {
		t.Fatal("New folder did not enter naming mode")
	}
	p.Frame()
	p.Text("sub")
	if got := ggui.Peek(m.Pick.NewName); got != "sub" {
		t.Fatalf("NewName after auto-focus typing = %q, want %q", got, "sub")
	}
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(m.Pick.Naming) {
		t.Fatal("first Escape did not exit inline folder naming")
	}
	if !ggui.Peek(m.Pick.On) {
		t.Fatal("first Escape while naming closed the entire folder picker")
	}
	// Second Escape closes the folder picker.
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if ggui.Peek(m.Pick.On) {
		t.Fatal("second Escape did not close folderPickerDialog")
	}
}

func TestProjectSettingsDialogKeyboardNav(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Nodes.Set([]*protocol.Node{{Id: "n1", Name: "local", Source: "self"}})
	g := sessionGroup{
		Key:     "n1\x00/tmp/demo",
		NodeID:  "n1",
		Project: "/tmp/demo",
		Roots:   []fileRoot{{Node: "n1", Path: "/tmp/demo"}},
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return projectSettingsDialog(m) }, ggui.Sz(800, 640))
	defer p.Close()
	p.Frame()

	m.openProjectSettings(g)
	// Stops: [0: Project name TextField, 1: Folder label TextField, 2: Connect new clones Switch,
	//         3: Give agents its other folders Switch, 4: Cancel, 5: Save]
	p.Type(ggui.Mods{}, ggui.KeyTab, ggui.KeyTab, ggui.KeySpace)
	if !ggui.Peek(m.Proj.Draft).NoJoin {
		t.Fatal("Space on 'Connect new clones' switch did not toggle NoJoin")
	}
	// ArrowDown to 'Give agents its other folders' switch; Enter toggles GiveDirs on.
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyEnter)
	if !ggui.Peek(m.Proj.Draft).GiveDirs {
		t.Fatal("ArrowDown+Enter on 'Give agents its other folders' switch did not toggle GiveDirs")
	}
	// ArrowDown to Cancel, ArrowRight to Save, ArrowLeft back to Cancel, Enter closes without saving.
	p.Type(ggui.Mods{}, ggui.KeyArrowDown, ggui.KeyArrowRight, ggui.KeyArrowLeft, ggui.KeyEnter)
	if ggui.Peek(m.Proj.SettingsOpen) {
		t.Fatal("Enter on Cancel did not close projectSettingsDialog")
	}
}
