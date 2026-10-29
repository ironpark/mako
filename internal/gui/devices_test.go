package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// The devices menu lists the devices this node borrows, with their
// environment and a way back, and the devices other nodes lend; it is not
// there while there are none.
func TestTheDevicesMenuListsWhatToBorrow(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "me", Name: "laptop"}})
	m.Nodes.Set([]*protocol.Node{{Id: "me", Name: "laptop"}, {Id: "desk", Name: "desktop"}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return devicesMenu(m) }, ggui.Sz(600, 400))
	defer p.Close()
	p.Frame()
	if strings.Contains(p.Semantics().String(), "devices") {
		t.Fatal("a menu with no devices")
	}

	pixel := &protocol.Device{NodeId: "desk", Kind: "adb", Serial: "P1", Name: "Pixel 8", State: "device", Shared: true}
	tab := &protocol.Device{NodeId: "desk", Kind: "adb", Serial: "T1", Name: "Tab", State: "device", Shared: true}
	own := &protocol.Device{NodeId: "me", Kind: "adb", Serial: "O1", State: "device", Shared: true}
	m.Devices.Set(deviceList{
		Devices: []*protocol.Device{pixel, tab, own},
		Leases: []*protocol.Lease{{Id: "l", Device: pixel, Addr: "127.0.0.1:5555", State: protocol.LeaseDetached, Borrowed: true,
			Env: map[string]string{"ANDROID_SERIAL": "P1"}}},
	})
	p.Frame()
	menu, ok := p.Find(Tf("%d devices borrowed", 1))
	if !ok {
		t.Fatalf("no devices menu:\n%s", p.Semantics().String())
	}
	p.Click(menu.Center())
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{
		"Pixel 8 (P1) of desktop · " + T("not attached"),
		T("Copy its adb environment"),
		T("Give it back"),
		Tf("Borrow %s of %s", "Tab (T1)", "desktop"),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in:\n%s", want, page)
		}
	}
	if strings.Contains(page, "O1") || strings.Contains(page, "Borrow Pixel") {
		t.Errorf("offered this node's own device, or one borrowed already:\n%s", page)
	}
	if got := shellEnv(map[string]string{"B": "it's", "A": "x"}); got != "export A=x\nexport B='it'\\''s'\n" && got != "export A='x'\nexport B='it'\\''s'\n" {
		t.Errorf("environment %q", got)
	}
}
