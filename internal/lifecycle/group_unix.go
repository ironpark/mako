//go:build !windows

package lifecycle

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, which its timeout or
// its context's end kills whole: the shell and whatever it started.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
}

// killGroup kills cmd's process group, os.ErrProcessDone when nothing of
// it is left.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
