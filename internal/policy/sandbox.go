package policy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Sandbox modes supported on Agent / Profile configurations.
const (
	SandboxOff       = ""
	SandboxWorkspace = "workspace"
)

// BuildSeatbeltProfile constructs a macOS sandbox-exec (Seatbelt) profile that
// allows reading files and executing tools while confining filesystem writes to
// projectDir, extraWritableDirs, and standard temp/device paths.
func BuildSeatbeltProfile(projectDir string, extraWritableDirs []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	writeSubpath := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		escaped := strings.ReplaceAll(clean, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		fmt.Fprintf(&b, "  (subpath \"%s\")\n", escaped)
		if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
			re := strings.ReplaceAll(real, `\`, `\\`)
			re = strings.ReplaceAll(re, `"`, `\"`)
			fmt.Fprintf(&b, "  (subpath \"%s\")\n", re)
		}
	}
	writeSubpath(projectDir)
	for _, d := range extraWritableDirs {
		writeSubpath(d)
	}
	for _, sysDir := range []string{"/tmp", "/private/tmp", "/private/var/folders", "/dev"} {
		writeSubpath(sysDir)
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		writeSubpath(tmp)
	}
	b.WriteString(")\n")
	return b.String()
}

// DefaultAgentWritableDirs returns the user-home agent state directories that
// CLI agents (Claude, Codex, agy, OpenCode, Mako) need write access to when
// running inside a workspace sandbox.
func DefaultAgentWritableDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".gemini"),
		filepath.Join(home, ".mako"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, "Library", "Application Support", "mako"),
		filepath.Join(home, "Library", "Caches"),
	}
	var existing []string
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			existing = append(existing, d)
		}
	}
	return existing
}

// WrapCommand wraps cmd in an OS-level workspace sandbox when mode is
// SandboxWorkspace. It returns the wrapped command and true if sandboxing was
// applied; if the host lacks the platform sandbox binary, it returns the
// original command and false along with a diagnostic note.
func WrapCommand(mode, projectDir string, extraWritable []string, cmd []string) ([]string, bool, string) {
	if strings.TrimSpace(mode) != SandboxWorkspace || len(cmd) == 0 || strings.TrimSpace(projectDir) == "" {
		return cmd, false, ""
	}
	switch runtime.GOOS {
	case "darwin":
		bin, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return cmd, false, "sandbox-exec not found; running without OS sandbox"
		}
		profile := BuildSeatbeltProfile(projectDir, extraWritable)
		wrapped := append([]string{bin, "-p", profile, "--"}, cmd...)
		return wrapped, true, ""
	case "linux":
		bin, err := exec.LookPath("bwrap")
		if err != nil {
			return cmd, false, "bwrap (bubblewrap) not found; running without OS sandbox"
		}
		args := []string{
			bin,
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--tmpfs", "/tmp",
			"--bind", projectDir, projectDir,
		}
		for _, d := range extraWritable {
			if _, err := os.Stat(d); err == nil {
				args = append(args, "--bind", d, d)
			}
		}
		args = append(args, "--")
		args = append(args, cmd...)
		return args, true, ""
	default:
		return cmd, false, fmt.Sprintf("OS workspace sandbox is not supported on %s", runtime.GOOS)
	}
}
