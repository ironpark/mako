package session

import (
	"os"
	"strconv"
)

// processes are the ids of the processes running.
func processes() []int {
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// environ is the process's environment, each entry NUL-ended.
func environ(pid int) []byte {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return b
}
