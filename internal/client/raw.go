//go:build unix

package client

import (
	"os"

	"golang.org/x/term"
)

// makeRaw puts the terminal in raw mode and returns a restore function.
func makeRaw(f *os.File) (func(), error) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, state) }, nil
}
