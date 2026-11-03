//go:build !windows

package daemon

import "os/exec"

// shellCommand runs command as the agents run a status line, in sh.
func shellCommand(command string) *exec.Cmd { return exec.Command("sh", "-c", command) }
