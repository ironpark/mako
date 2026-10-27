// Package p2p links mako daemons to each other over libp2p.
//
// Each daemon is a libp2p host with an identity of its own, kept in its
// configuration directory, and listens on QUIC and TCP. Peers reach each
// other directly where they can, through a relay run by a mako node with
// a public address where they cannot, and then try to punch through to a
// direct connection. Who may ask what of whom is a matter of keys, and
// runs one way (see trust.go): a host serves the mako protocol's streams,
// and sends its state, to the peers it lets in.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/crypto/ssh"
)

// Protocol is the libp2p protocol the mako protocol's frames travel on,
// one request stream to a libp2p stream, as on QUIC.
const Protocol protocol.ID = "/mako/2"

// stateProtocol carries a host's state to a peer, one message a stream,
// and accessProtocol a host's asking a peer to let it in.
const (
	stateProtocol  protocol.ID = "/mako/state/2"
	accessProtocol protocol.ID = "/mako/access/1"
)

// keyFile holds the host's identity in the configuration directory.
const keyFile = "peer.key"

// Config is how a host listens and whom it relies on.
type Config struct {
	// Dir keeps the identity.
	Dir string
	// Host is the address to listen on, "" for every interface.
	Host string
	// Port is the UDP and TCP port; 0 picks one.
	Port int
	// Node and Name are the daemon's node ID and name, which the host
	// introduces itself to its peers with.
	Node, Name string
	// Owner is the SSH key that vouches for the host, nil for none.
	Owner ssh.Signer
	// OnPeer is told how the host stands with a peer each time the two
	// introduce themselves.
	OnPeer func(Peer)
	// Relays are relay nodes' multiaddrs, each with its /p2p/ ID, that
	// this host reserves a slot on to be reachable behind NAT.
	Relays []string
	// RelayService makes this host a relay for the mesh's other nodes,
	// which a node with a public address can be.
	RelayService bool
	// UserAgent names the build to other hosts.
	UserAgent string
	// NoNAT leaves out the router port mapping, as tests do.
	NoNAT bool
	Log   *slog.Logger
}

// Host is a daemon's libp2p host. Its libp2p host is kept to itself, so
// every way in, a stream, a state, a relay circuit, goes through the
// check that it lets the other side in.
type Host struct {
	h     host.Host
	trust *trust
	log   *slog.Logger
	done  chan struct{}
	once  sync.Once
}

// New starts a host. It runs until Close.
func New(ctx context.Context, cfg Config) (*Host, error) {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	key, err := identity(cfg.Dir)
	if err != nil {
		return nil, err
	}
	relays, err := ParseRelays(cfg.Relays)
	if err != nil {
		return nil, err
	}
	h := &Host{log: log, done: make(chan struct{}),
		trust: newTrust(NewKeys(cfg.Dir), intro{Node: cfg.Node, Name: cfg.Name}, cfg.Owner, log)}
	h.trust.changed = cfg.OnPeer
	hosts := []string{"0.0.0.0", "::"}
	if cfg.Host != "" {
		hosts = []string{cfg.Host}
	}
	var listen []string
	for _, ip := range hosts {
		listen = append(listen, AddrsAt(ip, cfg.Port)...)
	}
	opts := []libp2p.Option{
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(listen...),
		libp2p.UserAgent(cfg.UserAgent),
		libp2p.EnableHolePunching(),
		// Each node tells the others whether it could dial them back, by
		// which one behind NAT knows to take a relay's slot.
		libp2p.EnableNATService(),
	}
	if !cfg.NoNAT {
		opts = append(opts, libp2p.NATPortMap())
	}
	if len(relays) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(relays))
	}
	if cfg.RelayService {
		// The mesh's own relay: its circuits are for the nodes it lets in
		// alone, and are not cut short, as a public relay's are, since a
		// session may run over one for hours where no hole could be
		// punched.
		opts = append(opts,
			libp2p.EnableRelayService(relay.WithInfiniteLimits(), relay.WithACL(h.trust)),
			libp2p.ForceReachabilityPublic())
	}
	h.h, err = libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("libp2p: %w", err)
	}
	if _, err := h.trust.vouch(h.h.ID()); err != nil {
		h.h.Close()
		return nil, fmt.Errorf("vouching with the SSH key: %w", err)
	}
	h.trust.attach(h.h)
	go h.watch()
	return h, nil
}

// watch introduces the host to its peers again when its keys change, and
// renews its certificate before it runs out, until Close.
func (h *Host) watch() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-tick.C:
		}
		renewed, err := h.trust.vouch(h.h.ID())
		if err != nil {
			h.log.Warn("could not renew the SSH key's certificate", "err", err)
		}
		if h.trust.keys.reload() || renewed {
			h.trust.reconsider()
		}
	}
}

// ID is the host's peer ID.
func (h *Host) ID() peer.ID { return h.h.ID() }

// Close stops the host.
func (h *Host) Close() error {
	h.once.Do(func() { close(h.done) })
	return h.h.Close()
}

// Keys are the keys the host lets in. After changing them, Reconsider.
func (h *Host) Keys() *Keys { return h.trust.keys }

// Reconsider introduces the host to its peers again, so a key let in or
// taken out holds at once.
func (h *Host) Reconsider() { h.trust.reconsider() }

// Key is the host's own key, as SSH writes it.
func (h *Host) Key() ssh.PublicKey {
	key, _ := PeerKey(h.h.ID())
	return key
}

// Owner is the SSH key that vouches for the host, nil for none.
func (h *Host) Owner() ssh.PublicKey { return h.trust.own }

// Peer is how the host stands with the peer id, once the two have
// introduced themselves.
func (h *Host) Peer(id string) (Peer, bool) {
	pid, err := peer.Decode(id)
	if err != nil {
		return Peer{}, false
	}
	return h.trust.get(pid)
}

// Peers is how the host stands with each peer linked to it.
func (h *Host) Peers() []Peer { return h.trust.list() }

// Connected reports whether the host has a link to the peer id.
func (h *Host) Connected(id string) bool {
	pid, err := peer.Decode(id)
	return err == nil && h.h.Network().Connectedness(pid) == network.Connected
}

// Addrs are the host's addresses to give other nodes: where it listens
// and the relays it is reachable through, without loopback ones.
func (h *Host) Addrs() []string {
	var out, loopback []string
	for _, a := range h.h.Addrs() {
		if manet.IsIPLoopback(a) {
			loopback = append(loopback, a.String())
		} else {
			out = append(out, a.String())
		}
	}
	if len(out) == 0 {
		return loopback // a host listening on loopback alone, as in tests
	}
	return out
}

// Port is the TCP port the host listens on, which a probe reaches, 0 if
// it cannot tell.
func (h *Host) Port() int {
	_, port := h.tcp()
	return port
}

// tcp is the host's first TCP address that is not loopback, or else a
// loopback one: its IP and port.
func (h *Host) tcp() (net.IP, int) {
	var ip net.IP
	port := 0
	for _, a := range h.h.Addrs() {
		v, err := a.ValueForProtocol(ma.P_TCP)
		if err != nil {
			continue
		}
		at, err := manet.ToIP(a)
		if err != nil {
			continue
		}
		n, _ := strconv.Atoi(v)
		if !at.IsLoopback() {
			return at, n
		}
		if ip == nil {
			ip, port = at, n
		}
	}
	return ip, port
}

// HandleStreams serves the mako protocol's streams with fn, from the hosts
// it lets in; another's are reset.
func (h *Host) HandleStreams(fn func(s network.Stream)) {
	h.h.SetStreamHandler(Protocol, func(s network.Stream) {
		if p, ok := h.trust.arrived(s.Conn().RemotePeer()); !ok || !p.Trusts {
			_ = s.Reset()
			return
		}
		fn(s)
	})
}

// Open opens a mako protocol stream to the peer id, reached at addrs if
// it is not connected already. It may go through a relay. A peer that
// does not let this host in is ErrNotTrusted.
func (h *Host) Open(ctx context.Context, id string, addrs []string) (network.Stream, error) {
	p, err := h.linkTo(ctx, id, addrs)
	if err != nil {
		return nil, err
	}
	if !p.TrustedBy {
		return nil, ErrNotTrusted
	}
	return h.h.NewStream(network.WithAllowLimitedConn(ctx, "mako"), p.ID, Protocol)
}

// Ensure links to the peer id, at addrs if it is not connected already,
// and waits for the two to introduce themselves.
func (h *Host) Ensure(ctx context.Context, id string, addrs []string) error {
	_, err := h.linkTo(ctx, id, addrs)
	return err
}

// EnsureAddr is Ensure for a multiaddr ending in the peer's /p2p/ ID.
func (h *Host) EnsureAddr(ctx context.Context, addr string) error {
	info, err := peer.AddrInfoFromString(addr)
	if err != nil {
		return err
	}
	_, err = h.link(ctx, *info)
	return err
}

// link connects to info, which libp2p does at once when it is connected
// already, and waits for the two to introduce themselves.
func (h *Host) link(ctx context.Context, info peer.AddrInfo) (Peer, error) {
	if info.ID == h.h.ID() {
		return Peer{}, errors.New("p2p: a link to itself")
	}
	if err := h.h.Connect(network.WithAllowLimitedConn(ctx, "mako"), info); err != nil {
		return Peer{}, err
	}
	h.trust.begin(info.ID)
	return h.trust.wait(ctx, info.ID)
}

// HandleState takes in the states peers send with fn: from a peer that
// lets this host in, or that this host lets in.
func (h *Host) HandleState(fn func(from Peer, data []byte)) {
	h.h.SetStreamHandler(stateProtocol, func(s network.Stream) {
		defer s.Close()
		p, ok := h.trust.get(s.Conn().RemotePeer())
		if !ok || !(p.Trusts || p.TrustedBy) {
			_ = s.Reset()
			return
		}
		_ = s.SetReadDeadline(time.Now().Add(trustTimeout))
		data, err := readMessage(s)
		if err != nil {
			_ = s.Reset()
			return
		}
		fn(p, data)
	})
}

// SendState sends data, a state, to the peer id, which must be linked.
func (h *Host) SendState(ctx context.Context, id peer.ID, data []byte) error {
	s, err := h.h.NewStream(network.WithAllowLimitedConn(ctx, "mako-state"), id, stateProtocol)
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetWriteDeadline(time.Now().Add(trustTimeout))
	return writeMessage(s, data)
}

// HandleAccess takes in with fn the peers' asking this host to let them
// in, and what they say with it.
func (h *Host) HandleAccess(fn func(from Peer, note string)) {
	h.h.SetStreamHandler(accessProtocol, func(s network.Stream) {
		defer s.Close()
		p, ok := h.trust.arrived(s.Conn().RemotePeer())
		if !ok {
			_ = s.Reset()
			return
		}
		_ = s.SetReadDeadline(time.Now().Add(trustTimeout))
		var req struct {
			Note string `json:"note"`
		}
		if err := readJSON(s, &req); err != nil {
			_ = s.Reset()
			return
		}
		fn(p, req.Note)
		_ = writeMessage(s, nil)
	})
}

// RequestAccess asks the peer id, reached at addrs if it is not linked
// already, to let this host in, saying note.
func (h *Host) RequestAccess(ctx context.Context, id string, addrs []string, note string) error {
	p, err := h.linkTo(ctx, id, addrs)
	if err != nil {
		return err
	}
	s, err := h.h.NewStream(network.WithAllowLimitedConn(ctx, "mako-access"), p.ID, accessProtocol)
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(trustTimeout))
	if err := writeJSON(s, map[string]string{"note": note}); err != nil {
		return err
	}
	_, err = readMessage(s)
	return err
}

// linkTo links to the peer id, at those of addrs that parse.
func (h *Host) linkTo(ctx context.Context, id string, addrs []string) (Peer, error) {
	pid, err := peer.Decode(id)
	if err != nil {
		return Peer{}, err
	}
	info := peer.AddrInfo{ID: pid}
	for _, a := range addrs {
		if m, err := ma.NewMultiaddr(a); err == nil {
			info.Addrs = append(info.Addrs, m)
		}
	}
	return h.link(ctx, info)
}

// AddrsAt are the addresses a host at ip listens on with port: QUIC and
// TCP.
func AddrsAt(ip string, port int) []string {
	family := "ip4"
	if strings.Contains(ip, ":") {
		family = "ip6"
	}
	return addrs(family, ip, port)
}

// addrs are a host's QUIC and TCP addresses, at host of the multiaddr
// protocol family.
func addrs(family, host string, port int) []string {
	return []string{
		fmt.Sprintf("/%s/%s/udp/%d/quic-v1", family, host, port),
		fmt.Sprintf("/%s/%s/tcp/%d", family, host, port),
	}
}

// identity is the host's key, read from dir, or made and kept there the
// first time. Without a dir it is made afresh.
func identity(dir string) (crypto.PrivKey, error) {
	if dir == "" {
		key, _, err := crypto.GenerateEd25519Key(nil)
		return key, err
	}
	path := filepath.Join(dir, keyFile)
	if data, err := os.ReadFile(path); err == nil {
		return crypto.UnmarshalPrivateKey(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}
	data, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// ParseRelays parses relay multiaddrs, each with its /p2p/ ID.
func ParseRelays(addrs []string) ([]peer.AddrInfo, error) {
	var out []peer.AddrInfo
	for _, a := range addrs {
		info, err := peer.AddrInfoFromString(a)
		if err != nil {
			return nil, fmt.Errorf("relay %q: %w", a, err)
		}
		out = append(out, *info)
	}
	return out, nil
}

// trustTimeout bounds how long a host waits for another to introduce
// itself, and for a message on the small protocols beside it.
const trustTimeout = 10 * time.Second

// HostPort is where the host listens for TCP, as host:port, for people to
// read: its first address that is not loopback, or a loopback one.
func (h *Host) HostPort() string {
	ip, port := h.tcp()
	if ip == nil {
		return ""
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port))
}

// AddrsOf are the addresses of a host at addr, a host name or IP with a
// port, as discovery or the config gives one: QUIC and TCP.
func AddrsOf(addr string) ([]string, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return AddrsAt(ip.String(), port), nil
	}
	return addrs("dns", host, port), nil
}
