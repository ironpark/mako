package config

import (
	"github.com/ironpark/mako/internal/agentspec"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsSSHKey(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MAKO_HOME", dir)
	path := filepath.Join(dir, "config.json")
	load := func(body string) string {
		t.Helper()
		if body == "" {
			os.Remove(path)
		} else if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		return cfg.SSHKey
	}

	if got := load(`{"node_id": "n1"}`); got != "" {
		t.Fatalf("without a key in ~/.ssh: %q, want none", got)
	}
	ssh := filepath.Join(home, ".ssh")
	os.MkdirAll(ssh, 0o700)
	for _, name := range []string{"id_rsa.pub", "id_ed25519.pub"} {
		if err := os.WriteFile(filepath.Join(ssh, name), []byte("ssh-ed25519 AAAA\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := load(`{"node_id": "n1"}`); got != "~/.ssh/id_ed25519.pub" {
		t.Fatalf("left out: %q, want ~/.ssh/id_ed25519.pub", got)
	}
	if got := load(""); got != "~/.ssh/id_ed25519.pub" {
		t.Fatalf("fresh install: %q, want ~/.ssh/id_ed25519.pub", got)
	}
	if got := load(`{"node_id": "n1", "ssh_key": ""}`); got != "" {
		t.Fatalf("set empty: %q, want none", got)
	}
	if got := load(`{"node_id": "n1", "ssh_key": "~/keys/work.pub"}`); got != "~/keys/work.pub" {
		t.Fatalf("set: %q, want ~/keys/work.pub", got)
	}
}

// A shell an older configuration kept among its agents moves to its own
// place, the login shell it named by default left to follow the user's.
func TestTheShellLeavesTheAgents(t *testing.T) {
	cfg := &Config{Agents: map[string]agentspec.Agent{
		"shell":  {Command: []string{agentspec.DefaultShell()}, Env: map[string]string{"A": "b"}},
		"claude": {},
	}}
	cfg.TakeShell()
	if _, ok := cfg.Agents["shell"]; ok || len(cfg.Agents) != 1 {
		t.Fatalf("agents %v", cfg.Agents)
	}
	if cfg.Shell.Command != nil || cfg.Shell.Env["A"] != "b" {
		t.Fatalf("shell %+v", cfg.Shell)
	}
	cfg = &Config{Agents: map[string]agentspec.Agent{"shell": {Command: []string{"fish"}}}}
	cfg.TakeShell()
	if len(cfg.Shell.Command) != 1 || cfg.Shell.Command[0] != "fish" {
		t.Fatalf("a shell of its own became %+v", cfg.Shell)
	}
}
