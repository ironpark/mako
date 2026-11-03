package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// shellCommand runs command as Claude Code runs a status line, in Git
// Bash, else in cmd.exe, which has the command line as it is written.
func shellCommand(command string) *exec.Cmd {
	if bash := gitBash(); bash != "" {
		return exec.Command(bash, "-c", command)
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`}
	return cmd
}

// gitBash is Git for Windows' bash, as Claude Code finds it: where
// CLAUDE_CODE_GIT_BASH_PATH says, else in the installation git runs from,
// "" for none. The bash.exe in System32 is WSL's, which is not it.
func gitBash() string {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		return p
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	// git.exe sits in cmd, bin or mingw64\bin of the installation.
	dir := filepath.Dir(git)
	for range 3 {
		for _, bash := range []string{filepath.Join(dir, "bin", "bash.exe"), filepath.Join(dir, "usr", "bin", "bash.exe")} {
			if _, err := os.Stat(bash); err == nil {
				return bash
			}
		}
		dir = filepath.Dir(dir)
	}
	return ""
}
