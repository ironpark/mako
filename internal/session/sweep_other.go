//go:build !darwin && !linux

package session

// sweep leaves what the session's program started where the processes of
// its terminal are not found here.
func sweep(string, int) {}
