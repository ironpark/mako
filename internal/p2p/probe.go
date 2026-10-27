package p2p

import (
	"context"
	"net"
	"sync"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multistream"
)

// prober is the TLS transport probes open their handshakes with, under a
// key of the process's: a probe is no host's.
var prober = sync.OnceValues(func() (*libp2ptls.Transport, error) {
	key, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	return libp2ptls.New(libp2ptls.ID, key, nil)
})

// Probe learns which host listens for libp2p at addr, a host and its TCP
// port, as discovery finds an address with no peer ID: it opens libp2p's
// TLS handshake there, which shows the host's peer ID and proves it, and
// hangs up. Linking to the host then holds it to that ID. ctx bounds it.
func Probe(ctx context.Context, addr string) (peer.ID, error) {
	tpt, err := prober()
	if err != nil {
		return "", err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := multistream.SelectProtoOrFail(libp2ptls.ID, conn); err != nil {
		return "", err
	}
	sc, err := tpt.SecureOutbound(ctx, conn, "")
	if err != nil {
		return "", err
	}
	defer sc.Close()
	return sc.RemotePeer(), nil
}

// Resolve is the host at addr, a host and its port: its peer ID, by a
// Probe, and its addresses, by AddrsOf.
func Resolve(ctx context.Context, addr string) (peer.ID, []string, error) {
	addrs, err := AddrsOf(addr)
	if err != nil {
		return "", nil, err
	}
	id, err := Probe(ctx, addr)
	return id, addrs, err
}
