package client

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// makeRaw puts the console in raw mode, its keys read as the escape
// sequences a Unix terminal sends, and has it interpret the ones the
// session writes; it returns a restore function.
func makeRaw(f *os.File) (func(), error) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	out := windows.Handle(os.Stdout.Fd())
	var mode uint32
	restoreOut := func() {}
	if windows.GetConsoleMode(out, &mode) == nil {
		if windows.SetConsoleMode(out, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN) == nil {
			restoreOut = func() { _ = windows.SetConsoleMode(out, mode) }
		}
	}
	return func() {
		restoreOut()
		_ = term.Restore(fd, state)
	}, nil
}

// resizePoll is how often the console's size is checked: Windows sends no
// signal when it changes.
const resizePoll = 250 * time.Millisecond

// watchResize signals each time the console's window changes size, until
// ctx ends.
func watchResize(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(resizePoll)
		defer t.Stop()
		cols, rows, _ := localSize()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c, r, ok := localSize()
				if !ok || (c == cols && r == rows) {
					continue
				}
				cols, rows = c, r
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
