package lifecycle

import "os/exec"

// Windows has no process group to start a hook's command in here: mako
// does not yet put it in a job object. Its timeout, or its context's end,
// kills cmd, the shell, as exec's own cancellation does; a program the
// shell started runs on after that, and after the shell exits, and keeps
// the hook's output open, which Run stops reading outputGrace after the
// shell ended.

// ownGroup leaves cmd to exec's own cancellation, which kills the shell
// alone.
func ownGroup(*exec.Cmd) {}

// killGroup has no group to kill: what the shell started is not killed.
func killGroup(*exec.Cmd) error { return nil }
