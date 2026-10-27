// Package discovery finds sanbo daemons on other computers.
//
// Two backends run side by side: Bonjour/mDNS for machines on the same link,
// and Tailscale MagicDNS probing for machines that only share a tailnet.
package discovery

import (
	"context"
	"sync"

	"github.com/ironpark/sanbo/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Found reports one discovered daemon.
type Found struct {
	NodeID string
	Name   string
	Addr   string
	Source string
	// PeerID and Multiaddrs are where it is on libp2p, if it says.
	PeerID     string
	Multiaddrs []string
}

// Node converts a Found into a protocol node record.
func (f Found) Node() *protocol.Node {
	return &protocol.Node{
		Id:         f.NodeID,
		Name:       f.Name,
		Addr:       f.Addr,
		Source:     f.Source,
		LastSeen:   timestamppb.Now(),
		PeerId:     f.PeerID,
		Multiaddrs: f.Multiaddrs,
	}
}

// Backend is one discovery mechanism.
type Backend interface {
	// Kind identifies the backend in logs and node records.
	Kind() string
	// Run emits discoveries on out until ctx is done.
	Run(ctx context.Context, out chan<- Found) error
}

// Run fans several backends into a single channel and closes it when all of
// them have stopped. Backend errors are reported through onError and do not
// stop the others.
func Run(ctx context.Context, backends []Backend, onError func(Backend, error)) <-chan Found {
	out := make(chan Found, 16)
	var wg sync.WaitGroup
	for _, b := range backends {
		wg.Add(1)
		go func(b Backend) {
			defer wg.Done()
			if err := b.Run(ctx, out); err != nil && ctx.Err() == nil && onError != nil {
				onError(b, err)
			}
		}(b)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
