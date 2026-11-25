//go:build !windows

package safeexec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxShebangDepth = 4

func checkDirAccess(dir string) error {
	if err := unix.Access(dir, unix.X_OK); err != nil {
		return &os.PathError{Op: "chdir", Path: dir, Err: err}
	}
	return nil
}

func checkExecutable(path, dir string, depth int) error {
	if depth > maxShebangDepth {
		return &os.PathError{Op: "fork/exec", Path: path, Err: syscall.ELOOP}
	}
	fi, err := os.Stat(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return &os.PathError{Op: "fork/exec", Path: path, Err: pe.Err}
		}
		return &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	if !fi.Mode().IsRegular() {
		return &os.PathError{Op: "fork/exec", Path: path, Err: syscall.EACCES}
	}
	if err := unix.Access(path, unix.X_OK); err != nil {
		return &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	if fi.Size() == 0 {
		return &os.PathError{Op: "fork/exec", Path: path, Err: syscall.ENOEXEC}
	}
	return checkShebang(path, dir, depth)
}

func checkShebang(path, dir string, depth int) error {
	f, err := os.Open(path)
	if err != nil {
		// Execute-only binaries (mode 0111) cannot be opened for reading but
		// can still be executed by the kernel.
		if errors.Is(err, os.ErrPermission) {
			return nil
		}
		return &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	var head [256]byte
	n, err := io.ReadFull(f, head[:])
	_ = f.Close()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	if n < 2 || head[0] != '#' || head[1] != '!' {
		return nil
	}
	line := head[2:n]
	if i := bytes.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	trimmed := strings.TrimLeft(string(line), " \t")
	if trimmed == "" {
		return &os.PathError{Op: "fork/exec", Path: path, Err: syscall.ENOEXEC}
	}
	interp := trimmed
	if i := strings.IndexAny(interp, " \t"); i >= 0 {
		interp = interp[:i]
	}
	if !filepath.IsAbs(interp) && dir != "" {
		interp = filepath.Join(dir, interp)
	}
	if err := checkExecutable(interp, dir, depth+1); err != nil {
		return badInterpreterError(path, interp, err)
	}
	return nil
}

func isTransientStartError(err error) bool {
	return errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.ENOMEM) ||
		errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EINTR)
}
