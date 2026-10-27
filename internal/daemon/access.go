package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/agent"
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

// takeAccessRequest keeps a peer's asking to be let in, for a front-end to
// answer, and tells the watching ones.
func (d *Daemon) takeAccessRequest(from p2p.Peer, note string) {
	if from.Trusts || from.Node == "" || from.Node == d.cfg.NodeID {
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
	for _, k := range d.p2p.Keys().List() {
		info.Keys = append(info.Keys, accessKey(k.Key, k.Comment))
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
	if err := d.changeAccess(ctx, r); err != nil {
		return err
	}
	return stream.Send(protocol.TypeAccessInfo, env.GetId(), d.accessInfo())
}

func (d *Daemon) changeAccess(ctx context.Context, r *protocol.Access) error {
	keys := d.p2p.Keys()
	switch r.GetOp() {
	case "":
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
		return d.p2p.RequestAccess(ctx, node.GetPeerId(), node.GetMultiaddrs(), r.GetNote())
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
			return fmt.Errorf("no key %s is let in", r.GetKey())
		}
	default:
		return fmt.Errorf("no access op %q", r.GetOp())
	}
	d.p2p.Reconsider()
	d.changes.fireAccess()
	return nil
}
