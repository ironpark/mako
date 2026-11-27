package gui

import (
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

func TestLaunchTabEnvironmentPickerShowsAddSSHHost(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	self := &protocol.Node{
		Id: "studio", Name: "MacStudio", Source: "self",
		Agents: []*protocol.AgentInfo{{Name: "claude"}},
	}
	sshNode := &protocol.Node{
		Id: "ssh:gpu1", Name: "GPU Box", Source: "ssh",
		TrustsUs: true, Link: protocol.LinkUp,
		Features: []string{sshremote.FeatureSSH, sshremote.FeatureSSHDirect},
		Agents:   []*protocol.AgentInfo{{Name: "claude"}, {Name: "shell"}},
	}
	m.Conn.Set(connState{OK: true, Self: self})
	m.Nodes.Set([]*protocol.Node{self, sshNode})
	m.SSHHosts.Set([]config.SSHHost{{ID: "gpu1", Name: "GPU Box", Host: "gpu.local", DefaultDir: "~/work"}})
	m.Agents.Set([]string{"claude"})

	l := m.openNew("studio", "/work/a")
	tabID := ggui.Peek(m.Selected)

	p := ggui.ProbeBuilder(func() ggui.Widget { return launchBox(m, tabID, l) }, ggui.Sz(760, 340))
	defer p.Close()
	p.Frame()

	envBtn, ok := p.Semantics().Find(ggui.RoleMenu, T("Environment"))
	if !ok {
		t.Fatalf("missing Environment picker button:\n%s", p.Semantics().String())
	}
	p.Click(envBtn.Rect.Origin.Add(ggui.Pt(envBtn.Rect.Size.W/2, envBtn.Rect.Size.H/2)))
	p.Frame()
	p.Frame()

	if _, ok := p.Semantics().Find(ggui.RoleMenuItem, T("Add SSH host…")); !ok {
		t.Fatalf("Environment menu missing 'Add SSH host…':\n%s", p.Semantics().String())
	}
	sshItem, ok := p.Semantics().Find(ggui.RoleMenuItem, "GPU Box (SSH)")
	if !ok {
		t.Fatalf("Environment menu missing SSH host item:\n%s", p.Semantics().String())
	}
	p.Click(sshItem.Rect.Origin.Add(ggui.Pt(sshItem.Rect.Size.W/2, sshItem.Rect.Size.H/2)))
	p.Frame()

	got := ggui.Peek(l.Form)
	if got.NodeID != "ssh:gpu1" || got.Project != "~/work" {
		t.Fatalf("after selecting SSH host: form = (%q, %q), want (ssh:gpu1, ~/work)", got.NodeID, got.Project)
	}
}

func TestSSHHostDialogFillAndSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAKO_HOME", dir)
	cfg := config.Default()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")

	var savedNodeID string
	m.openAddSSHHost(func(id string) { savedNodeID = id })
	m.SSH.ConfigHosts.Set([]sshremote.ConfigHost{
		{Alias: "dev-gpu", HostName: "10.0.0.9", User: "ubuntu", Port: 2222, IdentityFile: "~/.ssh/gpu"},
	})
	m.SSH.fillFromConfig(ggui.Peek(m.SSH.ConfigHosts)[0])
	m.SSH.DefaultDir.Set("~/ml")
	m.submitSSHHost()

	if ggui.Peek(m.SSH.Open) {
		t.Fatalf("dialog remained open with err=%q", ggui.Peek(m.SSH.Err))
	}
	if !config.IsSSHNodeID(savedNodeID) {
		t.Fatalf("savedNodeID = %q, want ssh:<id>", savedNodeID)
	}
	hosts := ggui.Peek(m.SSHHosts)
	if len(hosts) != 1 || hosts[0].Name != "dev-gpu" || hosts[0].Port != 2222 || hosts[0].DefaultDir != "~/ml" {
		t.Fatalf("saved hosts = %+v", hosts)
	}

	// Verify persisted to config.json.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.SSHHosts) != 1 || reloaded.SSHHosts[0].Host != "dev-gpu" {
		t.Fatalf("reloaded SSHHosts = %+v", reloaded.SSHHosts)
	}

	// Verify Settings ▸ Nodes renders SSH hosts section cleanly.
	m.Nodes.Set([]*protocol.Node{
		{Id: "local", Name: "Mac", Source: "self"},
		{Id: hosts[0].NodeID(), Name: "dev-gpu", Source: "ssh", TrustsUs: true, Link: protocol.LinkUp, Features: []string{sshremote.FeatureSSH, sshremote.FeatureSSHDaemon}},
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return nodeSettings(m) }, ggui.Sz(840, 600))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleButton, T("Add SSH host…")); !ok {
		t.Fatalf("nodeSettings missing Add SSH host button:\n%s", p.Semantics().String())
	}

	// Verify Host Config modal exposes the Install makod action.
	m.openEditSSHHost(hosts[0])
	dp := ggui.ProbeBuilder(func() ggui.Widget { return sshHostForm(m, T("Save")) }, ggui.Sz(560, 620))
	defer dp.Close()
	dp.Frame()
	if _, ok := dp.Semantics().Find(ggui.RoleButton, T("Install makod")); !ok {
		t.Fatalf("sshHostForm missing Install makod button:\n%s", dp.Semantics().String())
	}
}
