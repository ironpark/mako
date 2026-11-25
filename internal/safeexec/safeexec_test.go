package safeexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestPreflightRejectsBadDirAndMissingBinary(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = missingDir
	err := Preflight(cmd)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: err = %v, want os.ErrNotExist", err)
	}

	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("/bin/sh", "-c", "true")
	cmd.Dir = notDir
	err = Preflight(cmd)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file as dir: err = %v, want ENOTDIR", err)
	}

	missingBin := filepath.Join(t.TempDir(), "missing-bin")
	cmd = exec.Command(missingBin)
	err = Preflight(cmd)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing abs binary: err = %v, want os.ErrNotExist", err)
	}

	cmd = exec.Command("mako-definitely-missing-command-xyz")
	err = Preflight(cmd)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing PATH binary: err = %v, want exec.ErrNotFound", err)
	}
}

func TestPreflightUnixExecutableChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only executable checks")
	}
	dir := t.TempDir()

	// Non-executable file.
	nonExec := filepath.Join(dir, "nonexec")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(exec.Command(nonExec)); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("non-executable: err = %v, want os.ErrPermission", err)
	}

	// Empty 0-byte executable file.
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(exec.Command(empty)); !errors.Is(err, syscall.ENOEXEC) {
		t.Fatalf("empty executable: err = %v, want ENOEXEC", err)
	}

	// Broken symlink.
	brokenLink := filepath.Join(dir, "broken-link")
	if err := os.Symlink(filepath.Join(dir, "gone"), brokenLink); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(exec.Command(brokenLink)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("broken symlink: err = %v, want os.ErrNotExist", err)
	}

	// Broken shebang interpreter.
	badShebang := filepath.Join(dir, "bad-shebang")
	if err := os.WriteFile(badShebang, []byte("#!/nonexistent/path/to/node\nconsole.log(1)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(exec.Command(badShebang)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad shebang: err = %v, want os.ErrNotExist", err)
	}

	// Empty shebang line.
	emptyShebang := filepath.Join(dir, "empty-shebang")
	if err := os.WriteFile(emptyShebang, []byte("#!   \necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(exec.Command(emptyShebang)); !errors.Is(err, syscall.ENOEXEC) {
		t.Fatalf("empty shebang: err = %v, want ENOEXEC", err)
	}

	// Valid script with working directory.
	good := filepath.Join(dir, "good.sh")
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(good)
	cmd.Dir = dir
	out, err := Output(cmd)
	if err != nil {
		t.Fatalf("valid script failed: %v", err)
	}
	if string(out) != "ok\n" {
		t.Fatalf("valid script output = %q, want %q", string(out), "ok\n")
	}

	// Relative path resolved against cmd.Dir.
	relCmd := exec.Command("./good.sh")
	relCmd.Dir = dir
	if err := Preflight(relCmd); err != nil {
		t.Fatalf("relative cmd with Dir failed preflight: %v", err)
	}
}

func TestIsStartError(t *testing.T) {
	if IsStartError(nil) {
		t.Fatal("nil should not be a start error")
	}
	if IsStartError(context.Canceled) || IsStartError(context.DeadlineExceeded) {
		t.Fatal("context errors should not be start errors")
	}
	if !IsStartError(&os.PathError{Op: "fork/exec", Path: "/x", Err: syscall.ENOENT}) {
		t.Fatal("PathError should be a start error")
	}
	if runtime.GOOS != "windows" {
		err := Run(exec.Command("/bin/sh", "-c", "exit 3"))
		if err == nil {
			t.Fatal("expected exit 3 error")
		}
		if IsStartError(err) {
			t.Fatalf("non-zero exit %v should not be a start error", err)
		}
	}
}

func TestStartGateSuppressesUnchangedAndRecoversOnChange(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	gate := &StartGate{now: func() time.Time { return now }}

	if !gate.Allow(bin) {
		t.Fatal("fresh binary should be allowed")
	}
	gate.Record(bin, &os.PathError{Op: "fork/exec", Path: bin, Err: syscall.EPERM})
	if gate.Allow(bin) {
		t.Fatal("unchanged failed binary should be suppressed")
	}

	// Advance time: persistent failure remains suppressed until file changes.
	now = now.Add(2 * time.Hour)
	if gate.Allow(bin) {
		t.Fatal("persistent failure should stay suppressed while file is unchanged")
	}

	// Update file on disk: gate immediately allows retry.
	if err := os.WriteFile(bin, []byte("v2-updated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !gate.Allow(bin) {
		t.Fatal("updated binary should be allowed immediately")
	}

	// Transient error backs off temporarily and then allows again.
	gate.Record(bin, &os.PathError{Op: "fork/exec", Path: bin, Err: syscall.EAGAIN})
	if gate.Allow(bin) {
		t.Fatal("transient failure should back off initially")
	}
	now = now.Add(transientBackoff + time.Second)
	if !gate.Allow(bin) {
		t.Fatal("transient failure should recover after backoff")
	}
}
