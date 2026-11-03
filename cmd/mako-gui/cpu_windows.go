package main

import (
	"syscall"
	"time"
)

// processCPU is the CPU time the process has used, user and kernel.
func processCPU() time.Duration {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(self, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	// A Filetime counts 100-nanosecond intervals.
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration((ticks(kernel) + ticks(user)) * 100)
}
