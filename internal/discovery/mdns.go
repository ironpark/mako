package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/grandcat/zeroconf"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
)

// MDNS advertises this daemon over Bonjour and browses for others.
type MDNS struct {
	// NodeID and Name identify the local daemon in the TXT record.
	NodeID string
	Name   string
	// PeerID and Port are where the daemon is on libp2p.
	PeerID string
	Port   int
	// Domain is the mDNS domain, normally "local.".
	Domain string
}

// Kind identifies the backend.
func (m *MDNS) Kind() string { return "mdns" }

// Advertise publishes the local daemon. The returned shutdown function
// withdraws the record.
func (m *MDNS) Advertise() (func(), error) {
	txt := []string{
		"id=" + m.NodeID,
		"name=" + m.Name,
		"version=" + config.Version,
		"peer=" + m.PeerID,
	}
	server, err := zeroconf.Register(m.instance(), config.ServiceType, m.domain(), m.Port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns register: %w", err)
	}
	return server.Shutdown, nil
}

// Run browses for peers until ctx is done.
func (m *MDNS) Run(ctx context.Context, out chan<- Found) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return fmt.Errorf("mdns resolver: %w", err)
	}
	entries := make(chan *zeroconf.ServiceEntry, 8)
	if err := resolver.Browse(ctx, config.ServiceType, m.domain(), entries); err != nil {
		return fmt.Errorf("mdns browse: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-entries:
			if !ok {
				return nil
			}
			found, ok := m.convert(entry)
			if !ok {
				continue
			}
			select {
			case out <- found:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (m *MDNS) convert(entry *zeroconf.ServiceEntry) (Found, bool) {
	if entry == nil || entry.Port == 0 {
		return Found{}, false
	}
	id, name := txtLookup(entry.Text, "id"), txtLookup(entry.Text, "name")
	if id == "" || id == m.NodeID {
		return Found{}, false // unidentified, or our own advertisement
	}
	host := firstAddr(entry)
	if host == "" {
		return Found{}, false
	}
	if name == "" {
		name = strings.TrimSuffix(entry.HostName, ".")
	}
	f := Found{
		NodeID: id,
		Name:   name,
		Addr:   net.JoinHostPort(host, fmt.Sprint(entry.Port)),
		Source: "mdns",
	}
	if peer := txtLookup(entry.Text, "peer"); peer != "" {
		f.PeerID = peer
		for _, ip := range append(entry.AddrIPv4, entry.AddrIPv6...) {
			f.Multiaddrs = append(f.Multiaddrs, p2p.AddrsAt(ip.String(), entry.Port)...)
		}
	}
	return f, true
}

func (m *MDNS) instance() string {
	if m.Name != "" {
		return fmt.Sprintf("%s-%s", m.Name, shortID(m.NodeID))
	}
	return "mako-" + shortID(m.NodeID)
}

func (m *MDNS) domain() string {
	if m.Domain == "" {
		return "local."
	}
	return m.Domain
}

func firstAddr(entry *zeroconf.ServiceEntry) string {
	for _, ip := range entry.AddrIPv4 {
		return ip.String()
	}
	for _, ip := range entry.AddrIPv6 {
		return ip.String()
	}
	return strings.TrimSuffix(entry.HostName, ".")
}

func txtLookup(txt []string, key string) string {
	prefix := key + "="
	for _, kv := range txt {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
