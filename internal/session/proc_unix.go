//go:build unix

package session

import (
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// proc is the agent's process on a PTY: reading it reads the terminal's
// output, and writing it types.
type proc struct {
	cmd *exec.Cmd
	tty *os.File
}

// startProc starts argv in dir with env, on a terminal cols by rows.
func startProc(argv []string, dir string, env []string, cols, rows uint16) (*proc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &proc{cmd: cmd, tty: tty}, nil
}

func (p *proc) Read(b []byte) (int, error)  { return p.tty.Read(b) }
func (p *proc) Write(b []byte) (int, error) { return p.tty.Write(b) }

// Close closes the terminal.
func (p *proc) Close() error { return p.tty.Close() }

func (p *proc) resize(cols, rows uint16) error {
	return pty.Setsize(p.tty, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) kill() { _ = p.cmd.Process.Kill() }

// wait is the process's exit code once it exits, -1 when it says none.
func (p *proc) wait() int64 {
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return int64(exitErr.ExitCode())
	}
	return -1
}

// idle reports whether the terminal's foreground process group is the
// session's own program, as a shell's is at its prompt and is not while a
// command it started runs.
func (p *proc) idle() bool {
	raw, err := p.tty.SyscallConn()
	if err != nil {
		return false
	}
	pgrp := -1
	// Control, rather than Fd, leaves the file non-blocking for the pump.
	_ = raw.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp = v
		}
	})
	return pgrp == p.pid()
}
