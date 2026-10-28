package transport

import (
	"context"
	"errors"
	"io"
)

// Splice copies a to b and b to a until both have ended, or ctx is done,
// then closes both. A side that ends what it sends has the other's write
// half closed, where the connection can close only that, so a program that
// waits for the end of a request still hears it.
func Splice(ctx context.Context, a, b io.ReadWriteCloser) error {
	closeBoth := func() { a.Close(); b.Close() }
	defer closeBoth()
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	errs := make(chan error, 2)
	pipe := func(dst, src io.ReadWriteCloser) {
		_, err := io.Copy(dst, src)
		if err != nil || !closeWrite(dst) {
			closeBoth() // failed, or no half to close: both end
		}
		errs <- err
	}
	go pipe(b, a)
	go pipe(a, b)
	return errors.Join(<-errs, <-errs)
}

// closeWrite closes rw's write half, and reports whether it has one of
// its own to close.
func closeWrite(rw io.ReadWriteCloser) bool {
	if s, ok := rw.(*Stream); ok {
		rw = s.ReadWriteCloser
	}
	c, ok := rw.(interface{ CloseWrite() error })
	return ok && c.CloseWrite() == nil
}

func (c *closing) CloseWrite() error {
	if w, ok := c.ReadWriteCloser.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return errors.ErrUnsupported
}
