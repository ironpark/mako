package p2p

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// A pairing code is how a node is joined at a glance: a link, shown as a
// QR code, that says where the node is and carries a token it made. A host
// that asks the node to let it in with the token is let in at once, as if
// someone had allowed it there; the token then serves no other.

// Pairing is what a pairing code says.
type Pairing struct {
	ID    string   // the node's peer ID
	Name  string   // its name, for people to read
	Token string   // what lets the host asking in
	Addrs []string // its multiaddrs, without /p2p/
}

// pairScheme and pairHost begin a pairing code's link.
const (
	pairScheme = "mako"
	pairHost   = "pair"
)

// maxPairAddrs bounds the addresses a pairing code carries, so its QR code
// stays easy to scan.
const maxPairAddrs = 6

// NewPairToken makes a token for a pairing code.
func NewPairToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SameToken reports whether two tokens are the same, taking as long
// whatever they hold.
func SameToken(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Link is the pairing code as a link: mako://pair?id=…&n=…&t=…&a=…, an a
// for each address, those another device most likely reaches first: IPv4
// before IPv6, and none only its link reaches, nor this machine unless
// it has no other, as in tests.
func (p Pairing) Link() string {
	var q []string
	add := func(k, v string) { q = append(q, k+"="+queryEscape(v)) }
	add("id", p.ID)
	add("n", p.Name)
	add("t", p.Token)
	var v4, v6, loopback []string
	for _, a := range p.Addrs {
		m, err := ma.NewMultiaddr(a)
		switch {
		case err != nil || manet.IsIP6LinkLocal(m):
		case manet.IsIPLoopback(m):
			loopback = append(loopback, a)
		case isIP4(m):
			v4 = append(v4, a)
		default:
			v6 = append(v6, a)
		}
	}
	addrs := append(v4, v6...)
	if len(addrs) == 0 {
		addrs = loopback
	}
	for i, a := range addrs {
		if i == maxPairAddrs {
			break
		}
		add("a", a)
	}
	return pairScheme + "://" + pairHost + "?" + strings.Join(q, "&")
}

// queryEscape escapes v for a link's query, leaving the slashes and colons
// of multiaddrs as they are, which a query may hold: the code is the
// smaller for it.
func queryEscape(v string) string {
	return strings.NewReplacer("%2F", "/", "%3A", ":").Replace(url.QueryEscape(v))
}

func isIP4(m ma.Multiaddr) bool {
	_, err := m.ValueForProtocol(ma.P_IP4)
	return err == nil
}

// ParsePairing reads a pairing code's link.
func ParsePairing(link string) (Pairing, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != pairScheme || u.Host != pairHost {
		return Pairing{}, errors.New("not a mako pairing code")
	}
	q := u.Query()
	p := Pairing{ID: q.Get("id"), Name: q.Get("n"), Token: q.Get("t"), Addrs: q["a"]}
	if _, err := peer.Decode(p.ID); err != nil {
		return Pairing{}, fmt.Errorf("the pairing code's node: %w", err)
	}
	if p.Token == "" {
		return Pairing{}, errors.New("the pairing code has no token")
	}
	if len(p.Addrs) == 0 {
		return Pairing{}, errors.New("the pairing code has no address")
	}
	return p, nil
}
