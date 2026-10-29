package config

import (
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
