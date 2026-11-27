package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/sshremote"
)

func TestCLISSHAddDisableEnableRemoveAndTest(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MAKO_HOME", dir)

	if err := sshCmd([]string{"add", "-name", "GPU Box", "-user", "ubuntu", "-port", "2222", "-dir", "~/src", "-mode", "direct", "10.0.0.5"}); err != nil {
		t.Fatalf("ssh add: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SSHHosts) != 1 || cfg.SSHHosts[0].Name != "GPU Box" || cfg.SSHHosts[0].Port != 2222 || cfg.SSHHosts[0].Mode != "direct" {
		t.Fatalf("unexpected saved SSHHosts: %+v", cfg.SSHHosts)
	}

	if err := sshCmd([]string{"disable", "GPU Box"}); err != nil {
		t.Fatalf("ssh disable: %v", err)
	}
	cfg, _ = config.Load()
	if !cfg.SSHHosts[0].Disabled {
		t.Fatal("expected SSH host to be disabled")
	}

	if err := sshCmd([]string{"enable", cfg.SSHHosts[0].NodeID()}); err != nil {
		t.Fatalf("ssh enable: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.SSHHosts[0].Disabled {
		t.Fatal("expected SSH host to be enabled")
	}

	if runtime.GOOS != "windows" {
		fakeBin := filepath.Join(t.TempDir(), "fake-ssh")
		script := `#!/bin/sh
while [ $# -gt 0 ]; do
  if [ "$1" = "--" ]; then shift; shift; break; fi
  shift
done
exec /bin/sh -c "$*"
`
		if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		old := sshremote.SSHBinary
		sshremote.SSHBinary = fakeBin
		t.Cleanup(func() { sshremote.SSHBinary = old })

		if err := sshCmd([]string{"test", "GPU Box"}); err != nil {
			t.Fatalf("ssh test: %v", err)
		}

		fakeMakod := filepath.Join(t.TempDir(), "makod")
		if err := os.WriteFile(fakeMakod, []byte("#!/bin/sh\necho 'makod 0.9.0-cli'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(t.TempDir(), "bin", "makod")
		if err := sshCmd([]string{"install", "-binary", fakeMakod, "-dest", dest, "GPU Box"}); err != nil {
			t.Fatalf("ssh install: %v", err)
		}
		if _, err := os.Stat(dest); err != nil {
			t.Fatalf("installed makod missing at %q: %v", dest, err)
		}
	}

	if err := sshCmd([]string{"remove", "GPU Box"}); err != nil {
		t.Fatalf("ssh remove: %v", err)
	}
	cfg, _ = config.Load()
	if len(cfg.SSHHosts) != 0 {
		t.Fatalf("expected 0 hosts after remove, got %+v", cfg.SSHHosts)
	}
}
