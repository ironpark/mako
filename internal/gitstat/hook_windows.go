//go:build windows

package gitstat

import "os/exec"

// openNonblock is nothing on Windows, whose files have no FIFO to wait on.
const openNonblock = 0

// hookGroup leaves cmd as it is on Windows: its context ending kills cmd
// itself.
func hookGroup(*exec.Cmd) {}
