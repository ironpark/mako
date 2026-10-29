package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/crypto/ssh"
)

// Trust runs one way. Once libp2p has proved each host holds its peer ID's
// key, the two introduce themselves on the trust protocol: the node each
// is, and the certificate of the owner vouching for it, if one does. Each
// decides on its own whether it lets the other in, and tells it. A host
// serves the requests of a peer it lets in, and sends requests only to a
// peer that lets it in; two hosts that let in neither stay linked, so one
// can ask the other to let it in.
//
// The two introduce themselves again whenever either's keys change, so a
// key let in or taken out holds at once.

const trustProtocol protocol.ID = "/mako/trust/1"

// ErrNotTrusted is a request to a peer that does not let this host in.
var ErrNotTrusted = errors.New("p2p: the peer has not let this node in")

// intro is what a host says of itself on the trust protocol.
type intro struct {
	Node string `json:"node"` // its node ID
	Name string `json:"name"`
	OS   string `json:"os,omitempty"`   // as Go names it: darwin, linux, windows, android
	Arch string `json:"arch,omitempty"` // as Go names it: arm64, amd64, …
	Cert *Cert  `json:"cert,omitempty"`
}

// verdict is whether a host lets the other in.
type verdict struct {
	Trusts bool `json:"trusts"`
}

// Peer is how a host stands with a peer.
type Peer struct {
	ID     peer.ID
	Node   string // its node ID
	Name   string
	OS     string // as Go names it, "" from an older build
	Arch   string
	Key    ssh.PublicKey // its own
	Owner  ssh.PublicKey // the SSH key vouching for it; nil without
	Addr   ma.Multiaddr  // where it was last linked
	Trusts bool          // this host lets it in
	// TrustedBy says the peer lets this host in.
	TrustedBy bool
}

type trust struct {
	h       host.Host
	keys    *Keys
	sshKeys *Keys // the user's ~/.ssh/authorized_keys, whose keys vouch
	log     *slog.Logger
	owner   ssh.Signer    // the SSH key vouching for this host, nil for none
	own     ssh.PublicKey // owner's public half

	mu    sync.Mutex
	self  intro // what this host says of itself
	peers map[peer.ID]*trustState
	// changed is told of a peer each time it introduces itself again.
	changed func(Peer)
}

// trustState is where a peer stands: done is closed once the two have
// first introduced themselves, err saying whether that failed.
type trustState struct {
	started bool
	done    chan struct{}
	err     error
	peer    Peer
}

func newTrust(keys, sshKeys *Keys, self intro, owner ssh.Signer, log *slog.Logger) *trust {
	t := &trust{keys: keys, sshKeys: sshKeys, self: self, owner: owner, log: log, peers: map[peer.ID]*trustState{}}
	if owner != nil {
		t.own = owner.PublicKey()
	}
	return t
}

// vouch has the owner sign this host's certificate, if there is an owner
// and the certificate is missing or runs out within certRenew, and
// reports whether it signed one.
func (t *trust) vouch(id peer.ID) (bool, error) {
	if t.owner == nil {
		return false, nil
	}
	t.mu.Lock()
	cert := t.self.Cert
	t.mu.Unlock()
	if cert != nil && time.Until(time.Unix(cert.Expires, 0)) >= certRenew {
		return false, nil
	}
	cert, err := SignCert(t.owner, id)
	if err != nil {
		return false, err
	}
	t.mu.Lock()
	t.self.Cert = cert
	t.mu.Unlock()
	return true, nil
}

// attach serves the trust protocol on h, and starts it on each connection
// h dials.
func (t *trust) attach(h host.Host) {
	t.h = h
	h.SetStreamHandler(trustProtocol, func(s network.Stream) {
		p := s.Conn().RemotePeer()
		t.settle(p, t.exchange(s))
	})
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			if c.Stat().Direction == network.DirOutbound {
				t.begin(c.RemotePeer())
			}
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			if n.Connectedness(c.RemotePeer()) == network.NotConnected {
				t.mu.Lock()
				delete(t.peers, c.RemotePeer())
				t.mu.Unlock()
			}
		},
	})
}

// state is where p stands, made the first time it is asked for. t.mu is
// held.
func (t *trust) state(p peer.ID) *trustState {
	st := t.peers[p]
	if st == nil {
		st = &trustState{done: make(chan struct{})}
		t.peers[p] = st
	}
	return st
}

// settled is where st stands, once the peer has introduced itself
// without fail. t.mu is held, under which settle closes done.
func settled(st *trustState) (Peer, bool) {
	if st == nil {
		return Peer{}, false
	}
	select {
	case <-st.done:
		return st.peer, st.err == nil
	default:
		return Peer{}, false
	}
}

// get is how this host stands with p, once the two have introduced
// themselves.
func (t *trust) get(p peer.ID) (Peer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return settled(t.peers[p])
}

// list is how this host stands with each peer that introduced itself.
func (t *trust) list() []Peer {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Peer
	for _, st := range t.peers {
		if p, ok := settled(st); ok {
			out = append(out, p)
		}
	}
	return out
}

// trusts reports whether this host lets p in.
func (t *trust) trusts(p peer.ID) bool {
	if t.h != nil && p == t.h.ID() {
		return true
	}
	st, ok := t.get(p)
	return ok && st.Trusts
}

// wait waits until p has first introduced itself, and reports how this
// host stands with it.
func (t *trust) wait(ctx context.Context, p peer.ID) (Peer, error) {
	t.mu.Lock()
	st := t.state(p)
	t.mu.Unlock()
	select {
	case <-st.done:
	case <-ctx.Done():
		return Peer{}, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return st.peer, st.err
}

// begin starts introducing this host to p, once a connection.
func (t *trust) begin(p peer.ID) {
	t.mu.Lock()
	st := t.state(p)
	start := !st.started
	st.started = true
	t.mu.Unlock()
	if start {
		go t.introduce(p)
	}
}

// introduce opens the trust protocol to p and introduces the two.
func (t *trust) introduce(p peer.ID) {
	ctx, cancel := context.WithTimeout(context.Background(), trustTimeout)
	defer cancel()
	s, err := t.h.NewStream(network.WithAllowLimitedConn(ctx, "mako-trust"), p, trustProtocol)
	if err != nil {
		t.settle(p, err)
		return
	}
	t.settle(p, t.exchange(s))
}

// reconsider introduces this host again to every peer, as when its keys
// changed, so each side decides anew.
func (t *trust) reconsider() {
	for _, p := range t.list() {
		go t.introduce(p.ID)
	}
}

// exchange introduces the two hosts on s, which either side opened: each
// says who it is, then whether it lets the other in. It records where the
// peer stands, and returns an error if the peer cannot be told apart.
func (t *trust) exchange(s network.Stream) error {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(trustTimeout))
	p := s.Conn().RemotePeer()
	t.mu.Lock()
	self := t.self
	t.mu.Unlock()
	if err := writeJSON(s, self); err != nil {
		return err
	}
	var theirs intro
	if err := readJSON(s, &theirs); err != nil {
		return err
	}
	key, err := PeerKey(p)
	if err != nil {
		_ = s.Reset()
		return err
	}
	var owner ssh.PublicKey
	if theirs.Cert != nil {
		if owner, err = theirs.Cert.Verify(p); err != nil {
			t.log.Debug("a peer's certificate does not hold", "peer", p, "err", err)
			owner = nil
		}
	}
	trusts := t.keys.Has(key) || t.keys.Has(owner) || t.sshKeys.Has(owner) || sameKey(owner, t.own)
	if err := writeJSON(s, verdict{Trusts: trusts}); err != nil {
		return err
	}
	var v verdict
	if err := readJSON(s, &v); err != nil {
		return err
	}
	t.mu.Lock()
	st := t.state(p)
	st.peer = Peer{ID: p, Node: theirs.Node, Name: theirs.Name, OS: theirs.OS, Arch: theirs.Arch, Key: key, Owner: owner, Addr: s.Conn().RemoteMultiaddr(), Trusts: trusts, TrustedBy: v.Trusts}
	t.mu.Unlock()
	return nil
}

// settle records that p has introduced itself, or failed to. The first
// time decides whether the link is of any use; a failure then hangs up.
func (t *trust) settle(p peer.ID, err error) {
	t.mu.Lock()
	st := t.state(p)
	first := false
	select {
	case <-st.done:
	default:
		first = true
		st.err = err
		close(st.done)
	}
	info, changed := st.peer, t.changed
	t.mu.Unlock()
	if err != nil {
		if first {
			t.log.Debug("peer could not introduce itself", "peer", p, "err", err)
			_ = t.h.Network().ClosePeer(p)
		}
		return
	}
	if changed != nil {
		changed(info)
	}
}

// AllowReserve implements relay.ACLFilter: the relay keeps a slot for a
// host it lets in alone.
func (t *trust) AllowReserve(p peer.ID, _ ma.Multiaddr) bool {
	st, ok := t.arrived(p)
	return ok && st.Trusts
}

// AllowConnect implements relay.ACLFilter: a circuit joins two hosts the
// relay lets in.
func (t *trust) AllowConnect(src peer.ID, _ ma.Multiaddr, dest peer.ID) bool {
	st, ok := t.arrived(src)
	return ok && st.Trusts && t.trusts(dest)
}

// arrived is how this host stands with p, which has just connected, once
// it has introduced itself, waiting for that a while.
func (t *trust) arrived(p peer.ID) (Peer, bool) {
	if st, ok := t.get(p); ok {
		return st, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), trustTimeout)
	defer cancel()
	st, err := t.wait(ctx, p)
	return st, err == nil
}

func sameKey(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

// maxMessage bounds what a host reads of a message on the trust and the
// small protocols beside it.
const maxMessage = 1 << 20

// writeMessage writes data with its length before it.
func writeMessage(w io.Writer, data []byte) error {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// readMessage reads what writeMessage wrote.
func readMessage(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxMessage {
		return nil, fmt.Errorf("p2p: a message of %d bytes", size)
	}
	data := make([]byte, size)
	_, err := io.ReadFull(r, data)
	return data, err
}

func writeJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeMessage(w, data)
}

func readJSON(r io.Reader, v any) error {
	data, err := readMessage(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
