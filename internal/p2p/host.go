// Package p2p links sanbo daemons to each other over libp2p.
//
// Each daemon is a libp2p host with an identity of its own, kept in its
// configuration directory, and listens on QUIC and TCP. Peers reach each
// other directly where they can, through a relay run by a sanbo node with
// a public address where they cannot, and then try to punch through to a
// direct connection. A connection is of no use until both sides have shown
// they know the mesh's pre-shared key (see auth.go); only then are the
// sanbo protocol's streams and the mesh's state topic open to it.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Protocol is the libp2p protocol the sanbo protocol's frames travel on,
// one request stream to a libp2p stream, as on QUIC.
const Protocol protocol.ID = "/sanbo/2"

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
	// PSK is the mesh's pre-shared key; empty lets in any sanbo daemon.
	PSK string
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
// every way in, a stream, a topic, a relay circuit, goes through the
// check that the other side knows the key.
type Host struct {
	h    host.Host
	auth *auth
	ps   *pubsub.PubSub
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
	h := &Host{auth: newAuth(cfg.PSK, log)}
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
		// The mesh's own relay: its circuits are for the mesh's nodes
		// alone, and are not cut short, as a public relay's are, since a
		// session may run over one for hours where no hole could be
		// punched.
		opts = append(opts,
			libp2p.EnableRelayService(relay.WithInfiniteLimits(), relay.WithACL(h.auth)),
			libp2p.ForceReachabilityPublic())
	}
	h.h, err = libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("libp2p: %w", err)
	}
	h.auth.attach(h.h)
	h.ps, err = pubsub.NewGossipSub(ctx, h.h,
		// Only hosts that proved they know the key take part in the mesh's
		// topics, as senders and as receivers.
		pubsub.WithPeerFilter(func(p peer.ID, _ string) bool { return h.auth.ok(p) }),
	)
	if err != nil {
		h.h.Close()
		return nil, fmt.Errorf("pubsub: %w", err)
	}
	return h, nil
}

// ID is the host's peer ID.
func (h *Host) ID() peer.ID { return h.h.ID() }

// Close stops the host.
func (h *Host) Close() error { return h.h.Close() }

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

// Port is the port the host listens on, 0 if it cannot tell.
func (h *Host) Port() int {
	for _, a := range h.h.Addrs() {
		for _, proto := range []int{ma.P_UDP, ma.P_TCP} {
			if v, err := a.ValueForProtocol(proto); err == nil {
				if n, err := strconv.Atoi(v); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

// HandleStreams serves the sanbo protocol's streams with fn, from hosts
// that have shown they know the key; another's are reset.
func (h *Host) HandleStreams(fn func(s network.Stream)) {
	h.h.SetStreamHandler(Protocol, func(s network.Stream) {
		if !h.auth.passes(s.Conn().RemotePeer()) {
			_ = s.Reset()
			return
		}
		fn(s)
	})
}

// Open opens a sanbo protocol stream to the peer id, reached at addrs if
// it is not connected already. It may go through a relay.
func (h *Host) Open(ctx context.Context, id string, addrs []string) (network.Stream, error) {
	info, err := addrInfo(id, addrs)
	if err != nil {
		return nil, err
	}
	ctx = network.WithAllowLimitedConn(ctx, "sanbo")
	if err := h.link(ctx, info); err != nil {
		return nil, err
	}
	return h.h.NewStream(ctx, info.ID, Protocol)
}

// Ensure links to the peer id, at addrs if it is not connected already,
// and waits for both sides to show they know the key.
func (h *Host) Ensure(ctx context.Context, id string, addrs []string) error {
	info, err := addrInfo(id, addrs)
	if err != nil {
		return err
	}
	return h.link(ctx, info)
}

// EnsureAddr is Ensure for a multiaddr ending in the peer's /p2p/ ID.
func (h *Host) EnsureAddr(ctx context.Context, addr string) error {
	info, err := peer.AddrInfoFromString(addr)
	if err != nil {
		return err
	}
	return h.link(ctx, *info)
}

// link connects to info, which libp2p does at once when it is connected
// already, and waits for both sides to show they know the key.
func (h *Host) link(ctx context.Context, info peer.AddrInfo) error {
	if info.ID == h.h.ID() {
		return errors.New("p2p: a link to itself")
	}
	if err := h.h.Connect(network.WithAllowLimitedConn(ctx, "sanbo"), info); err != nil {
		return err
	}
	h.auth.begin(info.ID)
	return h.auth.wait(ctx, info.ID)
}

// addrInfo is the peer id at addrs, those of them that parse.
func addrInfo(id string, addrs []string) (peer.AddrInfo, error) {
	pid, err := peer.Decode(id)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	info := peer.AddrInfo{ID: pid}
	for _, a := range addrs {
		if m, err := ma.NewMultiaddr(a); err == nil {
			info.Addrs = append(info.Addrs, m)
		}
	}
	return info, nil
}

// Topic joins the mesh topic name. Messages are taken only as relayed by a
// host that showed it knows the key, and valid when decode makes something
// of them, which a subscriber finds in the message's ValidatorData.
func (h *Host) Topic(name string, decode func(from peer.ID, data []byte) (any, bool)) (*pubsub.Topic, *pubsub.Subscription, error) {
	err := h.ps.RegisterTopicValidator(name, func(_ context.Context, from peer.ID, msg *pubsub.Message) bool {
		if from != h.h.ID() && !h.auth.ok(from) {
			return false
		}
		v, ok := decode(msg.GetFrom(), msg.GetData())
		msg.ValidatorData = v
		return ok
	})
	if err != nil {
		return nil, nil, err
	}
	topic, err := h.ps.Join(name)
	if err != nil {
		return nil, nil, err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		topic.Close()
		return nil, nil, err
	}
	return topic, sub, nil
}

// AddrsAt are the addresses a host at ip listens on with port: QUIC and
// TCP.
func AddrsAt(ip string, port int) []string {
	family := "ip4"
	if strings.Contains(ip, ":") {
		family = "ip6"
	}
	return []string{
		fmt.Sprintf("/%s/%s/udp/%d/quic-v1", family, ip, port),
		fmt.Sprintf("/%s/%s/tcp/%d", family, ip, port),
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

// authTimeout bounds how long a host waits for another to show it knows
// the key.
const authTimeout = 10 * time.Second
