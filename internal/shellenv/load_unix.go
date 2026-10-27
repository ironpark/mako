//go:build !windows

package shellenv

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// timeout bounds the shell's start, for a profile that waits on something.
const timeout = 5 * time.Second

// Load sets this process's environment to the login shell's, when the
// desktop started it: a terminal sets TERM, and its environment is the
// shell's already. A shell that fails or takes too long leaves the
// environment as it was.
func Load() {
	if os.Getenv("TERM") != "" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Interactive as well as login, so what the rc files set comes too.
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c",
		"printf '%s' "+marker+"; /usr/bin/env -0; printf '%s' "+marker)
	cmd.Env = append(os.Environ(), "MAKO_SHELL_ENV=1")
	// Without a terminal of its own, so the shell cannot take this one's.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// A child the profile leaves holding the output does not hold the wait.
	cmd.WaitDelay = 500 * time.Millisecond
	out, _ := cmd.Output()
	for k, v := range parse(out) {
		_ = os.Setenv(k, v)
	}
}
