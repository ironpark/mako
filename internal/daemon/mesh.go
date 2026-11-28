package daemon

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net"
	"slices"
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
	cfg := d.config()
	host, port, err := cfg.P2P.HostPort()
	if err != nil {
		return err
	}
	var owner ssh.Signer
	if cfg.SSHKey != "" {
		if owner, err = p2p.LoadSigner(mcp.ExpandHome(cfg.SSHKey)); err != nil {
			d.log.Warn("the SSH key cannot vouch for this node; it is let in by its own key alone", "key", cfg.SSHKey, "err", err)
		}
	}
	h, err := p2p.New(ctx, p2p.Config{
		Dir:               dir,
		Host:              host,
		Port:              port,
		Node:              cfg.NodeID,
		Name:              cfg.Name,
		Owner:             owner,
		SSHAuthorizedKeys: mcp.ExpandHome("~/.ssh/authorized_keys"), // a key that logs in here by SSH vouches
		OnPeer:            d.onPeer,
		OnDrop:            d.onDrop,
		Relays:            cfg.P2P.Relays,
		RelayService:      cfg.P2P.Relay,
		UserAgent:         "mako/" + config.Version,
		NoNAT:             host != "" && net.ParseIP(host).IsLoopback(),
		Log:               d.log,
	})
	if err != nil {
		if c, ok := owner.(io.Closer); ok {
			_ = c.Close()
		}
		return err
	}
	d.p2p = h
	d.keyPrint, d.ownerPrint = fingerprint(h.Key()), fingerprint(h.Owner())
	h.HandleStreams(func(s network.Stream) {
		defer s.Close()
		if err := d.serveStream(ctx, transport.NewPeerStream(s), true); err != nil && !isClosed(err) {
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
	routes := d.routing()
	for _, node := range d.peers.list() {
		p, _ := d.linkedPeer(node)
		node.Trusted, node.TrustsUs = p.Trusts, p.TrustedBy
		node.Key, node.Owner = fingerprint(p.Key), fingerprint(p.Owner)
		r := routes[node.GetId()]
		node.Hops, node.Via = r.hops, r.via
		d.linkState(node)
		out = append(out, node)
	}
	if d.ssh != nil {
		out = append(out, d.ssh.nodes()...)
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
	if node.GetId() == "" || node.GetId() == d.config().NodeID {
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
	if p.Node == "" || p.Node == d.config().NodeID {
		return
	}
	node, ok := d.peers.get(p.Node)
	if !ok {
		node = &protocol.Node{Id: p.Node, Source: "mesh"}
	}
	node.Name, node.PeerId = p.Name, p.ID.String()
	// Every peer says what it runs on, let in or not.
	if p.OS != "" {
		node.Os, node.Arch = p.OS, p.Arch
	}
	if len(node.GetMultiaddrs()) == 0 && p.Addr != nil {
		node.Multiaddrs = []string{p.Addr.String()}
	}
	d.peers.upsert(node)
	if p.Trusts {
		d.access.drop(p.Node)
	}
	dropped := !p.TrustedBy && d.remote.drop(p.Node)
	d.changes.fire(true, dropped)
	routes := d.routing()
	// A peer just linked is sent the workspace whole: it is likely to hold
	// another.
	shared, full, public := d.encodeState(d.state(), routes, d.meshSessions(routes), true)
	d.enqueueState(d.ctx, p, shared, full, public)
}

// A failed link to a peer is tried again after relinkFirst, then after
// twice as long each time it fails again, up to relinkAfter; linkTimeout
// is how long one is tried.
const (
	relinkFirst = 2 * time.Second
	relinkAfter = 30 * time.Second
	linkTimeout = 15 * time.Second
)

// linkTry is how the link to a peer not linked is being made.
type linkTry struct {
	busy  bool      // a try is under way
	fails int       // how many tries failed in a row
	next  time.Time // when the next may begin
	err   string    // why the last failed
}

// relinkWait is how long a link that failed fails times in a row waits.
func relinkWait(fails int) time.Duration {
	return min(relinkFirst<<min(max(fails-1, 0), 5), relinkAfter)
}

// link connects to node over libp2p, if it has an identity there and is
// not linked already. One attempt at a time goes to a peer, and one that
// failed waits, longer each time.
func (d *Daemon) link(node *protocol.Node) {
	id := node.GetPeerId()
	if d.p2p == nil || id == "" {
		return
	}
	if d.p2p.Connected(id) {
		d.mu.Lock()
		if try := d.linking[id]; try != nil && !try.busy {
			delete(d.linking, id) // linked from the other side
		}
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	try := d.linking[id]
	if try == nil {
		try = &linkTry{}
		d.linking[id] = try
	}
	if try.busy || time.Now().Before(try.next) {
		d.mu.Unlock()
		return
	}
	try.busy = true
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(d.ctx, linkTimeout)
		defer cancel()
		err := d.p2p.Ensure(ctx, id, node.GetMultiaddrs())
		d.mu.Lock()
		try.busy = false
		if err == nil {
			if d.linking[id] == try {
				delete(d.linking, id)
			}
		} else {
			try.fails++
			try.err = err.Error()
			try.next = time.Now().Add(relinkWait(try.fails))
			d.log.Debug("libp2p link failed", "node", node.GetName(), "err", err, "fails", try.fails)
		}
		d.mu.Unlock()
		d.changes.fire(true, false)
	}()
}

// onDrop links again at once to a peer whose link dropped, as a laptop
// waking or a network changing drops them.
func (d *Daemon) onDrop(id string) {
	d.stateSenders.drop(id)
	if d.ctx.Err() != nil {
		return
	}
	d.relink(id)
}

// relink forgets the wait of the link to the peer id and tries it now.
func (d *Daemon) relink(id string) {
	d.mu.Lock()
	if try := d.linking[id]; try != nil && !try.busy {
		delete(d.linking, id)
	}
	d.mu.Unlock()
	for _, node := range d.peers.list() {
		if node.GetPeerId() == id {
			d.link(node)
			return
		}
	}
	d.changes.fire(true, false)
}

// linkState sets on node how this daemon's link to it stands.
func (d *Daemon) linkState(node *protocol.Node) {
	id := node.GetPeerId()
	if d.p2p == nil || id == "" {
		return
	}
	if d.p2p.Connected(id) {
		node.Link = protocol.LinkUp
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	node.Link = protocol.LinkDown
	if try := d.linking[id]; try != nil {
		if try.busy {
			node.Link = protocol.LinkLinking
		}
		node.LinkError = try.err
		if !try.next.IsZero() {
			node.RelinkAt = protocol.Time(try.next)
		}
	}
}

// state is this daemon's state as it stands, the node without its time.
func (d *Daemon) state() *protocol.NodeState {
	node := d.Self()
	node.LastSeen = nil
	sessions := d.listSessions()
	d.shareScopes(sessions)
	st := &protocol.NodeState{Node: node, Sessions: sessions, WatchingApprovals: d.approvals.watchedHere()}
	if d.syncsWorkspace() {
		st.Workspace = d.workspace.shared()
		st.PluginSyncDigest = d.pluginSyncDigest()
	}
	if d.syncsPlugins() {
		st.PluginConfigSyncDigest = d.pluginConfigSyncDigest()
	}
	if d.syncsAgents() {
		st.AgentSyncDigest = d.agentSyncDigest()
	}
	return st
}

// watchState sends this daemon's state to its peers, and tells watching
// front-ends when its own sessions change. Without libp2p it does the
// latter alone, and nothing while no one watches.
func (d *Daemon) watchState(ctx context.Context) {
	defer d.stateSenders.closeAndWait()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	settle := time.NewTimer(0)
	<-settle.C
	var last *protocol.NodeState
	var lastRoutes map[string]route
	var lastRelayed []*protocol.SessionInfo
	var sent time.Time
	// sentDigest is the digest of the workspace last sent whole. A peer
	// is sent the workspace whole when it changes, and its digest alone
	// otherwise; one that holds another asks for it.
	var sentDigest []byte
	for {
		_ = d.followed()
		if d.syncsAgents() {
			d.captureLocalAgentConfig()
		}
		if d.syncsPlugins() {
			d.captureLocalPluginConfig()
		}
		if d.p2p != nil || d.changes.watched() {
			st := d.state()
			// A route that changed goes on at once, as do the sessions of the
			// nodes reached, so they travel a chain a hop a tick.
			routes := d.routing()
			relayed := d.meshSessions(routes)
			routesChanged := !maps.Equal(routes, lastRoutes) || !sameSessions(relayed, lastRelayed)
			lastRoutes, lastRelayed = routes, relayed
			nodesChanged := last != nil && !proto.Equal(st.GetNode(), last.GetNode())
			sessionsChanged := last != nil && !sameSessions(st.GetSessions(), last.GetSessions())
			if nodesChanged || sessionsChanged {
				d.changes.fire(nodesChanged, sessionsChanged)
			}
			changed := last == nil || nodesChanged || sessionsChanged || routesChanged || st.GetWatchingApprovals() != last.GetWatchingApprovals() ||
				!bytes.Equal(st.GetWorkspace().GetDigest(), last.GetWorkspace().GetDigest()) ||
				!bytes.Equal(st.GetPluginSyncDigest(), last.GetPluginSyncDigest()) ||
				!bytes.Equal(st.GetPluginConfigSyncDigest(), last.GetPluginConfigSyncDigest()) ||
				!bytes.Equal(st.GetAgentSyncDigest(), last.GetAgentSyncDigest())
			if d.p2p != nil && (changed || time.Since(sent) >= stateEvery) {
				digest := st.GetWorkspace().GetDigest()
				whole := !bytes.Equal(digest, sentDigest)
				shared, full, public := d.encodeState(st, routes, relayed, whole)
				if whole {
					sentDigest = digest
				}
				for _, p := range d.p2p.Peers() {
					d.enqueueState(ctx, p, shared, full, public)
				}
				sent = time.Now()
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			continue
		case <-d.statusPoke:
		}
		// A status that changed goes out without waiting for the tick,
		// once those changing with it have: a turn's end and its approval
		// cleared go together.
		settle.Reset(statusSettle)
		select {
		case <-ctx.Done():
			return
		case <-settle.C:
		}
	}
}

// statusSettle is how long watchState lets a session's status settle once
// it changed before it sends it, which bounds how often it does.
const statusSettle = 100 * time.Millisecond

// pokeStatus tells watchState a session's status changed. It never blocks:
// sessions call it locked.
func (d *Daemon) pokeStatus() {
	select {
	case d.statusPoke <- struct{}{}:
	default:
	}
}

// encodeState is st stamped now, as sent to a peer this daemon lets in,
// with its sessions and the nodes it knows of, and the workspace for one
// that lets it in too, whole or, unless wholeWorkspace, its digest alone;
// and as sent to one that only lets it in, without.
// A peer this daemon lets in may route through it, so it has routes, how
// this daemon reaches each node, and relayed, the sessions of those nodes.
func (d *Daemon) encodeState(st *protocol.NodeState, routes map[string]route, relayed []*protocol.SessionInfo, wholeWorkspace bool) (shared, full, public []byte) {
	node := proto.CloneOf(st.GetNode())
	node.LastSeen = timestamppb.Now()
	sessions := append(slices.Clip(st.GetSessions()), relayed...)
	out := &protocol.NodeState{Node: node, Sessions: sessions, WatchingApprovals: st.GetWatchingApprovals()}
	for _, n := range d.peers.list() {
		if n.GetPeerId() != "" {
			r := routes[n.GetId()]
			n.Hops, n.Via = r.hops, r.via
			out.Peers = append(out.Peers, n)
		}
	}
	full = marshalState(out)
	out.Workspace = st.GetWorkspace()
	if ws := out.GetWorkspace(); ws != nil && !wholeWorkspace {
		out.Workspace = &protocol.Workspace{Digest: ws.GetDigest()}
	}
	out.PluginSyncDigest = st.GetPluginSyncDigest()
	out.PluginConfigSyncDigest = st.GetPluginConfigSyncDigest()
	out.AgentSyncDigest = st.GetAgentSyncDigest()
	shared = marshalState(out)
	node = d.publicSelf()
	node.LastSeen = out.Node.LastSeen
	public = marshalState(&protocol.NodeState{Node: node, WatchingApprovals: st.GetWatchingApprovals()})
	return shared, full, public
}

// marshalState is st as sent, compressed: a state goes to every peer every
// few seconds.
func marshalState(st *protocol.NodeState) []byte {
	b, _ := proto.Marshal(st)
	return protocol.Compress(b)
}

type peerStateItem struct {
	peer p2p.Peer
	data []byte
}

type peerStateWorker struct {
	mu      sync.Mutex
	pending *peerStateItem
	notify  chan struct{}
	cancel  context.CancelFunc
}

type stateSenders struct {
	mu      sync.Mutex
	closed  bool
	workers map[string]*peerStateWorker
	wg      sync.WaitGroup
	sendFn  func(context.Context, p2p.Peer, []byte)
}

func statePayload(p p2p.Peer, shared, full, public []byte) []byte {
	switch {
	case p.Trusts && p.TrustedBy:
		return shared
	case p.Trusts:
		return full
	case p.TrustedBy:
		return public
	default:
		return nil
	}
}

func (s *stateSenders) enqueue(ctx context.Context, p p2p.Peer, data []byte, defaultSend func(context.Context, p2p.Peer, []byte)) {
	id := p.ID.String()
	if data == nil {
		s.drop(id)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	if s.workers == nil {
		s.workers = map[string]*peerStateWorker{}
	}
	sendFn := s.sendFn
	if sendFn == nil {
		sendFn = defaultSend
	}
	w := s.workers[id]
	if w == nil {
		wctx, cancel := context.WithCancel(ctx)
		w = &peerStateWorker{
			notify: make(chan struct{}, 1),
			cancel: cancel,
		}
		s.workers[id] = w
		s.wg.Add(1)
		go s.runWorker(wctx, w, sendFn)
	}
	w.mu.Lock()
	w.pending = &peerStateItem{peer: p, data: data}
	w.mu.Unlock()
	s.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (s *stateSenders) runWorker(ctx context.Context, w *peerStateWorker, sendFn func(context.Context, p2p.Peer, []byte)) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			w.mu.Lock()
			item := w.pending
			w.pending = nil
			w.mu.Unlock()
			if item == nil {
				break
			}
			sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			sendFn(sendCtx, item.peer, item.data)
			cancel()
		}
	}
}

func (s *stateSenders) drop(id string) {
	s.mu.Lock()
	w := s.workers[id]
	if w != nil {
		delete(s.workers, id)
	}
	s.mu.Unlock()
	if w != nil {
		w.cancel()
	}
}

func (s *stateSenders) closeAndWait() {
	s.mu.Lock()
	s.closed = true
	workers := make([]*peerStateWorker, 0, len(s.workers))
	for _, w := range s.workers {
		workers = append(workers, w)
	}
	clear(s.workers)
	s.mu.Unlock()
	for _, w := range workers {
		w.cancel()
	}
	s.wg.Wait()
}

func (d *Daemon) enqueueState(ctx context.Context, p p2p.Peer, shared, full, public []byte) {
	d.stateSenders.enqueue(ctx, p, statePayload(p, shared, full, public), d.sendStatePayload)
}

// sendStatePayload sends p the preselected state payload.
func (d *Daemon) sendStatePayload(ctx context.Context, p p2p.Peer, data []byte) {
	if err := d.p2p.SendState(ctx, p.ID, data); err != nil && ctx.Err() == nil {
		d.log.Debug("state send failed", "node", p.Name, "err", err)
	}
}

// sameSessions reports whether a and b differ in nothing but last output
// and the spinner of an agent at work, which turns in its title every
// second: the next regular state carries them. Taken as a change, one
// working agent had every watching window read the sessions anew, and
// every peer sent this daemon's state, each second.
func sameSessions(a, b []*protocol.SessionInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if proto.Equal(a[i], b[i]) {
			continue
		}
		x, y := proto.CloneOf(a[i]), proto.CloneOf(b[i])
		x.LastOutput, y.LastOutput = nil, nil
		x.Title, y.Title = protocol.BareTitle(x.GetTitle()), protocol.BareTitle(y.GetTitle())
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
	data, err := protocol.Decompress(data)
	if err != nil {
		return
	}
	st := &protocol.NodeState{}
	if proto.Unmarshal(data, st) != nil {
		return
	}
	node := st.GetNode()
	nodeID := d.config().NodeID
	if node.GetId() == "" || node.GetId() == nodeID || node.GetId() != from.Node || node.GetPeerId() != from.ID.String() {
		return
	}
	node.Source = "mesh"
	var digest []byte
	if prev, ok := d.peers.get(node.GetId()); ok {
		digest = prev.GetWorkspaceDigest()
	}
	d.notePeer(node)
	// A node that reads this peer's workspace without merging it, as a
	// phone does, learns from the nodes changing that it may read it again.
	if from.TrustedBy && len(node.GetWorkspaceDigest()) > 0 && !bytes.Equal(digest, node.GetWorkspaceDigest()) {
		d.changes.fire(true, false)
	}
	// A workspace is merged from a peer trusted both ways: one person's
	// nodes, whose windows show each other's sessions; and only while this
	// node shares its own.
	if from.TrustedBy && from.Trusts {
		if d.syncsWorkspace() {
			d.takeWorkspace(node.GetId(), st.GetWorkspace())
			d.takePluginSyncDigest(node.GetId(), st.GetPluginSyncDigest())
		}
		if d.syncsPlugins() {
			d.takePluginConfigSyncDigest(node.GetId(), st.GetPluginConfigSyncDigest())
		}
		if d.syncsAgents() {
			d.takeAgentSyncDigest(node.GetId(), st.GetAgentSyncDigest())
		}
	}
	if from.TrustedBy {
		for _, other := range st.GetPeers() {
			if other.GetId() == nodeID || other.GetPeerId() == "" || other.GetPeerId() == d.peerID() {
				continue
			}
			d.peers.merge(other)
			d.link(other)
		}
		// Its sessions, its own and those of the nodes it reaches, which
		// are read as this daemon routes.
		sessionsChanged, routesChanged := d.remote.put(node.GetId(), st.GetSessions(), reach(st.GetPeers(), nodeID))
		if sessionsChanged || routesChanged {
			d.changes.fire(routesChanged, sessionsChanged)
		}
	}
	if from.Trusts && st.GetWatchingApprovals() {
		d.approvals.watch(false)
	}
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

// fireWorkspace tells every watcher that the workspace the daemon holds
// changed.
func (n *notifier) fireWorkspace() { n.send(&protocol.Changed{Workspace: true}) }

// firePlugins tells every watcher that a plugin came, went, or changed
// how it stands.
func (n *notifier) firePlugins() { n.send(&protocol.Changed{Plugins: true}) }

// fireAgentBlocks tells every watcher that an agent's block came, went or
// moved.
func (n *notifier) fireAgentBlocks() { n.send(&protocol.Changed{AgentBlocks: true}) }

// fireDevices tells every watcher that a device lent or borrowed here
// changed.
func (n *notifier) fireDevices() { n.send(&protocol.Changed{Devices: true}) }

// fireConfig tells every watcher that synced agent config, MCP servers,
// skills, or templates changed on this node.
func (n *notifier) fireConfig() { n.send(&protocol.Changed{Nodes: true, Config: true}) }

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
			next.Workspace = next.Workspace || pending.GetWorkspace()
			next.Devices = next.Devices || pending.GetDevices()
			next.Plugins = next.Plugins || pending.GetPlugins()
			next.AgentBlocks = next.AgentBlocks || pending.GetAgentBlocks()
			next.Config = next.Config || pending.GetConfig()
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
// sessions change, until it hangs up, and counts it as watching for
// approvals meanwhile if it asks.
func (d *Daemon) handleWatch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) (err error) {
	req, _ := protocol.Body[*protocol.Watch](env) // older front-ends send nothing
	if req.GetApprovals() && !fromPeer {
		defer d.approvals.hold()()
	}
	if req.GetLayouts() && !fromPeer {
		defer d.layouts.hold()()
	}
	feed, stop := d.changes.watch()
	defer stop()
	// The first says the daemon watches, and that everything is new to
	// the front-end.
	if err := stream.Send(protocol.TypeChanged, env.GetId(), &protocol.Changed{
		Nodes: true, Sessions: true, Access: true,
		Workspace: true, Devices: true, Plugins: true, AgentBlocks: true,
	}); err != nil {
		return err
	}
	h := watchStreamHangup(stream, nil)
	defer func() {
		err = h.finish(ctx, env.GetId(), err)
	}()
	for {
		select {
		case c := <-feed:
			if err := stream.Send(protocol.TypeChanged, env.GetId(), c); err != nil {
				return err
			}
		case err := <-h.gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
