package session

import (
	"os"

	"golang.org/x/sys/unix"
)

// processes are the ids of this user's processes running: another's it
// could neither read nor signal.
func processes() []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	uid := uint32(os.Getuid())
	var out []int
	for _, p := range procs {
		if p.Eproc.Ucred.Uid == uid {
			out = append(out, int(p.Proc.P_pid))
		}
	}
	return out
}

// environ is what can be read of the process's arguments and environment,
// each NUL-ended.
func environ(pid int) []byte {
	b, _ := unix.SysctlRaw("kern.procargs2", pid)
	return b
}
