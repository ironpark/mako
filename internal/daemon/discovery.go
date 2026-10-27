package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/discovery"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
)

// discover advertises this daemon and links to the nodes discovery finds,
// and to the configured peers, all over libp2p.
func (d *Daemon) discover(ctx context.Context) {
	if d.p2p == nil {
		<-ctx.Done()
		return
	}
	for _, addr := range d.cfg.Peers {
		go d.keepStatic(ctx, addr)
	}

	var backends []discovery.Backend
	if d.cfg.Discovery.MDNS {
		mdns := &discovery.MDNS{
			NodeID: d.cfg.NodeID,
			Name:   d.cfg.Name,
			Domain: d.cfg.Domain,
			PeerID: d.peerID(),
			Port:   d.p2p.Port(),
		}
		shutdown, err := mdns.Advertise()
		if err != nil {
			d.log.Warn("bonjour advertise failed", "err", err)
		} else {
			defer shutdown()
		}
		backends = append(backends, mdns)
	}
	if d.cfg.Discovery.Tailscale {
		backends = append(backends, &discovery.Tailscale{
			Self:     d.peerID(),
			Linked:   d.p2p.Connected,
			Hosts:    d.cfg.Discovery.TailnetHosts,
			Port:     d.p2p.Port(),
			Interval: time.Minute,
		})
	}
	if len(backends) == 0 {
		<-ctx.Done()
		return
	}

	found := discovery.Run(ctx, backends, func(b discovery.Backend, err error) {
		d.log.Warn("discovery backend failed", "backend", b.Kind(), "err", err)
	})
	for f := range found {
		switch {
		case f.NodeID == d.cfg.NodeID || f.PeerID == d.peerID():
		case f.NodeID == "":
			d.link(f.Node()) // its record comes as the two introduce themselves
		case d.notePeer(f.Node()):
			d.log.Info("peer discovered", "node", f.Name, "addr", f.Addr, "via", f.Source)
		}
	}
}

// keepStatic keeps a link to a configured peer: a multiaddr ending in its
// /p2p/ ID, or a host with its libp2p port, probed for its peer ID until
// it is linked. The node's record comes as the two introduce themselves.
func (d *Daemon) keepStatic(ctx context.Context, addr string) {
	var id string // the probed peer's, once known
	for {
		if id == "" || !d.p2p.Connected(id) {
			got, err := d.linkStatic(ctx, addr)
			if err != nil && ctx.Err() == nil {
				d.log.Debug("static peer unreachable", "peer", addr, "err", err)
			}
			id = got
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(relinkAfter):
		}
	}
}

// linkStatic links to the configured peer at addr, and returns the peer
// ID a probe found it at.
func (d *Daemon) linkStatic(ctx context.Context, addr string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, linkTimeout)
	defer cancel()
	if strings.HasPrefix(addr, "/") {
		return "", d.p2p.EnsureAddr(ctx, addr)
	}
	id, addrs, err := p2p.Resolve(ctx, config.WithP2PPort(addr))
	if err != nil {
		return "", err
	}
	return id.String(), d.p2p.Ensure(ctx, id.String(), addrs)
}

// maintain expires stale peers, keeping those still linked, and links
// again to those whose link dropped.
func (d *Daemon) maintain(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.peers.prune(peerTTL, func(n *protocol.Node) bool { return d.p2p != nil && d.p2p.Connected(n.GetPeerId()) }) {
				d.changes.fire(true, false)
			}
			for _, node := range d.peers.list() {
				d.link(node) // a link dropped is made again
			}
		}
	}
}
