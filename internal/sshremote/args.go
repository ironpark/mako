package sshremote

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/config"
)

// SSHBinary is the OpenSSH client binary invoked by sshremote. Tests may
// override it to point at a fake ssh wrapper script.
var SSHBinary = "ssh"

// Options controls SSH command construction.
type Options struct {
	// ControlPath enables OpenSSH connection multiplexing when non-empty.
	ControlPath string
	// Interactive omits BatchMode=yes so interactive TTY sessions can run.
	Interactive bool
	// ForceTTY passes -tt to allocate a remote PTY.
	ForceTTY bool
	// ConnectTimeoutSeconds overrides ConnectTimeout (default 10).
	ConnectTimeoutSeconds int
	// NoControlMaster disables ControlMaster/ControlPersist flags (e.g. for
	// dedicated -N -L tunnel processes).
	NoControlMaster bool
}

// ControlPath returns a short Unix socket path under runDir for multiplexing
// SSH connections to hostID, kept safely under macOS's 104-byte sun_path limit.
func ControlPath(runDir, hostID string) string {
	if runDir == "" || runtime.GOOS == "windows" {
		return ""
	}
	sum := sha256.Sum256([]byte("ctrl:" + hostID))
	return filepath.Join(runDir, "ssh-c-"+hex.EncodeToString(sum[:6])+".sock")
}

// TunnelSocketPath returns a short local Unix socket path under runDir for
// forwarding a remote makod daemon's Unix socket.
func TunnelSocketPath(runDir, hostID string) string {
	sum := sha256.Sum256([]byte("tun:" + hostID))
	return filepath.Join(runDir, "ssh-d-"+hex.EncodeToString(sum[:6])+".sock")
}

// Target returns the SSH destination argument for h ("user@host" or "host").
func Target(h config.SSHHost) string {
	host := strings.TrimSpace(h.Host)
	user := strings.TrimSpace(h.User)
	if user != "" && !strings.Contains(host, "@") {
		return user + "@" + host
	}
	return host
}

// BaseArgs builds the OpenSSH CLI arguments for h up to and including
// "-- <target>". Remote command tokens may be appended after this slice.
func BaseArgs(h config.SSHHost, opts Options) ([]string, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	timeout := opts.ConnectTimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	args := make([]string, 0, 24)
	if opts.ForceTTY {
		args = append(args, "-tt")
	}
	if !opts.Interactive {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args,
		"-o", fmt.Sprintf("ConnectTimeout=%d", timeout),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	)
	if opts.ControlPath != "" && !opts.NoControlMaster && runtime.GOOS != "windows" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-S", controlPathArg(opts.ControlPath),
			"-o", "ControlPersist=10m",
		)
	}
	if h.Port > 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	if id := expandLocalHome(strings.TrimSpace(h.IdentityFile)); id != "" {
		args = append(args, "-i", id, "-o", "IdentitiesOnly=yes")
	}
	if jump := strings.TrimSpace(h.ProxyJump); jump != "" {
		args = append(args, "-J", jump)
	}
	if len(h.ExtraArgs) > 0 {
		args = append(args, h.ExtraArgs...)
	}
	args = append(args, "--", Target(h))
	return args, nil
}

// controlPathArg is controlPath as ssh's -S flag takes it. Unlike
// -o ControlPath, which ssh splits into words as a config line, -S keeps
// the path whole, spaces and all: on macOS the run folder is in
// "Application Support". ssh still expands % tokens in it, so a literal %
// is doubled.
func controlPathArg(controlPath string) string {
	return strings.ReplaceAll(controlPath, "%", "%%")
}

// LookSSH returns the resolved ssh executable path.
func LookSSH() (string, error) {
	return exec.LookPath(SSHBinary)
}

// ShellQuote quotes s for safe interpolation in a POSIX /bin/sh command.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin quotes every argument and joins them with spaces for POSIX sh.
func ShellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}

// ExpandRemotePath expands a leading "~" or "~/" against remoteHome.
func ExpandRemotePath(p, remoteHome string) string {
	p = strings.TrimSpace(p)
	if remoteHome == "" {
		return p
	}
	if p == "" || p == "~" {
		return remoteHome
	}
	if strings.HasPrefix(p, "~/") {
		return strings.TrimRight(remoteHome, "/") + "/" + strings.TrimLeft(p[2:], "/")
	}
	return p
}

func expandLocalHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// CloseControlMaster cleanly tears down a multiplexed SSH ControlMaster socket
// if one exists at controlPath.
func CloseControlMaster(h config.SSHHost, controlPath string) {
	if controlPath == "" || runtime.GOOS == "windows" {
		return
	}
	if _, err := os.Stat(controlPath); err != nil {
		return
	}
	args := []string{"-S", controlPathArg(controlPath), "-O", "exit", "--", Target(h)}
	_ = exec.Command(SSHBinary, args...).Run()
	_ = os.Remove(controlPath)
}

// CleanSSHError distills verbose OpenSSH stderr into a concise single-line
// user-facing diagnostic.
func CleanSSHError(stderr string, fallback error) string {
	var best string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip benign banner/warning lines when a concrete error follows.
		if strings.HasPrefix(line, "Warning: Permanently added") {
			continue
		}
		best = line
	}
	if best != "" {
		return best
	}
	if fallback != nil {
		return fallback.Error()
	}
	return "SSH connection failed"
}
