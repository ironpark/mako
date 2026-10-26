package daemon

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/discovery"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// discover advertises this daemon and folds discovered nodes into the peer
// table, including the statically configured peers.
func (d *Daemon) discover(ctx context.Context) {
	for _, addr := range d.cfg.Peers {
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
		if d.peers.upsert(f.Node()) {
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
			if d.peers.upsert(node) {
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

// maintain expires stale peers and gossips the local view to the mesh.
func (d *Daemon) maintain(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.peers.prune(peerTTL)
			d.gossip(ctx)
		}
	}
}

// gossip pushes the local node view to every known peer and merges what comes
// back, so a node discovered on one link becomes visible across the mesh.
func (d *Daemon) gossip(ctx context.Context) {
	view := &protocol.Nodes{Nodes: append([]*protocol.Node{d.Self()}, d.peers.list()...)}
	for _, node := range d.peers.list() {
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
