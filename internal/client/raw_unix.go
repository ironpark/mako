//go:build unix

package client

import (
	"context"
	"os"
	"os/signal"
	"syscall"

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

// watchResize signals each time the terminal's window changes size, on
// SIGWINCH, until ctx ends.
func watchResize(ctx context.Context) <-chan struct{} {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
