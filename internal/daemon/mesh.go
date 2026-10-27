package daemon

import (
	"context"
	"net"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The mesh over libp2p. Daemons link to each other on libp2p, where each
// shows the other it knows the mesh's key before anything else (see
// internal/p2p), and keep one link to each peer they know of. Requests to
// a peer travel on its link as they do on QUIC. Each daemon publishes its
// state on the state topic, its record, its own sessions, and whether a
// window of its is answering approvals, when it changes and every few
// seconds besides; the others keep the latest of each, so listing the
// mesh's sessions asks no one, and a front-end watching its daemon hears
// as soon as anything changes.
//
// A peer without libp2p, an older build, is reached over QUIC, and its
// sessions are asked for as before.

// stateTopic is the topic daemons publish their state on.
const stateTopic = "mako/state/1"

// stateEvery is how often a daemon publishes its state though nothing
// changed, and stateFresh how long one published is taken for current.
const (
	stateEvery = 5 * time.Second
	stateFresh = 4 * stateEvery
)

// startP2P starts the libp2p host and serves the mako protocol on it.
func (d *Daemon) startP2P(ctx context.Context, dir string) error {
	host, listenPort, _ := net.SplitHostPort(d.cfg.Listen)
	port := d.cfg.P2P.Port
	if port == 0 && listenPort != "0" {
		port = config.DefaultP2PPort // a daemon on any free port, as in tests, takes any here too
	}
	h, err := p2p.New(ctx, p2p.Config{
		Dir:          dir,
		Host:         host,
		Port:         port,
		PSK:          d.cfg.PSK,
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
	h.HandleStreams(func(s network.Stream) {
		defer s.Close()
		from := &protocol.Node{PeerId: s.Conn().RemotePeer().String(), Multiaddrs: []string{s.Conn().RemoteMultiaddr().String()}}
		if err := d.serveStream(ctx, transport.NewStream(s), from); err != nil && !isClosed(err) {
			d.log.Debug("peer stream ended", "err", err)
		}
	})
	d.log.Info("libp2p listening", "peer", h.ID().String(), "addrs", h.Addrs())
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

// relinkAfter is how long a failed link to a peer waits to be tried again.
const relinkAfter = 30 * time.Second

// link connects to node over libp2p, if it has an identity there and is
// not linked already, so it shares the state topic with this daemon. One
// attempt at a time goes to a peer, and one that failed waits a while.
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
		ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
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

// watchState publishes this daemon's state on the mesh and takes in the
// others', and tells watching front-ends when its own sessions change.
// Without libp2p it does the latter alone, and nothing while no one
// watches.
func (d *Daemon) watchState(ctx context.Context) {
	var topic *pubsub.Topic
	if d.p2p != nil {
		t, sub, err := d.p2p.Topic(stateTopic, d.decodeState)
		if err != nil {
			d.log.Warn("state topic failed", "err", err)
		} else {
			topic = t
			defer topic.Close()
			defer sub.Cancel()
			go d.takeStates(ctx, sub)
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last *protocol.NodeState
	var sent time.Time
	// A peer new on the topic is told at the next tick: told the moment
	// it joins, before its side of the topic is ready, the state is lost.
	heard := 0
	for {
		if topic != nil || d.changes.watched() {
			// The node is compared without its time, which it is
			// published with.
			node := d.Self()
			node.LastSeen = nil
			st := &protocol.NodeState{Node: node, Sessions: d.listSessions(), WatchingApprovals: d.approvals.watchedHere()}
			sessionsChanged := last != nil && !sameSessions(st.GetSessions(), last.GetSessions())
			if sessionsChanged {
				d.changes.fire(false, true)
			}
			changed := last == nil || sessionsChanged || st.GetWatchingApprovals() != last.GetWatchingApprovals() || !proto.Equal(node, last.GetNode())
			if topic != nil {
				n := len(topic.ListPeers())
				if changed || n > heard || time.Since(sent) >= stateEvery {
					d.publishState(ctx, topic, st)
					sent = time.Now()
				}
				heard = n
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

// publishState publishes st on topic, stamped now.
func (d *Daemon) publishState(ctx context.Context, topic *pubsub.Topic, st *protocol.NodeState) {
	out := &protocol.NodeState{Node: proto.CloneOf(st.GetNode()), Sessions: st.GetSessions(), WatchingApprovals: st.GetWatchingApprovals()}
	out.Node.LastSeen = timestamppb.Now()
	data, err := proto.Marshal(out)
	if err == nil {
		err = topic.Publish(ctx, data)
	}
	if err != nil && ctx.Err() == nil {
		d.log.Debug("state publish failed", "err", err)
	}
}

// sameSessions reports whether a and b differ in nothing but last output,
// which the next regular publish carries.
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

// decodeState admits a state message only as its own node's: signed by
// the peer the record names.
func (d *Daemon) decodeState(from peer.ID, data []byte) (any, bool) {
	st := &protocol.NodeState{}
	if proto.Unmarshal(data, st) != nil {
		return nil, false
	}
	return st, st.GetNode().GetId() != "" && st.GetNode().GetPeerId() == from.String()
}

// takeStates takes in the states peers publish: each node's record, as
// it says of itself, and its sessions.
func (d *Daemon) takeStates(ctx context.Context, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			return
		}
		st, ok := msg.ValidatorData.(*protocol.NodeState)
		if !ok || msg.ReceivedFrom == d.p2p.ID() {
			continue
		}
		node := st.GetNode()
		if node.GetId() == d.cfg.NodeID {
			continue
		}
		node.Source = "mesh"
		d.notePeer(node)
		var own []*protocol.SessionInfo
		for _, info := range st.GetSessions() {
			if info.GetNodeId() == node.GetId() {
				own = append(own, info)
			}
		}
		if d.remote.put(node.GetId(), own) {
			d.changes.fire(false, true)
		}
		if st.GetWatchingApprovals() {
			d.approvals.watch(false)
		}
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

// fire tells every watcher that the nodes or the sessions changed, merged
// into what it has not taken yet.
func (n *notifier) fire(nodes, sessions bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.watchers {
		c := &protocol.Changed{Nodes: nodes, Sessions: sessions}
		select {
		case pending := <-ch:
			c.Nodes, c.Sessions = c.Nodes || pending.GetNodes(), c.Sessions || pending.GetSessions()
		default:
		}
		ch <- c
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
	if err := stream.Send(protocol.TypeChanged, env.GetId(), &protocol.Changed{Nodes: true, Sessions: true}); err != nil {
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
