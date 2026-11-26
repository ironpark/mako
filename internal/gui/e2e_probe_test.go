package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestAppProbeAndUiTreeProbeSmoke(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	ap := NewAppProbe(config.Default(), "", ggui.Sz(1280, 800))
	defer ap.Close()

	if ap.Probe() == nil {
		t.Fatal("AppProbe.Probe() returned nil")
	}
	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("initial SidebarPage = %q, want empty", got)
	}
	if got := ap.SelectedSession(); got != "" {
		t.Fatalf("initial SelectedSession = %q, want empty", got)
	}
	ap.m.setPlugins([]*protocol.PluginInfo{sidebarAndTabPlugin(protocol.PluginRunning)})
	ap.OpenSidebarPage("plugin:acme-tools/explorer")
	if got := ap.SidebarPage(); got != "plugin:acme-tools/explorer" {
		t.Fatalf("SidebarPage after OpenSidebarPage = %q", got)
	}

	var gotID, gotKind, gotVal string
	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/root/btn", Kind: "button", Text: "Trigger Action", Tap: true},
		},
	}
	up := NewUiTreeProbe(tree, func(id, kind, value string) {
		gotID, gotKind, gotVal = id, kind, value
	}, ggui.Sz(640, 480))
	defer up.Close()

	if sem := up.Semantics().String(); !strings.Contains(sem, "Trigger Action") {
		t.Fatalf("UiTreeProbe semantics missing button:\n%s", sem)
	}
	up.Tap("Trigger Action")
	up.Frame()
	if gotID != "/root/btn" || gotKind != protocol.UiTap || gotVal != "" {
		t.Fatalf("UiTreeProbe event = (%q, %q, %q), want (/root/btn, tap, '')", gotID, gotKind, gotVal)
	}
}

func TestUiTreeProbeNilOnEventDoesNotPanic(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/root/btn", Kind: "button", Text: "Trigger Action", Tap: true},
			{
				Id:        "/root/g",
				Kind:      "graph",
				MaxHeight: 240,
				Change:    true,
				DoubleTap: true,
				GraphNodes: []*protocol.UiGraphNode{
					{Id: "a", Label: "Alpha Node", X: 0, Y: 0, HasPos: true},
				},
			},
		},
	}

	up := NewUiTreeProbe(tree, nil, ggui.Sz(640, 480))
	defer up.Close()

	up.Tap("Trigger Action")
	up.Frame()

	alphaSem, ok := up.Semantics().Find(ggui.RoleButton, "Alpha Node")
	if !ok {
		t.Fatalf("Alpha Node missing in semantics:\n%s", up.Semantics().String())
	}
	up.Click(ggui.Pt(alphaSem.Rect.Origin.X+alphaSem.Rect.Size.W/2, alphaSem.Rect.Origin.Y+alphaSem.Rect.Size.H/2))
	up.Frame()
}
