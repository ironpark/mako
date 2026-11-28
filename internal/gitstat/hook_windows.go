//go:build windows

package gitstat

// openNonblock is nothing on Windows, whose files have no FIFO to wait on.
const openNonblock = 0
