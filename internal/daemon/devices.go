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

// claims are the claims of the devices attached to this node, by ID.
type claims struct {
	mu sync.Mutex
	by map[string]*claim
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
	list, err := k.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(list, func(dev *protocol.Device) bool { return dev.GetSerial() == req.GetSerial() })
	if i < 0 {
		return nil, nil, fmt.Errorf("%s is not attached to %s", req.GetSerial(), d.cfg.Name)
	}
	cl, err := d.claims.take(req, k.Exclusive())
	if err != nil {
		return nil, nil, err
	}
	dev := list[i]
	dev.NodeId, dev.Shared = d.cfg.NodeID, true
	dev.Holders = d.claims.holders(req.GetKind(), req.GetSerial())
	return dev, cl, nil
}

// handleClaim holds a device for a borrower for as long as its stream is
// open, and claimGrace after.
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
	drain(ctx, stream)
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

// claimed is the kind and serial of a claim this node holds.
func (d *Daemon) claimed(id string) (device.Kind, string, error) {
	kind, serial, ok := d.claims.get(id)
	if !ok {
		return nil, "", errors.New("the device is no longer lent to this node; borrow it again")
	}
	k, err := d.kind(kind)
	return k, serial, err
}

// A lease is a device this node borrows.
type lease struct {
	info   *protocol.Lease   // guarded by leases.mu
	claim  *protocol.Claim   // its node is the device's
	stream *transport.Stream // the claim's, nil for a device of this node; guarded by leases.mu
	ln     net.Listener
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

// existing is the lease of a device, if this node borrows it.
func (ls *leases) existing(ref string) *protocol.Lease {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if l := ls.byRef(ref); l != nil {
		return proto.CloneOf(l.info)
	}
	return nil
}

// set records a lease's claim stream as it stands now, and its state.
func (ls *leases) set(l *lease, stream *transport.Stream, state string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l.stream, l.info.State = stream, state
}

func (d *Daemon) handleBorrow(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeLease, func(r *protocol.Borrow) (*protocol.Lease, error) {
		return d.borrow(ctx, r)
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

// borrow borrows a device for this node's programs, or is the lease of
// one it borrows already.
func (d *Daemon) borrow(ctx context.Context, r *protocol.Borrow) (*protocol.Lease, error) {
	k, err := d.kind(r.GetKind())
	if err != nil {
		return nil, err
	}
	devNode := r.GetDeviceNodeId()
	if d.local(devNode) {
		devNode = d.cfg.NodeID
	}
	ref := protocol.DeviceRef(devNode, r.GetKind(), r.GetSerial())
	if info := d.leases.existing(ref); info != nil {
		return info, nil
	}

	req := &protocol.Claim{
		NodeId: devNode, Kind: r.GetKind(), Serial: r.GetSerial(),
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
	d.leases.mu.Lock()
	if other := d.leases.byRef(ref); other != nil { // borrowed at once twice
		info := proto.CloneOf(other.info)
		d.leases.mu.Unlock()
		cancel()
		ln.Close()
		undo()
		return info, nil
	}
	if d.leases.by == nil {
		d.leases.by = map[string]*lease{}
	}
	d.leases.by[l.info.GetId()] = l
	info := proto.CloneOf(l.info)
	d.leases.mu.Unlock()

	d.log.Info("device borrowed", "device", ref, "addr", addr)
	if stream == nil {
		context.AfterFunc(lctx, undo)
	} else {
		go d.keepClaim(lctx, l, stream)
	}
	go d.lend(lctx, l)
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

// drain reads a stream until it ends, or ctx is done, and closes it.
func drain(ctx context.Context, stream *transport.Stream) {
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			return
		}
	}
}

// keepClaim keeps a lease's claim of another node's device until the lease
// ends, claiming the device again whenever the claim's stream ends.
func (d *Daemon) keepClaim(ctx context.Context, l *lease, stream *transport.Stream) {
	for {
		drain(ctx, stream)
		if ctx.Err() != nil {
			return
		}
		d.leases.set(l, nil, protocol.LeaseReconnecting)
		d.log.Info("device's node went; claiming it again", "device", l.info.GetDevice().Ref())
		for wait := time.Second; ; wait = min(2*wait, 15*time.Second) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, again, err := d.claimThere(cctx, l.claim)
			cancel()
			if err == nil {
				stream = again
				break
			}
			d.log.Debug("device not claimed again", "device", l.info.GetDevice().Ref(), "err", err)
		}
		d.leases.set(l, stream, protocol.LeaseHeld)
	}
}

// lend takes the connections this node's programs make to a lease's port,
// and carries each to the device's node.
func (d *Daemon) lend(ctx context.Context, l *lease) {
	stop := context.AfterFunc(ctx, func() { l.ln.Close() })
	defer stop()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			up, err := d.deviceConn(ctx, l.claim.GetNodeId(), l.claim.GetClaimId())
			if err != nil {
				d.log.Debug("device not reached", "device", l.info.GetDevice().Ref(), "err", err)
				return
			}
			if err := transport.Splice(ctx, conn, up); err != nil && !isClosed(err) {
				d.log.Debug("device connection ended", "device", l.info.GetDevice().Ref(), "err", err)
			}
		}()
	}
}

// deviceConn opens a connection to a device under a claim: served here
// for a device of this node, else carried to its node.
func (d *Daemon) deviceConn(ctx context.Context, devNode, claimID string) (io.ReadWriteCloser, error) {
	if d.local(devNode) {
		k, serial, err := d.claimed(claimID)
		if err != nil {
			return nil, err
		}
		ours, theirs := net.Pipe()
		go func() { _ = k.Serve(ctx, serial, theirs) }()
		return ours, nil
	}
	dctx, cancel := context.WithTimeout(ctx, tunnelDial+5*time.Second)
	defer cancel()
	_, stream, err := open[*protocol.Tunneled](dctx, d, protocol.TypeDeviceConn, &protocol.DeviceConn{NodeId: devNode, ClaimId: claimID})
	return stream, err
}

// giveBack ends a lease.
func (d *Daemon) giveBack(r *protocol.GiveBack) (*protocol.Lease, error) {
	d.leases.mu.Lock()
	l := d.leases.by[r.GetLeaseId()]
	if l == nil && r.GetRef() != "" {
		l = d.leases.byRef(r.GetRef())
	}
	if l == nil {
		d.leases.mu.Unlock()
		return nil, errors.New("this node does not borrow that device")
	}
	delete(d.leases.by, l.info.GetId())
	l.info.State = protocol.LeaseReturned
	info := proto.CloneOf(l.info)
	d.leases.mu.Unlock()
	l.cancel()
	d.log.Info("device returned", "device", info.GetDevice().Ref())
	return info, nil
}

// deviceEnv borrows a session's devices, and is the environment that
// lends them to its programs. Two that would set one variable apart are
// refused.
func (d *Daemon) deviceEnv(ctx context.Context, refs []string) (map[string]string, error) {
	env := map[string]string{}
	setBy := map[string]string{} // the ref that set each variable
	for _, ref := range refs {
		node, kind, serial, err := protocol.ParseDeviceRef(ref)
		if err != nil {
			return nil, err
		}
		l, err := d.borrow(ctx, &protocol.Borrow{DeviceNodeId: node, Kind: kind, Serial: serial})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
		for k, v := range l.GetEnv() {
			if old, ok := env[k]; ok && old != v {
				return nil, fmt.Errorf("%s and %s both set %s: a session takes one of them", setBy[k], ref, k)
			}
			env[k], setBy[k] = v, ref
		}
	}
	return env, nil
}
