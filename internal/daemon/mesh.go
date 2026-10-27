package daemon

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The mesh over libp2p. Daemons link to every peer they know of, and on
// each link introduce themselves and say whether they let the other in
// (see internal/p2p). Trust runs one way: a daemon serves the requests of
// the peers it lets in, and asks things only of the peers that let it in.
// Each daemon sends its state to its peers when it changes and every few
// seconds besides: to those it lets in, its record, its own sessions and
// the nodes it knows of; to those that let it in, whether a window of its
// is answering approvals. Listing the mesh's sessions asks no one, and a
// front-end watching its daemon hears as soon as anything changes.

// stateEvery is how often a daemon sends its state though nothing
// changed, and stateFresh how long one sent is taken for current.
const (
	stateEvery = 5 * time.Second
	stateFresh = 4 * stateEvery
)

// startP2P starts the libp2p host and serves the mako protocol on it.
func (d *Daemon) startP2P(ctx context.Context, dir string) error {
	host, port, err := d.cfg.P2P.HostPort()
	if err != nil {
		return err
	}
	var owner ssh.Signer
	if d.cfg.SSHKey != "" {
		if owner, err = p2p.LoadSigner(mcp.ExpandHome(d.cfg.SSHKey)); err != nil {
			d.log.Warn("the SSH key cannot vouch for this node; it is let in by its own key alone", "key", d.cfg.SSHKey, "err", err)
		}
	}
	h, err := p2p.New(ctx, p2p.Config{
		Dir:          dir,
		Host:         host,
		Port:         port,
		Node:         d.cfg.NodeID,
		Name:         d.cfg.Name,
		Owner:        owner,
		OnPeer:       d.onPeer,
		Relays:       d.cfg.P2P.Relays,
		RelayService: d.cfg.P2P.Relay,
		UserAgent:    "mako/" + config.Version,
		NoNAT:        host != "" && net.ParseIP(host).IsLoopback(),
		Log:          d.log,
	})
	if err != nil {
		return err
	}
	d.p2p = h
	d.keyPrint, d.ownerPrint = fingerprint(h.Key()), fingerprint(h.Owner())
	h.HandleStreams(func(s network.Stream) {
		defer s.Close()
		if err := d.serveStream(ctx, transport.NewStream(s), true); err != nil && !isClosed(err) {
			d.log.Debug("peer stream ended", "err", err)
		}
	})
	h.HandleState(d.takeState)
	h.HandleAccess(d.takeAccessRequest)
	d.log.Info("libp2p listening", "peer", h.ID().String(), "addrs", h.Addrs(), "key", d.keyPrint)
	return nil
}

func (d *Daemon) peerID() string {
	if d.p2p == nil {
		return ""
	}
	return d.p2p.ID().String()
}

func (d *Daemon) multiaddrs() []string {
	if d.p2p == nil {
		return nil
	}
	return d.p2p.Addrs()
}

// addr is where this daemon is on libp2p, as host and TCP port, for
// people to read; "" without libp2p.
func (d *Daemon) addr() string {
	if d.p2p == nil {
		return ""
	}
	return d.p2p.HostPort()
}

// linkedPeer is how this daemon stands with node on libp2p, if they are
// linked.
func (d *Daemon) linkedPeer(node *protocol.Node) (p2p.Peer, bool) {
	if d.p2p == nil || node.GetPeerId() == "" {
		return p2p.Peer{}, false
	}
	return d.p2p.Peer(node.GetPeerId())
}

// nodes are this daemon and its peers, each with how this daemon stands
// with it, which only this daemon can say: a node it has no link to reads
// as neither.
func (d *Daemon) nodes() []*protocol.Node {
	out := []*protocol.Node{d.Self()}
	for _, node := range d.peers.list() {
		p, _ := d.linkedPeer(node)
		node.Trusted, node.TrustsUs = p.Trusts, p.TrustedBy
		node.Key, node.Owner = fingerprint(p.Key), fingerprint(p.Owner)
		out = append(out, node)
	}
	return out
}

// fingerprint is key's SHA256 fingerprint, "" for none.
func fingerprint(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	return ssh.FingerprintSHA256(key)
}

// notePeer records a node this daemon saw itself, links to it, and tells
// the watching front-ends of one new. It reports whether the node was new
// or moved.
func (d *Daemon) notePeer(node *protocol.Node) bool {
	if node.GetId() == "" || node.GetId() == d.cfg.NodeID {
		return false
	}
	moved := d.peers.upsert(node)
	if moved {
		d.changes.fire(true, false)
	}
	d.link(node)
	return moved
}

// onPeer takes in how this daemon stands with a peer, each time the two
// introduce themselves: it records the node, sends it this daemon's state
// at once, and forgets the sessions of a peer that no longer lets it in.
func (d *Daemon) onPeer(p p2p.Peer) {
	if p.Node == "" || p.Node == d.cfg.NodeID {
		return
	}
	node, ok := d.peers.get(p.Node)
	if !ok {
		node = &protocol.Node{Id: p.Node, Source: "mesh"}
	}
	node.Name, node.PeerId = p.Name, p.ID.String()
	if len(node.GetMultiaddrs()) == 0 && p.Addr != nil {
		node.Multiaddrs = []string{p.Addr.String()}
	}
	d.peers.upsert(node)
	if p.Trusts {
		d.access.drop(p.Node)
	}
	dropped := !p.TrustedBy && d.remote.drop(p.Node)
	d.changes.fire(true, dropped)
	full, public := d.encodeState(d.state())
	go d.sendState(d.ctx, p, full, public)
}

// relinkAfter is how long a failed link to a peer waits to be tried again,
// and linkTimeout how long one is tried.
const (
	relinkAfter = 30 * time.Second
	linkTimeout = 15 * time.Second
)

// link connects to node over libp2p, if it has an identity there and is
// not linked already. One attempt at a time goes to a peer, and one that
// failed waits a while.
func (d *Daemon) link(node *protocol.Node) {
	id := node.GetPeerId()
	if d.p2p == nil || id == "" || d.p2p.Connected(id) {
		return
	}
	d.mu.Lock()
	if time.Since(d.linking[id]) < relinkAfter {
		d.mu.Unlock()
		return
	}
	d.linking[id] = time.Now()
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(d.ctx, linkTimeout)
		defer cancel()
		err := d.p2p.Ensure(ctx, id, node.GetMultiaddrs())
		if err != nil {
			d.log.Debug("libp2p link failed", "node", node.GetName(), "err", err)
			return
		}
		d.mu.Lock()
		delete(d.linking, id)
		d.mu.Unlock()
	}()
}

// state is this daemon's state as it stands, the node without its time.
func (d *Daemon) state() *protocol.NodeState {
	node := d.Self()
	node.LastSeen = nil
	return &protocol.NodeState{Node: node, Sessions: d.listSessions(), WatchingApprovals: d.approvals.watchedHere()}
}

// watchState sends this daemon's state to its peers, and tells watching
// front-ends when its own sessions change. Without libp2p it does the
// latter alone, and nothing while no one watches.
func (d *Daemon) watchState(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last *protocol.NodeState
	var sent time.Time
	for {
		if d.p2p != nil || d.changes.watched() {
			st := d.state()
			sessionsChanged := last != nil && !sameSessions(st.GetSessions(), last.GetSessions())
			if sessionsChanged {
				d.changes.fire(false, true)
			}
			changed := last == nil || sessionsChanged || st.GetWatchingApprovals() != last.GetWatchingApprovals() || !proto.Equal(st.GetNode(), last.GetNode())
			if d.p2p != nil && (changed || time.Since(sent) >= stateEvery) {
				full, public := d.encodeState(st)
				for _, p := range d.p2p.Peers() {
					go d.sendState(ctx, p, full, public)
				}
				sent = time.Now()
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// encodeState is st stamped now, as sent to a peer this daemon lets in,
// with its sessions and the nodes it knows of, and as sent to one that
// only lets it in, without.
func (d *Daemon) encodeState(st *protocol.NodeState) (full, public []byte) {
	node := proto.CloneOf(st.GetNode())
	node.LastSeen = timestamppb.Now()
	out := &protocol.NodeState{Node: node, Sessions: st.GetSessions(), WatchingApprovals: st.GetWatchingApprovals()}
	for _, n := range d.peers.list() {
		if n.GetPeerId() != "" {
			out.Peers = append(out.Peers, n)
		}
	}
	full, _ = proto.Marshal(out)
	node = d.publicSelf()
	node.LastSeen = out.Node.LastSeen
	public, _ = proto.Marshal(&protocol.NodeState{Node: node, WatchingApprovals: st.GetWatchingApprovals()})
	return full, public
}

// sendState sends p as much of this daemon's state as it may have: full
// to a peer this daemon lets in, public to one that only lets it in.
func (d *Daemon) sendState(ctx context.Context, p p2p.Peer, full, public []byte) {
	data := public
	switch {
	case p.Trusts:
		data = full
	case !p.TrustedBy:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.p2p.SendState(ctx, p.ID, data); err != nil && ctx.Err() == nil {
		d.log.Debug("state send failed", "node", p.Name, "err", err)
	}
}

// sameSessions reports whether a and b differ in nothing but last output,
// which the next regular state carries.
func sameSessions(a, b []*protocol.SessionInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if proto.Equal(a[i], b[i]) {
			continue
		}
		if proto.Equal(a[i].GetLastOutput(), b[i].GetLastOutput()) {
			return false // something else differs
		}
		x, y := proto.CloneOf(a[i]), proto.CloneOf(b[i])
		x.LastOutput, y.LastOutput = nil, nil
		if !proto.Equal(x, y) {
			return false
		}
	}
	return true
}

// takeState takes in a state a peer sent: its record, as it says of
// itself; from a peer that lets this daemon in, its sessions and the
// nodes it knows of; and from one this daemon lets in, whether a window
// of its answers approvals.
func (d *Daemon) takeState(from p2p.Peer, data []byte) {
	st := &protocol.NodeState{}
	if proto.Unmarshal(data, st) != nil {
		return
	}
	node := st.GetNode()
	if node.GetId() == "" || node.GetId() == d.cfg.NodeID || node.GetId() != from.Node || node.GetPeerId() != from.ID.String() {
		return
	}
	node.Source = "mesh"
	d.notePeer(node)
	if from.TrustedBy {
		var own []*protocol.SessionInfo
		for _, info := range st.GetSessions() {
			if info.GetNodeId() == node.GetId() {
				own = append(own, info)
			}
		}
		if d.remote.put(node.GetId(), own) {
			d.changes.fire(false, true)
		}
		for _, other := range st.GetPeers() {
			if other.GetId() == d.cfg.NodeID || other.GetPeerId() == "" || other.GetPeerId() == d.peerID() {
				continue
			}
			d.peers.merge(other)
			d.link(other)
		}
	}
	if from.Trusts && st.GetWatchingApprovals() {
		d.approvals.watch(false)
	}
}

// remoteSessions are the sessions each peer last published, by node ID.
type remoteSessions struct {
	mu    sync.Mutex
	nodes map[string]publishedSessions
}

type publishedSessions struct {
	sessions []*protocol.SessionInfo
	at       time.Time
}

// put keeps sessions as node's, and reports whether they changed.
func (r *remoteSessions) put(node string, sessions []*protocol.SessionInfo) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = map[string]publishedSessions{}
	}
	prev, ok := r.nodes[node]
	r.nodes[node] = publishedSessions{sessions: sessions, at: time.Now()}
	return !ok || !sameSessions(prev.sessions, sessions)
}

// drop forgets node's sessions, and reports whether there were any.
func (r *remoteSessions) drop(node string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.nodes[node]
	delete(r.nodes, node)
	return ok
}

// get is the sessions node last published, if it did so lately.
func (r *remoteSessions) get(node string) ([]*protocol.SessionInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.nodes[node]
	if !ok || time.Since(p.at) > stateFresh {
		return nil, false
	}
	return p.sessions, true
}

// notifier tells the front-ends watching their daemon what changed.
type notifier struct {
	mu       sync.Mutex
	watchers map[chan *protocol.Changed]struct{}
}

// fire tells every watcher that the nodes or the sessions changed.
func (n *notifier) fire(nodes, sessions bool) {
	n.send(&protocol.Changed{Nodes: nodes, Sessions: sessions})
}

// fireAccess tells every watcher that a request came, or the keys let in
// changed.
func (n *notifier) fireAccess() { n.send(&protocol.Changed{Access: true}) }

// send gives every watcher c, merged into what it has not taken yet.
func (n *notifier) send(c *protocol.Changed) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.watchers {
		next := proto.CloneOf(c)
		select {
		case pending := <-ch:
			next.Nodes = next.Nodes || pending.GetNodes()
			next.Sessions = next.Sessions || pending.GetSessions()
			next.Access = next.Access || pending.GetAccess()
		default:
		}
		ch <- next
	}
}

// watched reports whether any front-end is watching.
func (n *notifier) watched() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.watchers) > 0
}

// watch is a feed of changes until stop.
func (n *notifier) watch() (feed <-chan *protocol.Changed, stop func()) {
	ch := make(chan *protocol.Changed, 1)
	n.mu.Lock()
	if n.watchers == nil {
		n.watchers = map[chan *protocol.Changed]struct{}{}
	}
	n.watchers[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.watchers, ch)
		n.mu.Unlock()
	}
}

// handleWatch sends a front-end a Changed whenever the mesh's nodes or
// sessions change, until it hangs up.
func (d *Daemon) handleWatch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	feed, stop := d.changes.watch()
	defer stop()
	// The first says the daemon watches, and that everything is new to
	// the front-end.
	if err := stream.Send(protocol.TypeChanged, env.GetId(), &protocol.Changed{Nodes: true, Sessions: true, Access: true}); err != nil {
		return err
	}
	// The front-end reads nothing more from the stream; its closing it
	// ends the watch.
	gone := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		gone <- err
	}()
	for {
		select {
		case c := <-feed:
			if err := stream.Send(protocol.TypeChanged, env.GetId(), c); err != nil {
				return err
			}
		case err := <-gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
