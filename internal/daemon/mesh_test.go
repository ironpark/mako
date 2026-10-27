package daemon

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// meshDaemon runs a daemon on loopback with its own configuration folder,
// the mesh's key psk and the peers given, until the test ends.
func meshDaemon(t *testing.T, name, psk string, peers ...string) *Daemon {
	t.Helper()
	return runDaemon(t, name, psk, false, peers...)
}

// runDaemon is meshDaemon, without libp2p when legacy is set, as an older
// build is.
func runDaemon(t *testing.T, name, psk string, legacy bool, peers ...string) *Daemon {
	t.Helper()
	cfg := config.Default()
	cfg.P2P.Disabled = legacy
	cfg.Name = name
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	cfg.PSK = psk
	cfg.Peers = peers
	d := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.dir = t.TempDir()
	d.hookBinary = ""
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatal(err)
	}
	if d.p2p == nil && !legacy {
		t.Fatal("no libp2p host")
	}
	return d
}

// p2pAddr is where d is on libp2p, to give a peer.
func p2pAddr(d *Daemon) string { return d.p2p.Addrs()[0] + "/p2p/" + d.p2p.ID().String() }

// addSession runs a session on d that lasts the test.
func addSession(t *testing.T, d *Daemon, id string) {
	t.Helper()
	s, err := session.Start(session.Spec{
		ID: id, NodeID: d.cfg.NodeID, Agent: "shell", Project: t.TempDir(),
		Command: []string{"sh", "-c", "sleep 30"}, Cols: 40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d.sessions.add(s)
}

func dialDaemon(t *testing.T, d *Daemon) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, d.Addr().String(), d.cfg.PSK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Two daemons that know the key link over libp2p: each learns the other
// from its state, lists its sessions from what it published, forwards
// requests to it there, and tells a watching front-end at once of a
// session started on the other.
func TestDaemonsShareTheirStateOverLibp2p(t *testing.T) {
	a := meshDaemon(t, "a", "key")
	b := meshDaemon(t, "b", "key", p2pAddr(a))
	addSession(t, b, "on-b")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waitFor(t, "b's session on a", func() bool {
		_, published := a.remote.get(b.cfg.NodeID)
		list, err := c.Sessions(ctx, "key")
		return err == nil && published && slices.ContainsFunc(list, func(s *protocol.SessionInfo) bool { return s.GetId() == "on-b" })
	})
	node, ok := a.peers.get(b.cfg.NodeID)
	if !ok || node.GetPeerId() != b.p2p.ID().String() || node.GetName() != "b" {
		t.Fatalf("a's record of b: %v", node)
	}
	dir := t.TempDir()
	if _, err := c.ListDir(ctx, "key", &protocol.ListDir{NodeId: b.cfg.NodeID, Path: dir}); err != nil {
		t.Fatalf("a request forwarded to b: %v", err)
	}

	changes := make(chan *protocol.Changed, 16)
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() { _ = c.Watch(watchCtx, "key", func(ch *protocol.Changed) { changes <- ch }) }()
	select {
	case <-changes: // the first, at once
	case <-ctx.Done():
		t.Fatal("the watch did not start")
	}
	addSession(t, b, "later-on-b")
	for {
		select {
		case ch := <-changes:
			if ch.GetSessions() {
				if _, ok := a.remote.get(b.cfg.NodeID); ok {
					return
				}
			}
		case <-ctx.Done():
			t.Fatal("the watching front-end did not hear of b's new session")
		}
	}
}

// A daemon with another key is refused: the mesh does not take it in, and
// it hears nothing of the mesh's sessions.
func TestADaemonWithAnotherKeyStaysOut(t *testing.T) {
	a := meshDaemon(t, "a", "key")
	addSession(t, a, "on-a")
	stranger := meshDaemon(t, "stranger", "other", p2pAddr(a))
	time.Sleep(3 * time.Second) // a few rounds of linking and publishing
	if _, ok := a.peers.get(stranger.cfg.NodeID); ok {
		t.Fatal("a took in a daemon with another key")
	}
	if _, ok := stranger.peers.get(a.cfg.NodeID); ok {
		t.Fatal("the stranger learned of a")
	}
	if _, ok := stranger.remote.get(a.cfg.NodeID); ok {
		t.Fatal("the stranger heard a's sessions")
	}
}

// A peer without libp2p, as an older build, is still reached over QUIC:
// its sessions are asked for as before.
func TestAnOlderPeerIsReachedOverQUIC(t *testing.T) {
	old := runDaemon(t, "old", "key", true)
	addSession(t, old, "on-old")
	a := meshDaemon(t, "a", "key", old.Addr().String())
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "the old peer's session", func() bool {
		list, err := c.Sessions(ctx, "key")
		return err == nil && slices.ContainsFunc(list, func(s *protocol.SessionInfo) bool { return s.GetId() == "on-old" })
	})
	if node, _ := a.peers.get(old.cfg.NodeID); node.GetPeerId() != "" {
		t.Fatalf("the old peer has a libp2p identity: %v", node)
	}
}
