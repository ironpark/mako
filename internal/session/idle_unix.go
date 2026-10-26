//go:build unix

package session

import (
	"os"

	"golang.org/x/sys/unix"
)

// foregroundIsSelf reports whether the terminal's foreground process
// group is the session's own program, as a shell's is at its prompt and
// is not while a command it started runs.
func foregroundIsSelf(tty *os.File, pid int) bool {
	raw, err := tty.SyscallConn()
	if err != nil {
		return false
	}
	pgrp := -1
	// Control, rather than Fd, leaves the file non-blocking for the pump.
	_ = raw.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp = v
		}
	})
	return pgrp == pid
}
