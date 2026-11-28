//go:build !windows

package gitstat

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// openNonblock opens a FIFO without waiting for a writer, so that what is
// opened is seen to be no regular file and refused.
const openNonblock = syscall.O_NONBLOCK

// hookGroup starts the hook cmd runs in a process group of its own, and
// has its context ending kill the group: the shell and whatever it
// started, which would otherwise outlive it.
func hookGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
