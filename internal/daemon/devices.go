package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/device"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Devices attached to one node, used from another; see docs/devices.md.
//
// The node a device is attached to lends it under a claim: a borrowing
// daemon claims it on a stream it keeps open, and the node holds the
// device for it while the stream is open and for claimGrace after, for the
// borrower to claim it again, as after the network dropped. The borrowing
// daemon lends the device to its own programs at a port on its loopback,
// and carries each connection made to it to the device's node, under the
// claim, where the device's kind serves it. Both legs go through the
// daemons between, as a tunnel's do.

// claimGrace is how long a node holds a device after its borrower's claim
// stream ends, and deviceListTimeout how long a node is asked for its
// devices.
const (
	claimGrace        = 30 * time.Second
	deviceListTimeout = 5 * time.Second
)

// deviceKinds are the kinds of device a node lends and borrows.
func deviceKinds(cfg *config.Config) map[string]device.Kind {
	return map[string]device.Kind{
		protocol.DeviceADB: &device.ADB{Server: cfg.Devices.ADBServer},
	}
}

func (d *Daemon) kind(name string) (device.Kind, error) {
	k, ok := d.kinds[name]
	if !ok {
		return nil, fmt.Errorf("no device kind %q", name)
	}
	return k, nil
}

func (d *Daemon) handleListDevices(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	req, err := protocol.Body[*protocol.ListDevices](env)
	if err != nil {
		return err
	}
	var out *protocol.Devices
	switch {
	case !d.local(req.GetNodeId()):
		out, err = forward[*protocol.Devices](ctx, d, env.Type(), req)
	case req.GetMesh() && !fromPeer:
		out = d.meshDevices(ctx)
	default:
		out = d.localDevices(ctx)
	}
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypeDevices, env.GetId(), out)
}

// localDevices are the devices attached to this node, of every kind.
func (d *Daemon) localDevices(ctx context.Context) *protocol.Devices {
	out := &protocol.Devices{}
	for _, name := range slices.Sorted(maps.Keys(d.kinds)) {
		list, err := d.kinds[name].List(ctx)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %s: %v", d.cfg.Name, name, err))
			continue
		}
		for _, dev := range list {
			dev.NodeId = d.cfg.NodeID
			dev.Shared = d.cfg.Devices.Shares(name)
			dev.Holders = d.claims.holders(name, dev.GetSerial())
			out.Devices = append(out.Devices, dev)
		}
	}
	return out
}

// meshDevices are the devices of this node and of every node it reaches
// that lists its own, asked at once.
func (d *Daemon) meshDevices(ctx context.Context) *protocol.Devices {
	out := &protocol.Devices{}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	add := func(got *protocol.Devices) {
		mu.Lock()
		defer mu.Unlock()
		out.Devices = append(out.Devices, got.GetDevices()...)
		out.Errors = append(out.Errors, got.GetErrors()...)
	}
	wg.Go(func() { add(d.localDevices(ctx)) })
	for id := range d.routing() {
		node, ok := d.peers.get(id)
		if !ok || node.GetRevision() < protocol.RevisionDevices {
			continue
		}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, deviceListTimeout)
			defer cancel()
			got, err := forward[*protocol.Devices](ctx, d, protocol.TypeListDevices, &protocol.ListDevices{NodeId: id})
			if err != nil {
				got = &protocol.Devices{Errors: []string{fmt.Sprintf("%s: %v", node.GetName(), err)}}
			}
			add(got)
		})
	}
	wg.Wait()
	return out
}

// A claim is a device this node lends to a borrower.
type claim struct {
	id, kind, serial string
	holder           *protocol.Holder
	live             int         // the claim streams open for it
	expire           *time.Timer // set while none is
}

// claims are the claims of the devices attached to this node, by ID,
// and the ports listened on for their borrowers, by address.
type claims struct {
	mu        sync.Mutex
	by        map[string]*claim
	listeners map[string]*devListener
}

// devListener is a port of this node listened on for a claim's borrower,
// whose connections wait in conns for the borrower to take them.
type devListener struct {
	claimID string
	ln      net.Listener
	conns   chan net.Conn
	done    chan struct{} // closed once it no longer listens
}

func (c *claims) listen(l *devListener) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listeners == nil {
		c.listeners = map[string]*devListener{}
	}
	c.listeners[l.ln.Addr().String()] = l
}

func (c *claims) unlisten(l *devListener) {
	c.mu.Lock()
	delete(c.listeners, l.ln.Addr().String())
	c.mu.Unlock()
	l.ln.Close()
	close(l.done)
}

// listener is the port listened on at addr for claim claimID.
func (c *claims) listener(addr, claimID string) (*devListener, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.listeners[addr]
	return l, ok && l.claimID == claimID
}

// take holds a device for a claim, or holds it again for one it holds,
// until release; one kind lent to one node at a time is refused to
// another.
func (c *claims) take(req *protocol.Claim, exclusive bool) (*claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.by[req.GetClaimId()]; ok {
		if cl.kind != req.GetKind() || cl.serial != req.GetSerial() {
			return nil, errors.New("that claim is of another device")
		}
		if cl.expire != nil {
			cl.expire.Stop()
			cl.expire = nil
		}
		cl.live++
		return cl, nil
	}
	if exclusive {
		for _, other := range c.by {
			if other.kind == req.GetKind() && other.serial == req.GetSerial() {
				return nil, fmt.Errorf("%s is lent to %s", req.GetSerial(), other.holder.GetName())
			}
		}
	}
	if c.by == nil {
		c.by = map[string]*claim{}
	}
	cl := &claim{
		id: req.GetClaimId(), kind: req.GetKind(), serial: req.GetSerial(), live: 1,
		holder: &protocol.Holder{NodeId: req.GetHolderId(), Name: req.GetHolderName()},
	}
	c.by[cl.id] = cl
	return cl, nil
}

// release lets go of a hold take made: the last one gone, the claim lasts
// for grace, for its borrower to take it again.
func (c *claims) release(cl *claim, grace time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl.live--; cl.live > 0 {
		return
	}
	cl.expire = time.AfterFunc(grace, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if cl.live == 0 && c.by[cl.id] == cl {
			delete(c.by, cl.id)
		}
	})
}

// get is the claim of an ID, while it holds.
func (c *claims) get(id string) (kind, serial string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.by[id]
	if !ok {
		return "", "", false
	}
	return cl.kind, cl.serial, true
}

// holders are the nodes a device is lent to.
func (c *claims) holders(kind, serial string) []*protocol.Holder {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*protocol.Holder
	for _, cl := range c.by {
		if cl.kind == kind && cl.serial == serial && !slices.ContainsFunc(out, func(h *protocol.Holder) bool { return h.GetNodeId() == cl.holder.GetNodeId() }) {
			out = append(out, proto.CloneOf(cl.holder))
		}
	}
	slices.SortFunc(out, func(a, b *protocol.Holder) int { return cmp.Compare(a.GetName(), b.GetName()) })
	return out
}

// claimHere holds a device attached to this node for req, and is the
// device as it is now.
func (d *Daemon) claimHere(ctx context.Context, req *protocol.Claim) (*protocol.Device, *claim, error) {
	k, err := d.kind(req.GetKind())
	if err != nil {
		return nil, nil, err
	}
	if !d.cfg.Devices.Shares(req.GetKind()) {
		return nil, nil, fmt.Errorf("%s does not lend its %s devices: its config's devices.share leaves them out", d.cfg.Name, req.GetKind())
	}
	dev, err := d.attached(ctx, k, req.GetKind(), req.GetSerial())
	if err != nil {
		return nil, nil, err
	}
	if dev == nil {
		return nil, nil, fmt.Errorf("%s is not attached to %s", req.GetSerial(), d.cfg.Name)
	}
	cl, err := d.claims.take(req, k.Exclusive())
	if err != nil {
		return nil, nil, err
	}
	dev.Holders = d.claims.holders(req.GetKind(), req.GetSerial())
	return dev, cl, nil
}

// attached is the device of kind and serial attached to this node, as it
// is now, or nil.
func (d *Daemon) attached(ctx context.Context, k device.Kind, kind, serial string) (*protocol.Device, error) {
	list, err := k.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, dev := range list {
		if dev.GetSerial() == serial {
			dev.NodeId, dev.Shared = d.cfg.NodeID, d.cfg.Devices.Shares(kind)
			return dev, nil
		}
	}
	return nil, nil
}

// deviceWatchEvery is how often a node looks at a device it lends, to
// tell its borrower when it changed; a variable for the tests.
var deviceWatchEvery = 5 * time.Second

// watchDevice calls changed with a device lent from this node each time
// its state changes, until ctx is done: one no longer attached is
// DeviceDetached.
func (d *Daemon) watchDevice(ctx context.Context, last *protocol.Device, changed func(*protocol.Device)) {
	k, err := d.kind(last.GetKind())
	if err != nil {
		return
	}
	t := time.NewTicker(deviceWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now, err := d.attached(ctx, k, last.GetKind(), last.GetSerial())
		if err != nil {
			continue
		}
		if now == nil {
			now = proto.CloneOf(last)
			now.State = protocol.DeviceDetached
		}
		if now.GetState() != last.GetState() {
			now.Holders = d.claims.holders(now.GetKind(), now.GetSerial())
			last = now
			changed(now)
		}
	}
}

// handleClaim holds a device for a borrower for as long as its stream is
// open, and claimGrace after, and tells the borrower on it when the device
// changes.
func (d *Daemon) handleClaim(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.Claim](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeClaim, req, stream)
	}
	dev, cl, err := d.claimHere(ctx, req)
	if err != nil {
		return err
	}
	defer d.claims.release(cl, claimGrace)
	d.log.Info("device lent", "device", dev.Ref(), "to", req.GetHolderName())
	if err := stream.Send(protocol.TypeClaimed, env.GetId(), &protocol.Claimed{Device: dev}); err != nil {
		return err
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.watchDevice(wctx, dev, func(now *protocol.Device) {
		_ = stream.Send(protocol.TypeClaimed, env.GetId(), &protocol.Claimed{Device: now})
	})
	drain(ctx, stream, nil)
	return io.EOF // the borrower went: the stream is done
}

// handleDeviceConn serves one connection a borrower's program made to a
// device it claimed.
func (d *Daemon) handleDeviceConn(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.DeviceConn](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeDeviceConn, req, stream)
	}
	k, serial, err := d.claimed(req.GetClaimId())
	if err != nil {
		return err
	}
	if err := stream.Send(protocol.TypeTunneled, env.GetId(), &protocol.Tunneled{Addr: serial}); err != nil {
		return err
	}
	if err := k.Serve(ctx, serial, stream); err != nil && !isClosed(err) {
		d.log.Debug("device connection ended", "serial", serial, "err", err)
	}
	return io.EOF
}

// handleDeviceListen listens on a port of this node's loopback for a
// claim's borrower, for as long as its stream stays open.
func (d *Daemon) handleDeviceListen(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.DeviceListen](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeDeviceListen, req, stream)
	}
	if _, _, err := d.claimed(req.GetClaimId()); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	l := &devListener{claimID: req.GetClaimId(), ln: ln, conns: make(chan net.Conn, 16), done: make(chan struct{})}
	d.claims.listen(l)
	defer d.claims.unlisten(l)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case l.conns <- c:
			default:
				c.Close() // more waiting than the borrower takes
			}
		}
	}()
	if err := stream.Send(protocol.TypeTunneled, env.GetId(), &protocol.Tunneled{Addr: ln.Addr().String()}); err != nil {
		return err
	}
	drain(ctx, stream, nil)
	return io.EOF
}

// handleDeviceAccept hands a borrower the next connection made to a port
// listened on for it.
func (d *Daemon) handleDeviceAccept(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.DeviceAccept](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeDeviceAccept, req, stream)
	}
	l, ok := d.claims.listener(req.GetAddr(), req.GetClaimId())
	if !ok {
		return fmt.Errorf("nothing listens on %s for this node", req.GetAddr())
	}
	var c net.Conn
	select {
	case c = <-l.conns:
	case <-l.done:
		return fmt.Errorf("%s is no longer listened on", req.GetAddr())
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := stream.Send(protocol.TypeTunneled, env.GetId(), &protocol.Tunneled{Addr: c.RemoteAddr().String()}); err != nil {
		select { // the borrower went; the next to wait takes it
		case l.conns <- c:
		default:
			c.Close()
		}
		return err
	}
	if err := transport.Splice(ctx, stream, c); err != nil && !isClosed(err) {
		d.log.Debug("device's connection ended", "addr", req.GetAddr(), "err", err)
	}
	return io.EOF
}

// claimed is the kind and serial of a claim this node holds.
func (d *Daemon) claimed(id string) (device.Kind, string, error) {
	kind, serial, ok := d.claims.get(id)
	if !ok {
		return nil, "", errors.New("the device is no longer lent to this node; borrow it again")
	}
	k, err := d.kind(kind)
	return k, serial, err
}

// drain reads a stream until it ends, or ctx is done, handing each frame
// to each, if any, and closes it.
func drain(ctx context.Context, stream *transport.Stream, each func(*protocol.Envelope)) {
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	defer stream.Close()
	for {
		env, err := stream.Recv()
		if err != nil {
			return
		}
		if each != nil {
			each(env)
		}
	}
}

// A lease is a device this node borrows.
type lease struct {
	info   *protocol.Lease   // guarded by leases.mu
	claim  *protocol.Claim   // its node is the device's
	stream *transport.Stream // the claim's, nil for a device of this node; guarded by leases.mu
	ln     net.Listener
	lent   device.Lent
	cancel context.CancelFunc
}

// leases are the devices this node borrows, by lease ID.
type leases struct {
	mu sync.Mutex
	by map[string]*lease
}

// byRef is the lease of a device, if this node borrows it.
func (ls *leases) byRef(ref string) *lease {
	for _, l := range ls.by {
		if l.info.GetDevice().Ref() == ref {
			return l
		}
	}
	return nil
}

func (ls *leases) list() []*protocol.Lease {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]*protocol.Lease, 0, len(ls.by))
	for _, l := range ls.by {
		out = append(out, proto.CloneOf(l.info))
	}
	slices.SortFunc(out, func(a, b *protocol.Lease) int { return cmp.Compare(a.GetDevice().Ref(), b.GetDevice().Ref()) })
	return out
}

// hold has session, or with "" a borrowing by hand, hold the lease of a
// device, if this node borrows it, and is the lease.
func (ls *leases) hold(ref, session string) *protocol.Lease {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l := ls.byRef(ref)
	if l == nil {
		return nil
	}
	addHolder(l.info, session)
	return proto.CloneOf(l.info)
}

func addHolder(info *protocol.Lease, session string) {
	switch {
	case session == "":
		info.Borrowed = true
	case !slices.Contains(info.GetSessions(), session):
		info.Sessions = append(info.Sessions, session)
	}
}

// set records a lease's claim stream as it stands now, and its state.
func (ls *leases) set(l *lease, stream *transport.Stream, state string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l.stream, l.info.State = stream, state
}

// update records the device of a lease as its node last said it is.
func (ls *leases) update(l *lease, dev *protocol.Device) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l.info.Device = dev
	l.info.State = protocol.LeaseHeld
	if dev.GetState() == protocol.DeviceDetached {
		l.info.State = protocol.LeaseDetached
	}
}

func (d *Daemon) handleBorrow(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeLease, func(r *protocol.Borrow) (*protocol.Lease, error) {
		return d.borrow(ctx, r, "")
	})
}

func (d *Daemon) handleGiveBack(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeLease, func(r *protocol.GiveBack) (*protocol.Lease, error) {
		return d.giveBack(r)
	})
}

func (d *Daemon) handleListLeases(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeLeases, func(r *protocol.ListLeases) (*protocol.Leases, error) {
		return &protocol.Leases{Leases: d.leases.list()}, nil
	})
}

// nodeName names a node by its ID, for people.
func (d *Daemon) nodeName(id string) string {
	if d.local(id) {
		return d.cfg.Name
	}
	if n, ok := d.peers.get(id); ok && n.GetName() != "" {
		return n.GetName()
	}
	return id
}

// resolveDevice is the device a Borrow names: by its node, kind and
// serial, or by Borrow.device among the devices this node reaches.
func (d *Daemon) resolveDevice(ctx context.Context, r *protocol.Borrow) (node, kind, serial string, err error) {
	if r.GetSerial() != "" {
		node = r.GetDeviceNodeId()
		if d.local(node) {
			node = d.cfg.NodeID
		}
		return node, r.GetKind(), r.GetSerial(), nil
	}
	name := r.GetDevice()
	if name == "" {
		return "", "", "", errors.New("no device named")
	}
	// A ref names its device without asking the mesh.
	if node, kind, serial, err := protocol.ParseDeviceRef(name); err == nil {
		if _, ok := d.peers.get(node); ok || d.local(node) {
			return cmp.Or(node, d.cfg.NodeID), kind, serial, nil
		}
	}
	list := d.meshDevices(ctx)
	dev, err := protocol.MatchDevice(list.GetDevices(), d.nodeName, name)
	if err != nil {
		if len(list.GetErrors()) > 0 {
			err = fmt.Errorf("%w; not every node answered: %s", err, strings.Join(list.GetErrors(), "; "))
		}
		return "", "", "", err
	}
	return dev.GetNodeId(), dev.GetKind(), dev.GetSerial(), nil
}

// borrow borrows a device for this node's programs, held by session, or
// with "" by hand; a device it borrows already is held by them too.
func (d *Daemon) borrow(ctx context.Context, r *protocol.Borrow, session string) (*protocol.Lease, error) {
	devNode, kind, serial, err := d.resolveDevice(ctx, r)
	if err != nil {
		return nil, err
	}
	k, err := d.kind(kind)
	if err != nil {
		return nil, err
	}
	ref := protocol.DeviceRef(devNode, kind, serial)
	if info := d.leases.hold(ref, session); info != nil {
		d.changes.fireDevices()
		return info, nil
	}

	req := &protocol.Claim{
		NodeId: devNode, Kind: kind, Serial: serial,
		ClaimId: newSessionID() + newSessionID(), HolderId: d.cfg.NodeID, HolderName: d.cfg.Name,
	}
	var (
		dev    *protocol.Device
		stream *transport.Stream // nil for a device of this node
		undo   func()
	)
	if d.local(devNode) {
		var cl *claim
		dev, cl, err = d.claimHere(ctx, req)
		undo = func() { d.claims.release(cl, 0) }
	} else {
		dev, stream, err = d.claimThere(ctx, req)
		undo = func() { stream.Close() }
	}
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		undo()
		return nil, err
	}
	addr := ln.Addr().String()
	lctx, cancel := context.WithCancel(d.ctx)
	l := &lease{
		info: &protocol.Lease{
			Id: newSessionID(), NodeId: d.cfg.NodeID, Device: dev, Addr: addr,
			Env: k.Env(addr, dev), State: protocol.LeaseHeld, Warnings: k.Check(ctx, dev),
		},
		claim: req, stream: stream, ln: ln, cancel: cancel,
	}
	addHolder(l.info, session)
	d.leases.mu.Lock()
	if d.leases.byRef(ref) != nil { // borrowed at once twice
		d.leases.mu.Unlock()
		cancel()
		ln.Close()
		undo()
		return d.leases.hold(ref, session), nil
	}
	if d.leases.by == nil {
		d.leases.by = map[string]*lease{}
	}
	d.leases.by[l.info.GetId()] = l
	info := proto.CloneOf(l.info)
	d.leases.mu.Unlock()

	d.log.Info("device borrowed", "device", ref, "addr", addr)
	l.lent = k.Lend(lctx, dev, deviceLink{d: d, claim: req})
	context.AfterFunc(lctx, l.lent.Close)
	if stream == nil {
		context.AfterFunc(lctx, undo)
		go d.watchDevice(lctx, dev, func(now *protocol.Device) {
			d.leases.update(l, now)
			d.changes.fireDevices()
		})
	} else {
		go d.keepClaim(lctx, l, stream)
	}
	go d.lend(lctx, l)
	d.changes.fireDevices()
	return info, nil
}

// claimThere claims a device of another node for this one, on a stream
// that holds the claim for as long as it stays open.
func (d *Daemon) claimThere(ctx context.Context, req *protocol.Claim) (*protocol.Device, *transport.Stream, error) {
	if node, err := d.node(req.GetNodeId()); err == nil && node.GetRevision() != 0 && node.GetRevision() < protocol.RevisionDevices {
		return nil, nil, fmt.Errorf("%s runs a mako too old to lend devices", node.GetName())
	}
	got, stream, err := open[*protocol.Claimed](ctx, d, protocol.TypeClaim, req)
	if err != nil {
		return nil, nil, err
	}
	return got.GetDevice(), stream, nil
}

// keepClaim keeps a lease's claim of another node's device until the lease
// ends: it follows what the device's node says of the device, and claims
// the device again whenever the claim's stream ends.
func (d *Daemon) keepClaim(ctx context.Context, l *lease, stream *transport.Stream) {
	for {
		drain(ctx, stream, func(env *protocol.Envelope) {
			if c := env.GetClaimed(); c != nil {
				d.leases.update(l, c.GetDevice())
				d.changes.fireDevices()
			}
		})
		if ctx.Err() != nil {
			return
		}
		d.leases.set(l, nil, protocol.LeaseReconnecting)
		d.changes.fireDevices()
		d.log.Info("device's node went; claiming it again", "device", l.claim.GetSerial())
		for wait := time.Second; ; wait = min(2*wait, 15*time.Second) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			dev, again, err := d.claimThere(cctx, l.claim)
			cancel()
			if err == nil {
				stream = again
				d.leases.set(l, stream, protocol.LeaseHeld)
				d.leases.update(l, dev)
				break
			}
			d.log.Debug("device not claimed again", "device", l.claim.GetSerial(), "err", err)
		}
		d.changes.fireDevices()
	}
}

// lend takes the connections this node's programs make to a lease's port,
// and has the device's kind serve each.
func (d *Daemon) lend(ctx context.Context, l *lease) {
	stop := context.AfterFunc(ctx, func() { l.ln.Close() })
	defer stop()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			if err := l.lent.Serve(ctx, conn); err != nil && !isClosed(err) {
				d.log.Debug("device connection ended", "device", l.claim.GetSerial(), "err", err)
			}
		}()
	}
}

// deviceLink is a lease's way to its device's node.
type deviceLink struct {
	d     *Daemon
	claim *protocol.Claim
}

// Dial opens a connection to the device: served here for a device of this
// node, else carried to its node.
func (k deviceLink) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	d, node, claimID := k.d, k.claim.GetNodeId(), k.claim.GetClaimId()
	if d.local(node) {
		kind, serial, err := d.claimed(claimID)
		if err != nil {
			return nil, err
		}
		ours, theirs := net.Pipe()
		go func() { _ = kind.Serve(ctx, serial, theirs) }()
		return ours, nil
	}
	dctx, cancel := context.WithTimeout(ctx, tunnelDial+5*time.Second)
	defer cancel()
	_, stream, err := open[*protocol.Tunneled](dctx, d, protocol.TypeDeviceConn, &protocol.DeviceConn{NodeId: node, ClaimId: claimID})
	return stream, err
}

// Listen listens on a port of the device's node.
func (k deviceLink) Listen(ctx context.Context) (device.Listener, error) {
	d, node, claimID := k.d, k.claim.GetNodeId(), k.claim.GetClaimId()
	if d.local(node) {
		if _, _, err := d.claimed(claimID); err != nil {
			return nil, err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return localListener{ln}, nil
	}
	if n, err := d.node(node); err == nil && n.GetRevision() != 0 && n.GetRevision() < protocol.RevisionDeviceListen {
		return nil, fmt.Errorf("%s runs a mako too old to listen for a device's borrower", n.GetName())
	}
	lctx, cancel := context.WithTimeout(ctx, tunnelDial+5*time.Second)
	defer cancel()
	got, stream, err := open[*protocol.Tunneled](lctx, d, protocol.TypeDeviceListen, &protocol.DeviceListen{NodeId: node, ClaimId: claimID})
	if err != nil {
		return nil, err
	}
	return &peerListener{link: k, addr: got.GetAddr(), stream: stream}, nil
}

// localListener is a port of this node, for a device of its own.
type localListener struct{ ln net.Listener }

func (l localListener) Addr() string { return l.ln.Addr().String() }
func (l localListener) Close() error { return l.ln.Close() }
func (l localListener) Accept(context.Context) (io.ReadWriteCloser, error) {
	return l.ln.Accept()
}

// peerListener is a port of the device's node, listened on for as long
// as stream stays open.
type peerListener struct {
	link   deviceLink
	addr   string
	stream *transport.Stream
}

func (l *peerListener) Addr() string { return l.addr }
func (l *peerListener) Close() error { return l.stream.Close() }

// Accept waits on the device's node for the next connection to the port.
func (l *peerListener) Accept(ctx context.Context) (io.ReadWriteCloser, error) {
	c := l.link.claim
	_, stream, err := open[*protocol.Tunneled](ctx, l.link.d, protocol.TypeDeviceAccept,
		&protocol.DeviceAccept{NodeId: c.GetNodeId(), ClaimId: c.GetClaimId(), Addr: l.addr})
	return stream, err
}

// findLease is the lease a name names: by its ID, or its device's ref,
// serial or name.
func (ls *leases) find(name string) (*lease, error) {
	var found []*lease
	for _, l := range ls.by {
		dev := l.info.GetDevice()
		if l.info.GetId() == name || dev.Ref() == name {
			return l, nil
		}
		if dev.GetSerial() == name || strings.EqualFold(dev.GetName(), name) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("this node borrows no single device %q", name)
	}
	return found[0], nil
}

// giveBack gives back what this node borrowed by hand; a lease a session
// holds stays until the session ends.
func (d *Daemon) giveBack(r *protocol.GiveBack) (*protocol.Lease, error) {
	d.leases.mu.Lock()
	l, err := d.leases.find(cmp.Or(r.GetLeaseId(), r.GetRef()))
	if err != nil {
		d.leases.mu.Unlock()
		return nil, err
	}
	if !l.info.GetBorrowed() {
		d.leases.mu.Unlock()
		return nil, fmt.Errorf("%s was not borrowed by hand: sessions %s hold it, until they end", l.info.GetDevice().GetSerial(), strings.Join(l.info.GetSessions(), ", "))
	}
	l.info.Borrowed = false
	info := d.endIdleLocked(l)
	d.leases.mu.Unlock()
	d.changes.fireDevices()
	return info, nil
}

// releaseSession lets go of the leases a session held, as it ended.
func (d *Daemon) releaseSession(session string) {
	d.leases.mu.Lock()
	var changed bool
	for _, l := range d.leases.by {
		if i := slices.Index(l.info.GetSessions(), session); i >= 0 {
			l.info.Sessions = slices.Delete(l.info.Sessions, i, i+1)
			d.endIdleLocked(l)
			changed = true
		}
	}
	d.leases.mu.Unlock()
	if changed {
		d.changes.fireDevices()
	}
}

// endIdleLocked ends a lease no one holds, and is the lease as it stands.
func (d *Daemon) endIdleLocked(l *lease) *protocol.Lease {
	if !l.info.GetBorrowed() && len(l.info.GetSessions()) == 0 {
		delete(d.leases.by, l.info.GetId())
		l.info.State = protocol.LeaseReturned
		l.cancel()
		d.log.Info("device returned", "device", l.info.GetDevice().Ref())
	}
	return proto.CloneOf(l.info)
}

// deviceEnv borrows a session's devices, held by the session, and is the
// environment that lends them to its programs. Two that would set one
// variable apart are refused.
func (d *Daemon) deviceEnv(ctx context.Context, session string, names []string) (map[string]string, error) {
	env := map[string]string{}
	setBy := map[string]string{} // the device that set each variable
	for _, name := range names {
		l, err := d.borrow(ctx, &protocol.Borrow{Device: name}, session)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for k, v := range l.GetEnv() {
			if old, ok := env[k]; ok && old != v {
				return nil, fmt.Errorf("%s and %s both set %s: a session takes one of them", setBy[k], name, k)
			}
			env[k], setBy[k] = v, name
		}
	}
	return env, nil
}
