package lifecycle

import "os/exec"

// ownGroup leaves cmd to exec's own cancellation on Windows, which kills
// the shell, not what the shell started.
func ownGroup(*exec.Cmd) {}

// killGroup has no group to kill on Windows.
func killGroup(*exec.Cmd) error { return nil }
