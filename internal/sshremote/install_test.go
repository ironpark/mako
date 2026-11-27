package sshremote

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/config"
)

func TestInstallMakodUploadLocalAndVerify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake ssh test")
	}
	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh")
	sshScript := `#!/bin/sh
while [ $# -gt 0 ]; do
  if [ "$1" = "--" ]; then shift; shift; break; fi
  shift
done
exec /bin/sh -c "$*"
`
	if err := os.WriteFile(fakeSSH, []byte(sshScript), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := SSHBinary
	SSHBinary = fakeSSH
	t.Cleanup(func() { SSHBinary = oldBin })

	// Create a tiny executable script pretending to be a compiled makod binary.
	fakeMakod := filepath.Join(t.TempDir(), "makod")
	makodScript := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'makod 0.9.0-test'; exit 0; fi\n"
	if err := os.WriteFile(fakeMakod, []byte(makodScript), 0o755); err != nil {
		t.Fatal(err)
	}

	destPath := filepath.Join(t.TempDir(), "remote-home", ".mako", "bin", "makod")
	var steps []string
	res, err := InstallMakod(context.Background(), config.SSHHost{
		ID:   "inst1",
		Name: "Install Host",
		Host: "install.local",
	}, InstallOptions{
		LocalBinary: fakeMakod,
		DestPath:    destPath,
		OnProgress: func(step string) {
			steps = append(steps, step)
		},
	})
	if err != nil {
		t.Fatalf("InstallMakod failed: %v", err)
	}
	if res.RemotePath != destPath {
		t.Fatalf("RemotePath = %q, want %q", res.RemotePath, destPath)
	}
	if res.Version != "makod 0.9.0-test" {
		t.Fatalf("Version = %q, want 'makod 0.9.0-test'", res.Version)
	}
	if res.Method != "upload-local" {
		t.Fatalf("Method = %q, want upload-local", res.Method)
	}
	if len(steps) < 3 {
		t.Fatalf("expected progress steps, got %v", steps)
	}
	fi, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("installed binary missing at %q: %v", destPath, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("installed binary not executable: mode=%v", fi.Mode())
	}

	// Verify missing local binary returns a clear error.
	if _, err := InstallMakod(context.Background(), config.SSHHost{Host: "install.local"}, InstallOptions{
		LocalBinary: filepath.Join(t.TempDir(), "missing"),
	}); err == nil || !strings.Contains(err.Error(), "not a readable file") {
		t.Fatalf("expected missing local binary error, got %v", err)
	}
}
