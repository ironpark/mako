package agentspec

import "syscall"

// shortPath is path's 8.3 short name, without spaces, or "" for none: a
// path that is not there, or a volume that keeps no short names.
func shortPath(path string) string {
	long, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 260)
	for {
		n, err := syscall.GetShortPathName(long, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return ""
		}
		if int(n) <= len(buf) {
			return syscall.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n)
	}
}
