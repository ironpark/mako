package p2p

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// Two hosts show each other they know the mesh's key without sending it.
// libp2p has already proved each is the peer ID it claims; on the auth
// protocol the one that dialed sends a nonce, the other answers with one
// of its own and a MAC over its ID, the dialer's and the dialer's nonce,
// and the dialer answers with a MAC over the same the other way round.
// Neither MAC can be replayed to another host or to another connection,
// and a host that does not know the key learns nothing of it.
//
// A host that fails is hung up on; one that has passed is let in until it
// has no connection left.

const authProtocol protocol.ID = "/sanbo/auth/1"

const nonceSize = 32

var errDenied = errors.New("p2p: the peer does not know the mesh's key")

type auth struct {
	key []byte
	log *slog.Logger
	h   host.Host

	mu    sync.Mutex
	peers map[peer.ID]*authState
}

// authState is where one peer stands: done is closed once it is known
// whether it passed, which err says.
type authState struct {
	started bool
	done    chan struct{}
	err     error
}

func newAuth(psk string, log *slog.Logger) *auth {
	return &auth{key: []byte(psk), log: log, peers: map[peer.ID]*authState{}}
}

// attach serves the auth protocol on h, and starts it on each connection
// h dials.
func (a *auth) attach(h host.Host) {
	a.h = h
	h.SetStreamHandler(authProtocol, a.answer)
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			if c.Stat().Direction == network.DirOutbound {
				a.begin(c.RemotePeer())
			}
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			if n.Connectedness(c.RemotePeer()) == network.NotConnected {
				a.mu.Lock()
				delete(a.peers, c.RemotePeer())
				a.mu.Unlock()
			}
		},
	})
}

// state is where p stands, made the first time it is asked for. a.mu is
// held.
func (a *auth) state(p peer.ID) *authState {
	st := a.peers[p]
	if st == nil {
		st = &authState{done: make(chan struct{})}
		a.peers[p] = st
	}
	return st
}

// ok reports whether p has passed.
func (a *auth) ok(p peer.ID) bool {
	if a.h != nil && p == a.h.ID() {
		return true
	}
	a.mu.Lock()
	st := a.peers[p]
	a.mu.Unlock()
	if st == nil {
		return false
	}
	select {
	case <-st.done:
		return st.err == nil
	default:
		return false
	}
}

// wait waits until it is known whether p passed.
func (a *auth) wait(ctx context.Context, p peer.ID) error {
	a.mu.Lock()
	st := a.state(p)
	a.mu.Unlock()
	select {
	case <-st.done:
		return st.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settle records whether p passed, unless that is known already; a peer
// that failed is hung up on.
func (a *auth) settle(p peer.ID, err error) {
	a.mu.Lock()
	st := a.state(p)
	select {
	case <-st.done:
		a.mu.Unlock()
		return
	default:
	}
	st.err = err
	close(st.done)
	a.mu.Unlock()
	if err != nil {
		a.log.Warn("peer refused", "peer", p, "err", err)
		_ = a.h.Network().ClosePeer(p)
	}
}

// begin starts showing p the key from this side, once.
func (a *auth) begin(p peer.ID) {
	a.mu.Lock()
	st := a.state(p)
	start := !st.started
	st.started = true
	a.mu.Unlock()
	if start {
		go func() { a.settle(p, a.ask(p)) }()
	}
}

// ask runs the dialer's side with p.
func (a *auth) ask(p peer.ID) error {
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	s, err := a.h.NewStream(network.WithAllowLimitedConn(ctx, "sanbo-auth"), p, authProtocol)
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(authTimeout))
	mine := make([]byte, nonceSize)
	_, _ = rand.Read(mine)
	if _, err := s.Write(mine); err != nil {
		return err
	}
	reply := make([]byte, nonceSize+sha256.Size)
	if _, err := io.ReadFull(s, reply); err != nil {
		return err
	}
	theirs, theirMAC := reply[:nonceSize], reply[nonceSize:]
	if !hmac.Equal(theirMAC, a.mac("answer", p, a.h.ID(), mine)) {
		_ = s.Reset()
		return errDenied
	}
	if _, err := s.Write(a.mac("ask", a.h.ID(), p, theirs)); err != nil {
		return err
	}
	var verdict [1]byte
	if _, err := io.ReadFull(s, verdict[:]); err != nil || verdict[0] != 1 {
		return errDenied
	}
	return nil
}

// answer runs the other side, for the host that dialed.
func (a *auth) answer(s network.Stream) {
	p := s.Conn().RemotePeer()
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(authTimeout))
	theirs := make([]byte, nonceSize)
	if _, err := io.ReadFull(s, theirs); err != nil {
		_ = s.Reset()
		return
	}
	mine := make([]byte, nonceSize)
	_, _ = rand.Read(mine)
	if _, err := s.Write(append(mine, a.mac("answer", a.h.ID(), p, theirs)...)); err != nil {
		return
	}
	theirMAC := make([]byte, sha256.Size)
	if _, err := io.ReadFull(s, theirMAC); err != nil {
		return
	}
	if !hmac.Equal(theirMAC, a.mac("ask", p, a.h.ID(), mine)) {
		_, _ = s.Write([]byte{0})
		a.settle(p, errDenied)
		return
	}
	if _, err := s.Write([]byte{1}); err != nil {
		return
	}
	a.settle(p, nil)
}

// mac is the MAC of role, by the host from, for the host to, over nonce.
func (a *auth) mac(role string, from, to peer.ID, nonce []byte) []byte {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte("sanbo/auth/1 " + role + "\x00"))
	m.Write([]byte(from))
	m.Write([]byte{0})
	m.Write([]byte(to))
	m.Write([]byte{0})
	m.Write(nonce)
	return m.Sum(nil)
}

// AllowReserve implements relay.ACLFilter: the relay keeps a slot for a
// host of the mesh alone.
func (a *auth) AllowReserve(p peer.ID, _ ma.Multiaddr) bool { return a.passes(p) }

// AllowConnect implements relay.ACLFilter: a circuit joins two hosts of
// the mesh.
func (a *auth) AllowConnect(src peer.ID, _ ma.Multiaddr, dest peer.ID) bool {
	return a.passes(src) && a.ok(dest)
}

// passes waits for p, which has just connected, to show it knows the key.
func (a *auth) passes(p peer.ID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	return a.wait(ctx, p) == nil
}
