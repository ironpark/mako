package sshremote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/safeexec"
)

// DefaultRemoteMakodPath is where Mako installs makod on a remote SSH host
// when no custom RemoteBinary path is configured.
const DefaultRemoteMakodPath = "~/.mako/bin/makod"

// InstallOptions customizes how InstallMakod builds or locates and installs
// the makod binary on a remote SSH host.
type InstallOptions struct {
	// ControlPath optionally reuses an existing SSH ControlMaster socket.
	ControlPath string
	// LocalBinary, if non-empty, uploads this prebuilt makod binary directly
	// without searching or cross-compiling.
	LocalBinary string
	// SourceDir optionally overrides the Mako Go module root used for
	// cross-compiling makod.
	SourceDir string
	// DestPath overrides the remote install path (defaults to host.RemoteBinary
	// or DefaultRemoteMakodPath).
	DestPath string
	// OnProgress, if non-nil, receives human-readable status updates for UI/CLI.
	OnProgress func(step string)
}

// InstallResult describes a completed remote makod installation.
type InstallResult struct {
	RemotePath string
	Version    string
	OS         string
	Arch       string
	Method     string // "upload-local", "cross-compile", or "remote-go-install"
	Duration   time.Duration
}

// InstallMakod installs makod onto the remote SSH host h using the best
// available strategy:
//  1. Upload an explicitly provided LocalBinary.
//  2. If remote OS/arch matches this machine, upload the local makod binary.
//  3. If the Mako source tree and Go toolchain are available locally,
//     cross-compile `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build ./cmd/makod`
//     and upload it over SSH stdin.
//  4. Otherwise, if `go` is installed on the remote host, run
//     `CGO_ENABLED=0 GOBIN=<dir> go install github.com/ironpark/mako/cmd/makod@latest`.
//
// After installation it verifies the remote binary executes via `-version`.
func InstallMakod(ctx context.Context, h config.SSHHost, opts InstallOptions) (InstallResult, error) {
	if err := h.Validate(); err != nil {
		return InstallResult{}, err
	}
	start := time.Now()
	progress := func(msg string) {
		if opts.OnProgress != nil {
			opts.OnProgress(msg)
		}
	}

	progress("Probing remote host…")
	probe, err := Probe(ctx, h, opts.ControlPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("probe remote host: %w", err)
	}
	if probe.OS == "" || probe.Arch == "" {
		return InstallResult{}, fmt.Errorf("could not determine remote OS/arch (%q/%q)", probe.OS, probe.Arch)
	}
	if probe.OS == "windows" {
		return InstallResult{}, errors.New("automatic remote makod installation over SSH is currently supported on Linux, macOS, and BSD hosts")
	}

	dest := strings.TrimSpace(opts.DestPath)
	if dest == "" {
		dest = strings.TrimSpace(h.RemoteBinary)
	}
	if dest == "" {
		dest = DefaultRemoteMakodPath
	}
	expandedDest := ExpandRemotePath(dest, probe.Home)

	var method string
	var uploadPath string
	var cleanup func()

	switch {
	case strings.TrimSpace(opts.LocalBinary) != "":
		uploadPath = expandLocalHome(strings.TrimSpace(opts.LocalBinary))
		if st, err := os.Stat(uploadPath); err != nil || st.IsDir() {
			return InstallResult{}, fmt.Errorf("local binary %q is not a readable file", uploadPath)
		}
		method = "upload-local"

	case probe.OS == runtime.GOOS && probe.Arch == runtime.GOARCH:
		if localBin := findLocalMakod(); localBin != "" {
			uploadPath = localBin
			method = "upload-local"
		}
	}

	if uploadPath == "" {
		if srcDir := findLocalSourceDir(opts.SourceDir); srcDir != "" {
			if goBin, err := exec.LookPath("go"); err == nil {
				progress(fmt.Sprintf("Building makod for %s/%s…", probe.OS, probe.Arch))
				tmpBin, clean, err := crossCompileMakod(ctx, goBin, srcDir, probe.OS, probe.Arch)
				if err != nil {
					return InstallResult{}, fmt.Errorf("cross-compile makod for %s/%s: %w", probe.OS, probe.Arch, err)
				}
				uploadPath = tmpBin
				cleanup = clean
				method = "cross-compile"
			}
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	if uploadPath != "" {
		progress(fmt.Sprintf("Uploading makod to %s…", dest))
		if err := uploadMakodBinary(ctx, h, opts.ControlPath, uploadPath, expandedDest); err != nil {
			return InstallResult{}, fmt.Errorf("upload makod: %w", err)
		}
	} else {
		progress("Installing makod via remote Go toolchain…")
		if err := installMakodViaRemoteGo(ctx, h, opts.ControlPath, expandedDest); err != nil {
			return InstallResult{}, err
		}
		method = "remote-go-install"
	}

	progress("Verifying remote makod…")
	version, err := verifyRemoteMakod(ctx, h, opts.ControlPath, expandedDest)
	if err != nil {
		return InstallResult{}, fmt.Errorf("verify remote makod: %w", err)
	}

	return InstallResult{
		RemotePath: expandedDest,
		Version:    version,
		OS:         probe.OS,
		Arch:       probe.Arch,
		Method:     method,
		Duration:   time.Since(start),
	}, nil
}

func findLocalMakod() string {
	name := "makod"
	if runtime.GOOS == "windows" {
		name = "makod.exe"
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, name),
			filepath.Join(dir, "..", "bin", name),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "bin", name))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func findLocalSourceDir(override string) string {
	var candidates []string
	if override != "" {
		candidates = append(candidates, override)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		// internal/sshremote/install.go -> repo root is ../..
		candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")))
	}
	for _, dir := range candidates {
		if isMakoSourceRoot(dir) {
			return dir
		}
	}
	return ""
}

func isMakoSourceRoot(dir string) bool {
	if dir == "" {
		return false
	}
	modBytes, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || !strings.Contains(string(modBytes), "module github.com/ironpark/mako") {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "cmd", "makod", "main.go"))
	return err == nil && !st.IsDir()
}

func crossCompileMakod(ctx context.Context, goBin, srcDir, targetOS, targetArch string) (binPath string, cleanup func(), err error) {
	tmpDir, err := os.MkdirTemp("", "mako-ssh-build-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }
	outPath := filepath.Join(tmpDir, "makod")
	ldflags := fmt.Sprintf("-X github.com/ironpark/mako/internal/config.Version=%s", config.Version)
	cmd := exec.CommandContext(ctx, goBin, "build", "-ldflags", ldflags, "-o", outPath, "./cmd/makod")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+targetOS,
		"GOARCH="+targetArch,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := safeexec.Run(cmd); err != nil {
		cleanup()
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", nil, fmt.Errorf("%w: %s", err, msg)
		}
		return "", nil, err
	}
	return outPath, cleanup, nil
}

func uploadMakodBinary(ctx context.Context, h config.SSHHost, controlPath, localPath, remoteDest string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Stop any older ~/.mako-ssh daemon so replacing the binary is clean and the
	// next reconnect starts the updated binary, then stream atomically into place.
	script := fmt.Sprintf(`set -e
_dst=%s
case "$_dst" in '~') _dst="$HOME/makod";; '~/'*) _dst="$HOME/${_dst#~/}";; esac
_dir="$(dirname -- "$_dst")"
mkdir -p -- "$_dir"
if [ -S "$HOME/.mako-ssh/run/daemon.sock" ]; then
  pkill -f "$_dst" >/dev/null 2>&1 || true
  rm -f "$HOME/.mako-ssh/run/daemon.sock"
fi
_tmp="${_dst}.tmp.$$"
trap 'rm -f "$_tmp"' EXIT
cat > "$_tmp"
chmod 755 "$_tmp"
mv -f -- "$_tmp" "$_dst"
trap - EXIT`, ShellQuote(remoteDest))

	_, err = runSSHStdin(ctx, h, controlPath, script, f)
	return err
}

func installMakodViaRemoteGo(ctx context.Context, h config.SSHHost, controlPath, remoteDest string) error {
	script := fmt.Sprintf(`%s
set -e
if ! command -v go >/dev/null 2>&1; then
  echo "neither local Mako source nor remote 'go' toolchain is available to install makod" >&2
  exit 1
fi
_dst=%s
case "$_dst" in '~') _dst="$HOME/makod";; '~/'*) _dst="$HOME/${_dst#~/}";; esac
_dir="$(dirname "$_dst")"
mkdir -p "$_dir"
CGO_ENABLED=0 GOBIN="$_dir" go install github.com/ironpark/mako/cmd/makod@latest
if [ "$_dir/makod" != "$_dst" ] && [ -x "$_dir/makod" ]; then
  mv -f "$_dir/makod" "$_dst"
fi`, remotePathPrelude, ShellQuote(remoteDest))

	_, err := runSSH(ctx, h, controlPath, script)
	return err
}

func verifyRemoteMakod(ctx context.Context, h config.SSHHost, controlPath, remoteDest string) (string, error) {
	script := fmt.Sprintf(`_dst=%s
case "$_dst" in '~') _dst="$HOME/makod";; '~/'*) _dst="$HOME/${_dst#~/}";; esac
[ -x "$_dst" ] || { echo "makod binary not executable at $_dst" >&2; exit 1; }
"$_dst" -version`, ShellQuote(remoteDest))

	out, err := runSSH(ctx, h, controlPath, script)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
