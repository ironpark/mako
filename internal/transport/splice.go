package transport

import (
	"context"
	"errors"
	"io"

	"github.com/ironpark/mako/internal/protocol"
)

// Splice copies a to b and b to a until both have ended, or ctx is done,
// then closes both. A side that ends what it sends has the other's write
// half closed, where the connection can close only that, so a program that
// waits for the end of a request still hears it.
func Splice(ctx context.Context, a, b io.ReadWriteCloser) error {
	return splice(ctx, a, b, copyAll)
}

// SpliceUnpacking is Splice for a, a front-end's socket, and b, a peer's
// stream of frames: b's frames reach a decompressed, as the socket
// carries them.
func SpliceUnpacking(ctx context.Context, a, b io.ReadWriteCloser) error {
	return splice(ctx, a, b, protocol.UnpackFrames)
}

func copyAll(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	return err
}

// splice is Splice, copying b to a with fromB.
func splice(ctx context.Context, a, b io.ReadWriteCloser, fromB func(dst io.Writer, src io.Reader) error) error {
	closeBoth := func() { a.Close(); b.Close() }
	defer closeBoth()
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	errs := make(chan error, 2)
	pipe := func(dst, src io.ReadWriteCloser, move func(io.Writer, io.Reader) error) {
		err := move(dst, src)
		if err != nil || !closeWrite(dst) {
			closeBoth() // failed, or no half to close: both end
		}
		errs <- err
	}
	go pipe(b, a, copyAll)
	go pipe(a, b, fromB)
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
