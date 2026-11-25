// Package safeexec validates subprocess launches before Go calls fork(2).
//
// On macOS, Go's syscall.forkAndExecInChild exits a failed post-fork child
// through libc exit(3) rather than _exit(2). Inside a multi-threaded GUI
// process (mako-gui with AppKit, CoreFoundation, PowerLog, libc++), running
// C/C++ atexit destructors in the forked child crashes pre-exec with SIGSEGV.
// Checking the working directory, executable, and any #! interpreter up front
// catches common failure modes before fork(2), and StartGate prevents periodic
// callers from repeatedly retrying an unchanged binary that failed to start.
package safeexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Preflight checks that cmd's working directory and executable are ready to
// run before fork(2) is called. If cmd already carries a lookup error, that
// error is returned unchanged.
func Preflight(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("safeexec: nil command")
	}
	if cmd.Err != nil {
		return cmd.Err
	}
	if cmd.Dir != "" {
		if err := CheckDir(cmd.Dir); err != nil {
			return err
		}
	}
	path := cmd.Path
	if path == "" {
		return &os.PathError{Op: "fork/exec", Path: "", Err: syscall.ENOENT}
	}
	if !filepath.IsAbs(path) {
		if hasPathSeparator(path) {
			if cmd.Dir != "" {
				path = filepath.Join(cmd.Dir, path)
			}
		} else {
			lp, err := exec.LookPath(path)
			if err != nil {
				return err
			}
			path = lp
		}
	}
	return checkExecutable(path, cmd.Dir, 0)
}

// CheckDir reports whether dir exists, is a directory, and can be entered.
func CheckDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return &os.PathError{Op: "chdir", Path: dir, Err: pe.Err}
		}
		return &os.PathError{Op: "chdir", Path: dir, Err: err}
	}
	if !fi.IsDir() {
		return &os.PathError{Op: "chdir", Path: dir, Err: syscall.ENOTDIR}
	}
	return checkDirAccess(dir)
}

// CheckExecutable reports whether path points to a runnable executable (and on
// Unix, that any #! script interpreter it names is also runnable).
func CheckExecutable(path string) error {
	if path == "" {
		return &os.PathError{Op: "fork/exec", Path: "", Err: syscall.ENOENT}
	}
	return checkExecutable(path, "", 0)
}

// Start runs Preflight before cmd.Start.
func Start(cmd *exec.Cmd) error {
	if err := Preflight(cmd); err != nil {
		return err
	}
	return cmd.Start()
}

// Run runs Preflight before cmd.Run.
func Run(cmd *exec.Cmd) error {
	if err := Preflight(cmd); err != nil {
		return err
	}
	return cmd.Run()
}

// Output runs Preflight before cmd.Output.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if err := Preflight(cmd); err != nil {
		return nil, err
	}
	return cmd.Output()
}

// CombinedOutput runs Preflight before cmd.CombinedOutput.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if err := Preflight(cmd); err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// IsStartError reports whether err indicates that the subprocess failed to
// start (pre-exec or lookup/preflight failure) rather than exiting with a
// non-zero status or failing due to context cancellation/timeout.
func IsStartError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func hasPathSeparator(p string) bool {
	return strings.ContainsRune(p, filepath.Separator) || strings.ContainsRune(p, '/')
}

type binaryFingerprint struct {
	exists  bool
	size    int64
	mode    os.FileMode
	modNano int64
}

func statFingerprint(path string) binaryFingerprint {
	fi, err := os.Stat(path)
	if err != nil {
		return binaryFingerprint{}
	}
	return binaryFingerprint{
		exists:  true,
		size:    fi.Size(),
		mode:    fi.Mode(),
		modNano: fi.ModTime().UnixNano(),
	}
}

type blockedBinary struct {
	fp    binaryFingerprint
	until time.Time // zero means blocked until the file fingerprint changes
}

// StartGate suppresses repeated launches of an unchanged binary that failed to
// start (e.g. Gatekeeper, wrong CPU architecture, or broken interpreter), while
// automatically retrying as soon as the binary changes on disk.
type StartGate struct {
	mu      sync.Mutex
	blocked map[string]blockedBinary
	now     func() time.Time // test hook; defaults to time.Now
}

const transientBackoff = time.Minute

func (g *StartGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// Allow reports whether path should be attempted. It returns false when path
// previously failed to start and has not changed on disk (or is still within
// transient backoff).
func (g *StartGate) Allow(path string) bool {
	if path == "" {
		return false
	}
	fp := statFingerprint(path)
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.blocked[path]
	if !ok {
		return true
	}
	if b.fp != fp {
		delete(g.blocked, path)
		return true
	}
	if !b.until.IsZero() && !g.clock().Before(b.until) {
		delete(g.blocked, path)
		return true
	}
	return false
}

// Record updates gate state after running cmd at path. A nil error or normal
// post-exec exit clears any prior block; a start error blocks retries until
// the binary changes on disk (or for a brief backoff on transient resource
// errors such as EAGAIN/EMFILE).
func (g *StartGate) Record(path string, err error) {
	if path == "" {
		return
	}
	if !IsStartError(err) {
		g.mu.Lock()
		delete(g.blocked, path)
		g.mu.Unlock()
		return
	}
	entry := blockedBinary{fp: statFingerprint(path)}
	if isTransientStartError(err) {
		entry.until = g.clock().Add(transientBackoff)
	}
	g.mu.Lock()
	if g.blocked == nil {
		g.blocked = map[string]blockedBinary{}
	}
	g.blocked[path] = entry
	g.mu.Unlock()
}

func badInterpreterError(script, interp string, err error) error {
	return fmt.Errorf("fork/exec %s: bad interpreter %s: %w", script, interp, err)
}
