package config

import (
	"testing"
)

func TestSSHHostValidateAndNormalize(t *testing.T) {
	valid := SSHHost{
		Name:         "gpu-box",
		Host:         "dev@10.0.0.5",
		Port:         2222,
		IdentityFile: "~/.ssh/id_ed25519",
		ProxyJump:    "bastion",
		DefaultDir:   "~/work",
		Mode:         "direct",
		ExtraArgs:    []string{"-o", "ForwardAgent=yes"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid host rejected: %v", err)
	}
	if got := valid.DisplayAddr(); got != "dev@10.0.0.5:2222" {
		t.Fatalf("DisplayAddr = %q, want dev@10.0.0.5:2222", got)
	}

	invalid := []SSHHost{
		{Host: ""},
		{Host: "-oProxyCommand=evil"},
		{Host: "host with space"},
		{Host: "ok", User: "-bad"},
		{Host: "ok", Port: -1},
		{Host: "ok", Port: 70000},
		{Host: "ok", ProxyJump: "-evil"},
		{Host: "ok", Mode: "unknown"},
		{Host: "ok", ExtraArgs: []string{"bad\narg"}},
	}
	for i, h := range invalid {
		if err := h.Validate(); err == nil {
			t.Errorf("case %d (%+v): expected validation error", i, h)
		}
	}

	norm := NormalizeSSHHosts([]SSHHost{
		{Host: "   "},
		{ID: "a", Host: "  box1  ", Mode: "AUTO"},
		{ID: "a", Name: "Box 2", Host: "box2", Mode: "daemon"},
	})
	if len(norm) != 2 {
		t.Fatalf("NormalizeSSHHosts len = %d, want 2", len(norm))
	}
	if norm[0].ID != "a" || norm[0].Name != "box1" || norm[0].Host != "box1" || norm[0].Mode != "" {
		t.Fatalf("norm[0] = %+v", norm[0])
	}
	if norm[1].ID == "a" || norm[1].ID == "" || norm[1].Name != "Box 2" || norm[1].Mode != "daemon" {
		t.Fatalf("norm[1] = %+v", norm[1])
	}
	if !IsSSHNodeID(norm[0].NodeID()) || IsSSHNodeID("plain-node") || IsSSHNodeID(SSHNodePrefix) {
		t.Fatalf("IsSSHNodeID check failed for %q", norm[0].NodeID())
	}
}

func TestSSHHostsRoundtrip(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MAKO_HOME", dir)

	cfg := Default()
	cfg.SSHHosts = []SSHHost{
		{
			ID:           "gpu1",
			Name:         "GPU Server",
			Host:         "gpu.internal",
			User:         "ubuntu",
			Port:         2200,
			IdentityFile: "~/.ssh/gpu_key",
			DefaultDir:   "~/src",
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.SSHHosts) != 1 || !loaded.SSHHosts[0].Equal(cfg.SSHHosts[0]) {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", loaded.SSHHosts, cfg.SSHHosts)
	}
}
