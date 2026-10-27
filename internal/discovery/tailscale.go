package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Tailscale discovers daemons across a tailnet. Peers are enumerated from
// MagicDNS names -- either the configured list, or whatever the local
// tailscale CLI reports -- and each one is probed on the mako QUIC port.
//
// This is the "ping search" path: a name is only reported as a node once a
// handshake against it succeeds.
type Tailscale struct {
	// NodeID is the local node, used to skip our own address.
	NodeID string
	// Hello is sent when probing a candidate.
	Hello *protocol.Hello
	// Hosts are MagicDNS names to probe. Empty means ask the tailscale CLI.
	Hosts []string
	// Port is the mako QUIC port on remote daemons.
	Port int
	// Interval is how often the tailnet is re-scanned.
	Interval time.Duration
	// Timeout bounds a single probe.
	Timeout time.Duration
	// Binary is the tailscale CLI to consult; defaults to "tailscale" on
	// the PATH, else the CLI inside the macOS app.
	Binary string
}

// Kind identifies the backend.
func (t *Tailscale) Kind() string { return "tailscale" }

// Run scans the tailnet until ctx is done.
func (t *Tailscale) Run(ctx context.Context, out chan<- Found) error {
	interval := t.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		t.scan(ctx, out)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (t *Tailscale) scan(ctx context.Context, out chan<- Found) {
	hosts := t.Hosts
	if len(hosts) == 0 {
		var err error
		hosts, err = t.tailnetHosts(ctx)
		if err != nil {
			return // tailscale not installed or not up; nothing to scan
		}
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	var wg sync.WaitGroup
	for _, host := range hosts {
		addr := net.JoinHostPort(host, fmt.Sprint(t.port()))
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			node, err := transport.Probe(ctx, addr, t.Hello, timeout)
			if err != nil || node.GetId() == "" || node.GetId() == t.NodeID {
				return
			}
			select {
			case out <- Found{NodeID: node.GetId(), Name: node.GetName(), Addr: addr, Source: "tailscale", PeerID: node.GetPeerId(), Multiaddrs: node.GetMultiaddrs()}:
			case <-ctx.Done():
			}
		}(addr)
	}
	wg.Wait()
}

// tailnetHosts asks the local tailscale CLI for peer MagicDNS names.
func (t *Tailscale) tailnetHosts(ctx context.Context) ([]string, error) {
	bin := t.Binary
	if bin == "" {
		bin = tailscaleCLI()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	body, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	var status struct {
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
		Peer           map[string]struct {
			DNSName string `json:"DNSName"`
			Online  bool   `json:"Online"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	hosts := make([]string, 0, len(status.Peer))
	for _, peer := range status.Peer {
		if !peer.Online || peer.DNSName == "" {
			continue
		}
		hosts = append(hosts, trimDot(peer.DNSName))
	}
	return hosts, nil
}

// macTailscale is the CLI the macOS app carries, which is not on the PATH
// unless the user linked it there.
const macTailscale = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// tailscaleCLI finds the tailscale CLI: on the PATH, else in the macOS app.
func tailscaleCLI() string {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path
	}
	if _, err := os.Stat(macTailscale); err == nil {
		return macTailscale
	}
	return "tailscale"
}

func (t *Tailscale) port() int {
	if t.Port == 0 {
		return 4242
	}
	return t.Port
}

func trimDot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
