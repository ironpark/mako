package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Access: who this daemon lets in, and the peers asking to be. A request
// waits, in memory, until a front-end of this daemon's allows or denies
// it, or the peer is let in some other way.

// maxRequests bounds the requests waiting, and maxNote the characters of
// what one says.
const (
	maxRequests = 32
	maxNote     = 500
)

// accessRequests are the peers asking to be let in, by node ID.
type accessRequests struct {
	mu      sync.Mutex
	waiting map[string]accessRequest
}

type accessRequest struct {
	req  *protocol.AccessRequest
	peer p2p.Peer
}

// put keeps a request, in place of one the node made before; past
// maxRequests the oldest goes.
func (a *accessRequests) put(r accessRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waiting == nil {
		a.waiting = map[string]accessRequest{}
	}
	id := r.req.GetNode().GetId()
	if _, ok := a.waiting[id]; !ok && len(a.waiting) >= maxRequests {
		oldest := ""
		for k, w := range a.waiting {
			if oldest == "" || w.req.GetAt().AsTime().Before(a.waiting[oldest].req.GetAt().AsTime()) {
				oldest = k
			}
		}
		delete(a.waiting, oldest)
	}
	a.waiting[id] = r
}

func (a *accessRequests) get(node string) (accessRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.waiting[node]
	return r, ok
}

// drop forgets node's request, and reports whether there was one.
func (a *accessRequests) drop(node string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.waiting[node]
	delete(a.waiting, node)
	return ok
}

// list is the requests waiting, oldest first.
func (a *accessRequests) list() []*protocol.AccessRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*protocol.AccessRequest, 0, len(a.waiting))
	for _, r := range a.waiting {
		out = append(out, r.req)
	}
	slices.SortFunc(out, func(x, y *protocol.AccessRequest) int { return x.GetAt().AsTime().Compare(y.GetAt().AsTime()) })
	return out
}

// pairTTL is how long a pairing code lasts unread.
const pairTTL = 10 * time.Minute

// pairing is the pairing code waiting to be read, if one is: its token,
// and when it runs out.
type pairing struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

// renew makes a new code, in place of the one there was.
func (p *pairing) renew() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token, p.expires = p2p.NewPairToken(), time.Now().Add(pairTTL)
}

func (p *pairing) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = ""
}

// current is the code's token and when it runs out, "" for none.
func (p *pairing) current() (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().After(p.expires) {
		p.token = ""
	}
	return p.token, p.expires
}

// use reports whether token is the code's, which it then uses up.
func (p *pairing) use(token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p2p.SameToken(token, p.token) || time.Now().After(p.expires) {
		return false
	}
	p.token = ""
	return true
}

// takeAccessRequest keeps a peer's asking to be let in, for a front-end to
// answer, and tells the watching ones. A peer asking with the token of
// this node's pairing code is let in at once.
func (d *Daemon) takeAccessRequest(from p2p.Peer, note, token string) {
	if from.Trusts || from.Node == "" || from.Node == d.cfg.NodeID {
		return
	}
	if token != "" && d.pairing.use(token) {
		if err := d.p2p.Keys().Add(from.Key, nodeComment(from.Name)); err != nil {
			d.log.Warn("could not let in a node that read the pairing code", "node", from.Name, "err", err)
			return
		}
		d.log.Info("a node that read the pairing code is let in", "node", from.Name, "key", fingerprint(from.Key))
		d.access.drop(from.Node)
		d.p2p.Reconsider()
		d.changes.fireAccess()
		return
	}
	note = agent.OneLine(note, maxNote)
	node, ok := d.peers.get(from.Node)
	if !ok {
		node = &protocol.Node{Id: from.Node, Name: from.Name, PeerId: from.ID.String()}
	}
	d.access.put(accessRequest{peer: from, req: &protocol.AccessRequest{
		Node:  node,
		Key:   accessKey(from.Key, nodeComment(from.Name)),
		Owner: accessKey(from.Owner, ""),
		Note:  note,
		At:    timestamppb.Now(),
	}})
	d.log.Info("a node asks to be let in", "node", from.Name, "key", fingerprint(from.Key), "owner", fingerprint(from.Owner))
	d.changes.fireAccess()
}

// nodeComment is the comment a node's own key is let in with.
func nodeComment(name string) string { return "mako node " + name }

// accessKey describes key, nil for none.
func accessKey(key ssh.PublicKey, comment string) *protocol.AccessKey {
	if key == nil {
		return nil
	}
	return &protocol.AccessKey{Fingerprint: ssh.FingerprintSHA256(key), Type: key.Type(), Comment: comment, Line: p2p.KeyLine(key, comment)}
}

// accessInfo is this daemon's keys, the requests waiting and the keys it
// lets in.
func (d *Daemon) accessInfo() *protocol.AccessInfo {
	info := &protocol.AccessInfo{
		Key:      accessKey(d.p2p.Key(), nodeComment(d.cfg.Name)),
		Owner:    accessKey(d.p2p.Owner(), ""),
		Requests: d.access.list(),
	}
	if token, expires := d.pairing.current(); token != "" {
		code := p2p.Pairing{ID: d.peerID(), Name: d.cfg.Name, Token: token, Addrs: d.multiaddrs()}
		info.Pairing = &protocol.Pairing{Link: code.Link(), Expires: timestamppb.New(expires)}
	}
	for _, keys := range []*p2p.Keys{d.p2p.Keys(), d.p2p.SSHKeys()} {
		for _, k := range keys.List() {
			key := accessKey(k.Key, k.Comment)
			if keys.ReadOnly() {
				key.Source = keys.Path()
			}
			info.Keys = append(info.Keys, key)
		}
	}
	return info
}

// handleAccess lists or changes who this daemon lets in. A peer may not:
// it would let itself in.
func (d *Daemon) handleAccess(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errors.New("access is for this node's own front-ends")
	}
	if d.p2p == nil {
		return errors.New("the mesh is off on this node")
	}
	r, err := protocol.Body[*protocol.Access](env)
	if err != nil {
		return err
	}
	joined, err := d.changeAccess(ctx, r)
	if err != nil {
		return err
	}
	info := d.accessInfo()
	info.Joined = joined
	return stream.Send(protocol.TypeAccessInfo, env.GetId(), info)
}

// changeAccess makes the change r asks for, and returns the name of the
// node a join asked.
func (d *Daemon) changeAccess(ctx context.Context, r *protocol.Access) (string, error) {
	if r.GetOp() == "join" {
		return d.join(ctx, r.GetKey(), r.GetNote())
	}
	return "", d.changeKeys(ctx, r)
}

func (d *Daemon) changeKeys(ctx context.Context, r *protocol.Access) error {
	keys := d.p2p.Keys()
	switch r.GetOp() {
	case "":
		return nil
	case "pair":
		d.pairing.renew()
		d.changes.fireAccess()
		return nil
	case "unpair":
		d.pairing.drop()
		d.changes.fireAccess()
		return nil
	case "request":
		node, err := d.node(r.GetNodeId())
		if err != nil {
			return err
		}
		if node.GetPeerId() == "" {
			return fmt.Errorf("%s is not on libp2p", node.GetName())
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err = d.p2p.RequestAccess(ctx, node.GetPeerId(), node.GetMultiaddrs(), r.GetNote(), "")
		return err
	case "allow":
		w, ok := d.access.get(r.GetNodeId())
		if !ok {
			return fmt.Errorf("no request from node %q", r.GetNodeId())
		}
		key, comment := w.peer.Key, nodeComment(w.peer.Name)
		if r.GetOwner() {
			if w.peer.Owner == nil {
				return fmt.Errorf("no SSH key vouches for %s", w.peer.Name)
			}
			key, comment = w.peer.Owner, "the SSH key of mako node "+w.peer.Name
		}
		if err := keys.Add(key, comment); err != nil {
			return err
		}
		d.access.drop(r.GetNodeId())
	case "deny":
		if !d.access.drop(r.GetNodeId()) {
			return fmt.Errorf("no request from node %q", r.GetNodeId())
		}
	case "add":
		key, comment, err := p2p.ParseKey(r.GetKey())
		if err != nil {
			return err
		}
		if err := keys.Add(key, comment); err != nil {
			return err
		}
	case "revoke":
		ok, err := keys.Remove(r.GetKey())
		if err != nil {
			return err
		}
		if !ok {
			if ok, _ = d.p2p.SSHKeys().Remove(r.GetKey()); ok {
				return fmt.Errorf("%s is let in by %s, which mako does not edit; take it out there", r.GetKey(), d.p2p.SSHKeys().Path())
			}
			return fmt.Errorf("no key %s is let in", r.GetKey())
		}
	default:
		return fmt.Errorf("no access op %q", r.GetOp())
	}
	d.p2p.Reconsider()
	d.changes.fireAccess()
	return nil
}

// join asks the node whose pairing code's link is link to let this one
// in, with the code's token, and keeps the address it was reached at, so
// this node links to it again when it starts, wherever discovery does not
// find it. It returns the node's name.
func (d *Daemon) join(ctx context.Context, link, note string) (string, error) {
	code, err := p2p.ParsePairing(link)
	if err != nil {
		return "", err
	}
	if code.ID == d.peerID() {
		return "", errors.New("this is this node's own pairing code")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	peer, err := d.p2p.RequestAccess(ctx, code.ID, code.Addrs, note, code.Token)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", code.Name, err)
	}
	if peer.Addr != nil {
		d.keepAddr(peer.Addr.String() + "/p2p/" + code.ID)
	}
	if peer.Name != "" {
		return peer.Name, nil
	}
	return code.Name, nil
}

// keepAddr adds addr to the peers config.json has this node always link
// to, unless one there already names its peer, and links to it from now
// on.
func (d *Daemon) keepAddr(addr string) {
	cfg, err := config.Load()
	if err != nil {
		d.log.Warn("could not keep a joined node's address", "addr", addr, "err", err)
		return
	}
	id := addr[strings.LastIndex(addr, "/p2p/"):]
	if slices.ContainsFunc(cfg.Peers, func(p string) bool { return strings.HasSuffix(p, id) }) {
		return
	}
	cfg.Peers = append(cfg.Peers, addr)
	if err := config.Save(cfg); err != nil {
		d.log.Warn("could not keep a joined node's address", "addr", addr, "err", err)
		return
	}
	if d.ctx != nil {
		go d.keepStatic(d.ctx, addr)
	}
}
