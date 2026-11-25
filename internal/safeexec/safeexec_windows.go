//go:build windows

package safeexec

import (
	"errors"
	"os"
	"syscall"
)

func checkDirAccess(string) error {
	return nil
}

func checkExecutable(path, _ string, _ int) error {
	fi, err := os.Stat(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return &os.PathError{Op: "fork/exec", Path: path, Err: pe.Err}
		}
		return &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	if fi.IsDir() {
		return &os.PathError{Op: "fork/exec", Path: path, Err: syscall.EACCES}
	}
	return nil
}

func isTransientStartError(error) bool {
	return false
}
