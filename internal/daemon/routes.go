package daemon

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// Requests reach past the nodes that let this daemon in. A node that lets
// this daemon in serves its requests, those addressed to another node
// among them: it passes them on as its own, and they are served where that
// node lets it in. So a daemon reaches every node at the end of a chain of
// nodes each letting in the one before, through the first of them, which
// could ask as much of the rest itself. Each daemon says in the state it
// sends the peers it lets in how it reaches each node it knows, in
// Node.hops and Node.via, and the sessions of those nodes; a peer that
// cannot reach a node as well itself routes its requests through it.

// maxHops is as far as a request goes: the most daemons it goes through
// to the node it is for.
const maxHops = 8

// peerReports keeps what each peer that lets this daemon in last said in
// its state: the sessions it sent, its own and those of the nodes it
// reaches, and how far it reaches each node.
type peerReports struct {
	mu   sync.Mutex
	from map[string]peerReport // by the peer's node ID
}

type peerReport struct {
	sessions []*protocol.SessionInfo
	hops     map[string]uint32 // by node ID
	at       time.Time
}

// put keeps what the peer said, and reports whether its sessions, or how
// far it reaches each node, changed.
func (r *peerReports) put(peer string, sessions []*protocol.SessionInfo, hops map[string]uint32) (sessionsChanged, hopsChanged bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.from == nil {
		r.from = map[string]peerReport{}
	}
	prev, ok := r.from[peer]
	r.from[peer] = peerReport{sessions: sessions, hops: hops, at: time.Now()}
	fresh := ok && time.Since(prev.at) <= stateFresh
	return !fresh || !sameSessions(prev.sessions, sessions), !fresh || !maps.Equal(prev.hops, hops)
}

// drop forgets what the peer said, and reports whether it had said
// anything.
func (r *peerReports) drop(peer string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.from[peer]
	delete(r.from, peer)
	return ok
}

// fresh is what each peer said lately, by its node ID.
func (r *peerReports) fresh() map[string]peerReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]peerReport{}
	for peer, p := range r.from {
		if time.Since(p.at) <= stateFresh {
			out[peer] = p
		}
	}
	return out
}

// reach is how far a peer's state says it reaches each node, but those it
// reaches through self, which would come back here.
func reach(peers []*protocol.Node, self string) map[string]uint32 {
	hops := map[string]uint32{}
	for _, n := range peers {
		if h := n.GetHops(); h > 0 && n.GetVia() != self && n.GetId() != self {
			hops[n.GetId()] = h
		}
	}
	return hops
}

// route is how this daemon reaches a node: how many daemons a request
// goes through to it, and the node ID of the peer it goes to first, ""
// when it goes to the node itself.
type route struct {
	hops uint32
	via  string
}

// routing is how this daemon reaches each node it can, by node ID: the
// peers that let it in directly, and the nodes they reach through the
// fewest hops.
func (d *Daemon) routing() map[string]route {
	out := map[string]route{}
	if d.p2p == nil {
		return out
	}
	for _, p := range d.p2p.Peers() {
		if p.TrustedBy && p.Node != "" {
			out[p.Node] = route{hops: 1}
		}
	}
	reports := d.remote.fresh()
	for _, peer := range slices.Sorted(maps.Keys(reports)) {
		if out[peer] != (route{hops: 1}) {
			continue // a peer that no longer lets this daemon in
		}
		for id, h := range reports[peer].hops {
			if h+1 > maxHops || id == d.cfg.NodeID {
				continue
			}
			if r, ok := out[id]; !ok || h+1 < r.hops {
				out[id] = route{hops: h + 1, via: peer}
			}
		}
	}
	return out
}

// meshSessions are the sessions peers sent of the nodes routes reach
// through them: each peer's own, and those of the nodes it is the way to.
func (d *Daemon) meshSessions(routes map[string]route) []*protocol.SessionInfo {
	reports := d.remote.fresh()
	var out []*protocol.SessionInfo
	for _, peer := range slices.Sorted(maps.Keys(reports)) {
		for _, s := range reports[peer].sessions {
			r := routes[s.GetNodeId()]
			if r.hops > 0 && (r.via == peer || r.via == "" && s.GetNodeId() == peer) {
				out = append(out, s)
			}
		}
	}
	return out
}

type hopsKey struct{}

// withHops marks ctx as a request that went through hops daemons before
// this one.
func withHops(ctx context.Context, hops uint32) context.Context {
	return context.WithValue(ctx, hopsKey{}, hops)
}

// hopsOf is how many daemons the request of ctx went through before this
// one: 0 for one this daemon's own front-end made.
func hopsOf(ctx context.Context) uint32 {
	h, _ := ctx.Value(hopsKey{}).(uint32)
	return h
}
