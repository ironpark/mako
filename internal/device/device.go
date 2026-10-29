// Package device is the kinds of device a node lends to the nodes it lets
// in: how a kind finds the devices attached to the node, serves a
// borrower's connection to one, and tells a borrower's programs where it
// is lent. The daemon keeps the leases and carries the connections; see
// docs/devices.md.
package device

import (
	"context"
	"io"

	"github.com/ironpark/mako/internal/protocol"
)

// Kind is a kind of device.
type Kind interface {
	// List is the devices of the kind attached to this node, with no node
	// ID; none, and no error, when the kind's tools are not there.
	List(ctx context.Context) ([]*protocol.Device, error)
	// Serve serves one connection a borrower's program made to the device
	// of serial, attached to this node, until it ends.
	Serve(ctx context.Context, serial string, conn io.ReadWriteCloser) error
	// Env is what tells the kind's tools on a borrowing node that dev is
	// lent at addr there, host:port on its loopback.
	Env(addr string, dev *protocol.Device) map[string]string
	// Check is what on a borrowing node may keep its tools from working
	// with dev, for people to read.
	Check(ctx context.Context, dev *protocol.Device) []string
	// Exclusive reports whether one node at a time borrows a device.
	Exclusive() bool
	// Lend lends dev on a borrowing node, which reaches the device's node
	// through link, until the Lent is closed.
	Lend(ctx context.Context, dev *protocol.Device, link Link) Lent
}

// Lent is a device lent on a borrowing node.
type Lent interface {
	// Serve serves one connection a program there made to the lease's
	// port, until it ends.
	Serve(ctx context.Context, conn io.ReadWriteCloser) error
	// Close ends what the lending made, as ports forwarded.
	Close()
}

// Link is a borrowing node's way to the device's node, under its claim.
type Link interface {
	// Dial opens a connection to the device, which its node serves as
	// Kind.Serve does.
	Dial(ctx context.Context) (io.ReadWriteCloser, error)
	// Listen listens on a port of the device's node's loopback, for the
	// device to reach the borrowing node through.
	Listen(ctx context.Context) (Listener, error)
}

// Listener is a port of the device's node, listened on for a borrower.
type Listener interface {
	// Addr is where it listens on the device's node, as host:port.
	Addr() string
	// Accept waits for a connection to it.
	Accept(ctx context.Context) (io.ReadWriteCloser, error)
	Close() error
}
