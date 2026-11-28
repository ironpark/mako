//go:build !windows

package gitstat

import "syscall"

// openNonblock opens a FIFO without waiting for a writer, so that what is
// opened is seen to be no regular file and refused.
const openNonblock = syscall.O_NONBLOCK
