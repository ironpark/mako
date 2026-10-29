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
}
