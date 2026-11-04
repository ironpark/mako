//go:build darwin || linux

package session

import (
	"bytes"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// sweepStep is how long a process the sweep signalled has to exit before
// it is signalled again, harder.
const sweepStep = time.Second

// sweep ends every process the session's program started, wherever it
// went: hung up at once, then, as long as any stays, terminated and
// killed. Each round finds them anew, so a process id reused meanwhile is
// not signalled.
func sweep(scope string, leader int) {
	if signalSwept(scope, leader, unix.SIGHUP) {
		go func() {
			for _, sig := range []unix.Signal{unix.SIGTERM, unix.SIGKILL} {
				time.Sleep(sweepStep)
				if !signalSwept(scope, leader, sig) {
					return
				}
			}
		}()
	}
}

func signalSwept(scope string, leader int, sig unix.Signal) bool {
	pids := swept(scope, leader)
	for _, pid := range pids {
		_ = unix.Kill(pid, sig)
	}
	return len(pids) > 0
}

// swept are the processes the session's program started: those of the
// terminal's session, which the program led, and those whose environment
// holds the session's scope, as one that left the session keeps. A process
// with the leader's id is not its, the leader gone: one that took its id.
// Some environments cannot be read, as macOS keeps its own programs'.
func swept(scope string, leader int) []int {
	want := []byte(scopeEnv + "=" + scope + "\x00")
	self := os.Getpid()
	var out []int
	for _, pid := range processes() {
		if pid <= 1 || pid == self || pid == leader {
			continue
		}
		if sid, err := unix.Getsid(pid); (err == nil && sid == leader) || hasEntry(environ(pid), want) {
			out = append(out, pid)
		}
	}
	return out
}

// hasEntry reports whether the NUL-separated block holds the entry want,
// NUL-ended.
func hasEntry(block, want []byte) bool {
	for i := 0; ; {
		j := bytes.Index(block[i:], want)
		if j < 0 {
			return false
		}
		if at := i + j; at == 0 || block[at-1] == 0 {
			return true
		}
		i += j + 1
	}
}
