package daemon

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/discovery"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// discover advertises this daemon and folds discovered nodes into the peer
// table, including the statically configured peers.
func (d *Daemon) discover(ctx context.Context) {
	for _, addr := range d.cfg.Peers {
		if strings.HasPrefix(addr, "/") {
			go d.linkStatic(ctx, addr)
			continue
		}
		go d.probeStatic(ctx, addr)
	}

	var backends []discovery.Backend
	if d.cfg.Discovery.MDNS {
		mdns := &discovery.MDNS{
			NodeID: d.cfg.NodeID,
			Name:   d.cfg.Name,
			Port:   d.port,
			Domain: d.cfg.Domain,
		}
		if d.p2p != nil {
			mdns.PeerID, mdns.P2PPort = d.p2p.ID().String(), d.p2p.Port()
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
			NodeID:   d.cfg.NodeID,
			Hello:    d.hello(true),
			Hosts:    d.cfg.Discovery.TailnetHosts,
			Port:     d.port,
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
		if f.NodeID == d.cfg.NodeID {
			continue
		}
		if d.notePeer(f.Node()) {
			d.log.Info("peer discovered", "node", f.Name, "addr", f.Addr, "via", f.Source)
		}
	}
}

// probeStatic keeps a configured peer address in the table, retrying until the
// far side answers.
func (d *Daemon) probeStatic(ctx context.Context, addr string) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, strconv.Itoa(config.DefaultPort))
	}
	for {
		node, err := transportProbe(ctx, addr, d.hello(true))
		if err == nil && node.GetId() != d.cfg.NodeID {
			node.Source = "static"
			if d.notePeer(node) {
				d.log.Info("static peer reachable", "node", node.GetName(), "addr", addr)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// linkStatic keeps a link to a peer configured by its multiaddr, which
// ends in its /p2p/ ID; the node's record comes on the state topic.
func (d *Daemon) linkStatic(ctx context.Context, addr string) {
	if d.p2p == nil {
		d.log.Warn("a peer given by multiaddr needs libp2p, which is off", "peer", addr)
		return
	}
	for {
		if err := d.p2p.EnsureAddr(ctx, addr); err != nil && ctx.Err() == nil {
			d.log.Debug("static peer unreachable", "peer", addr, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// maintain expires stale peers and gossips the local view to the mesh.
func (d *Daemon) maintain(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.peers.prune(peerTTL) {
				d.changes.fire(true, false)
			}
			for _, node := range d.peers.list() {
				d.link(node) // a link dropped is made again
			}
			d.gossip(ctx)
		}
	}
}

// gossip pushes the local node view to every known peer and merges what comes
// back, so a node discovered on one link becomes visible across the mesh.
func (d *Daemon) gossip(ctx context.Context) {
	view := &protocol.Nodes{Nodes: append([]*protocol.Node{d.Self()}, d.peers.list()...)}
	for _, node := range d.peers.list() {
		if node.GetPeerId() != "" {
			continue // a peer on libp2p hears of the mesh on its state topic
		}
		go func(node *protocol.Node) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			stream, err := d.peerStream(ctx, node)
			if err != nil {
				return
			}
			defer stream.Close()

			reply, err := transport.Call[*protocol.Nodes](stream, protocol.TypeGossip, view)
			if err != nil {
				return
			}
			for _, remote := range reply.GetNodes() {
				if remote.GetId() != d.cfg.NodeID {
					d.peers.merge(remote)
				}
			}
		}(node)
	}
}
