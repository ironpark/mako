package mobile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/config"
)

func setupMobileTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	makoHome, err := os.MkdirTemp("", "mako-mob-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if runDir, err := config.RunDir(makoHome); err == nil {
			_ = os.RemoveAll(runDir)
		}
		_ = os.RemoveAll(makoHome)
	})
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MAKO_HOME", makoHome)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	return makoHome
}

func TestPeersAndRenameConfig(t *testing.T) {
	setupMobileTestEnv(t)

	peers, err := Peers()
	if err != nil {
		t.Fatalf("Peers() on fresh home: %v", err)
	}
	if peers != "" {
		t.Fatalf("expected empty peers on fresh home, got %q", peers)
	}

	if err := AddPeer("   "); err == nil || err.Error() != "no address" {
		t.Fatalf("AddPeer(blank) = %v, want error %q", err, "no address")
	}

	if err := AddPeer("node-a:4243"); err != nil {
		t.Fatalf("AddPeer(node-a): %v", err)
	}
	if err := AddPeer("  node-a:4243  "); err != nil {
		t.Fatalf("AddPeer(duplicate node-a): %v", err)
	}
	if err := AddPeer("node-b:4243"); err != nil {
		t.Fatalf("AddPeer(node-b): %v", err)
	}

	peers, err = Peers()
	if err != nil {
		t.Fatalf("Peers() after AddPeer: %v", err)
	}
	if want := "node-a:4243\nnode-b:4243"; peers != want {
		t.Fatalf("Peers() = %q, want %q", peers, want)
	}

	if err := RemovePeer("node-a:4243"); err != nil {
		t.Fatalf("RemovePeer(node-a): %v", err)
	}
	peers, err = Peers()
	if err != nil {
		t.Fatalf("Peers() after RemovePeer: %v", err)
	}
	if want := "node-b:4243"; peers != want {
		t.Fatalf("Peers() after remove = %q, want %q", peers, want)
	}

	if err := Rename("  phone-1  "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Name != "phone-1" {
		t.Fatalf("cfg.Name = %q, want %q", cfg.Name, "phone-1")
	}
}

func TestStopAndSocketWhenStopped(t *testing.T) {
	if got := Socket(); got != "" {
		t.Fatalf("Socket() when stopped = %q, want empty", got)
	}
	Stop()
	if got := Socket(); got != "" {
		t.Fatalf("Socket() after Stop() = %q, want empty", got)
	}
}

func TestStartSocketRestartAndStop(t *testing.T) {
	makoHome := setupMobileTestEnv(t)
	cache := t.TempDir()

	cfgJSON := `{"node_id":"mobile-test-node","name":"init-phone","p2p":{"disabled":true},"discovery":{"mdns":false,"tailscale":false}}`
	if err := os.WriteFile(filepath.Join(makoHome, "config.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	t.Cleanup(Stop)

	if err := Start(makoHome, cache, "ignored-when-config-exists"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sock := Socket()
	if sock == "" {
		t.Fatal("Socket() is empty after Start")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("stat socket %q: %v", sock, err)
	}

	// Second Start while already running is a no-op.
	if err := Start(makoHome, cache, "other"); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := Socket(); got != sock {
		t.Fatalf("Socket() changed on second Start: got %q, want %q", got, sock)
	}

	// Rename while running restarts the node cleanly.
	if err := Rename("renamed-phone"); err != nil {
		t.Fatalf("Rename while running: %v", err)
	}
	if Socket() == "" {
		t.Fatal("Socket() is empty after Rename restart")
	}

	Stop()
	if got := Socket(); got != "" {
		t.Fatalf("Socket() after Stop = %q, want empty", got)
	}
}
