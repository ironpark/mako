package daemon

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// meshDaemon runs a daemon on loopback with its own configuration folder
// and the peers given, until the test ends.
func meshDaemon(t *testing.T, name string, peers ...string) *Daemon {
	t.Helper()
	cfg := config.Default()
	cfg.Name = name
	cfg.P2P.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
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
	if d.p2p == nil {
		t.Fatal("no libp2p host")
	}
	return d
}

// letIn has a let b in, by b's own key.
func letIn(t *testing.T, a, b *Daemon) {
	t.Helper()
	if err := a.p2p.Keys().Add(b.p2p.Key(), b.cfg.Name); err != nil {
		t.Fatal(err)
	}
	a.p2p.Reconsider()
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
	c, err := client.Dial(ctx, d.Socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// lists reports whether the front-end c lists session id.
func lists(ctx context.Context, c *client.Client, id string) bool {
	list, err := c.Sessions(ctx)
	return err == nil && slices.ContainsFunc(list, func(s *protocol.SessionInfo) bool { return s.GetId() == id })
}

// nodeOf is c's daemon's record of the node id.
func nodeOf(ctx context.Context, t *testing.T, c *client.Client, id string) *protocol.Node {
	t.Helper()
	nodes, err := c.Nodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.GetId() == id {
			return n
		}
	}
	return nil
}

// Two daemons that let each other in share their state: each lists the
// other's sessions from what it sent, forwards requests to it, and tells
// a watching front-end at once of a session started on the other.
func TestDaemonsThatLetEachOtherInShareTheirState(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)
	addSession(t, b, "on-b")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waitFor(t, "b's session on a", func() bool {
		_, sent := a.remote.fresh()[b.cfg.NodeID]
		return sent && lists(ctx, c, "on-b")
	})
	node := nodeOf(ctx, t, c, b.cfg.NodeID)
	if node.GetPeerId() != b.p2p.ID().String() || node.GetName() != "b" || !node.GetTrusted() || !node.GetTrustsUs() || node.GetKey() == "" {
		t.Fatalf("a's record of b: %v", node)
	}
	if _, err := c.ListDir(ctx, &protocol.ListDir{NodeId: b.cfg.NodeID, Path: t.TempDir()}); err != nil {
		t.Fatalf("a request forwarded to b: %v", err)
	}

	changes := make(chan *protocol.Changed, 16)
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() { _ = c.Watch(watchCtx, func(ch *protocol.Changed) { changes <- ch }) }()
	select {
	case <-changes: // the first, at once
	case <-ctx.Done():
		t.Fatal("the watch did not start")
	}
	addSession(t, b, "later-on-b")
	for {
		select {
		case ch := <-changes:
			if ch.GetSessions() && lists(ctx, c, "later-on-b") {
				return
			}
		case <-ctx.Done():
			t.Fatal("the watching front-end did not hear of b's new session")
		}
	}
}

// Trust runs one way: a server that lets a laptop in is driven from the
// laptop, and the server's front-end sees nothing of the laptop's.
func TestTrustRunsOneWayAcrossTheMesh(t *testing.T) {
	server := meshDaemon(t, "server")
	laptop := meshDaemon(t, "laptop", p2pAddr(server))
	letIn(t, server, laptop)
	addSession(t, server, "on-server")
	addSession(t, laptop, "on-laptop")
	fromLaptop, fromServer := dialDaemon(t, laptop), dialDaemon(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waitFor(t, "the server's session on the laptop", func() bool { return lists(ctx, fromLaptop, "on-server") })
	if _, err := fromLaptop.ListDir(ctx, &protocol.ListDir{NodeId: server.cfg.NodeID, Path: t.TempDir()}); err != nil {
		t.Fatalf("the laptop asking the server: %v", err)
	}
	if lists(ctx, fromServer, "on-laptop") {
		t.Fatal("the server lists the laptop's session")
	}
	_, err := fromServer.ListDir(ctx, &protocol.ListDir{NodeId: laptop.cfg.NodeID, Path: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "has not let this node in") {
		t.Fatalf("the server asking the laptop: %v", err)
	}
	if n := nodeOf(ctx, t, fromServer, laptop.cfg.NodeID); n == nil || !n.GetTrusted() || n.GetTrustsUs() {
		t.Fatalf("the server's record of the laptop: %v", n)
	}
}

// Trust carries along a chain: a laptop let in by a server, which a build
// box lets in, which a runner lets in, drives the runner through the
// others, sees its sessions, and is cut off when a link of the chain
// lets go. Nothing runs back up the chain.
func TestTrustCarriesAlongAChain(t *testing.T) {
	laptop := meshDaemon(t, "laptop")
	server := meshDaemon(t, "server", p2pAddr(laptop))
	box := meshDaemon(t, "box", p2pAddr(server))
	runner := meshDaemon(t, "runner", p2pAddr(box))
	letIn(t, server, laptop)
	letIn(t, box, server)
	letIn(t, runner, box)
	addSession(t, runner, "on-runner")
	addSession(t, laptop, "on-laptop")
	fromLaptop, fromRunner := dialDaemon(t, laptop), dialDaemon(t, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	waitFor(t, "the runner's session on the laptop", func() bool { return lists(ctx, fromLaptop, "on-runner") })
	n := nodeOf(ctx, t, fromLaptop, runner.cfg.NodeID)
	if n.GetHops() != 3 || n.GetVia() != server.cfg.NodeID || n.Standing() != protocol.StandingVia {
		t.Fatalf("the laptop's record of the runner: hops %d via %q, %s", n.GetHops(), n.GetVia(), n.Standing())
	}
	if _, err := fromLaptop.ListDir(ctx, &protocol.ListDir{NodeId: runner.cfg.NodeID, Path: t.TempDir()}); err != nil {
		t.Fatalf("the laptop asking the runner: %v", err)
	}
	// A file large enough to go compressed comes whole down the chain.
	text := strings.Repeat("a line of the runner's source\n", 1<<15)
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err := fromLaptop.ReadFile(ctx, &protocol.ReadFile{NodeId: runner.cfg.NodeID, Path: path}); err != nil || string(f.GetData()) != text {
		t.Fatalf("the laptop reading the runner's file: %d bytes, %v", len(f.GetData()), err)
	}
	info, err := fromLaptop.Rename(ctx, &protocol.RenameSession{NodeId: runner.cfg.NodeID, SessionId: "on-runner", Name: "renamed"})
	if err != nil || info.GetName() != "renamed" {
		t.Fatalf("the laptop renaming the runner's session: %v %v", info, err)
	}

	if lists(ctx, fromRunner, "on-laptop") {
		t.Fatal("the runner lists the laptop's session")
	}
	if _, err := fromRunner.ListDir(ctx, &protocol.ListDir{NodeId: laptop.cfg.NodeID, Path: t.TempDir()}); err == nil {
		t.Fatal("the runner reached the laptop")
	}
	// What a node runs on is told to every node it links to, let in or not.
	if n := nodeOf(ctx, t, fromRunner, laptop.cfg.NodeID); n.GetOs() != runtime.GOOS || n.GetArch() != runtime.GOARCH {
		t.Fatalf("the runner's record of the laptop: %s/%s", n.GetOs(), n.GetArch())
	}

	// The box letting the server go cuts the laptop off.
	if _, err := box.p2p.Keys().Remove(ssh.FingerprintSHA256(server.p2p.Key())); err != nil {
		t.Fatal(err)
	}
	box.p2p.Reconsider()
	waitFor(t, "the laptop to be cut off from the runner", func() bool {
		_, err := fromLaptop.ListDir(ctx, &protocol.ListDir{NodeId: runner.cfg.NodeID, Path: t.TempDir()})
		return err != nil
	})
}

// A route that goes stale in a ring of nodes that let each other in
// ends: a request for the node it led to fails, rather than going round,
// and the ring forgets the route.
func TestAStaleRouteEnds(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	d := meshDaemon(t, "d", p2pAddr(a), p2pAddr(b))
	c := meshDaemon(t, "c", p2pAddr(b))
	for _, pair := range [][2]*Daemon{{a, b}, {b, d}, {d, a}} {
		letIn(t, pair[0], pair[1])
		letIn(t, pair[1], pair[0])
	}
	letIn(t, c, b)
	fromA := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "a to reach c", func() bool {
		n := nodeOf(ctx, t, fromA, c.cfg.NodeID)
		return n.GetHops() == 2 && n.GetVia() == b.cfg.NodeID
	})

	if _, err := c.p2p.Keys().Remove(ssh.FingerprintSHA256(b.p2p.Key())); err != nil {
		t.Fatal(err)
	}
	c.p2p.Reconsider()
	waitFor(t, "b to lose c", func() bool { return b.routing()[c.cfg.NodeID].hops == 0 })
	if _, err := fromA.ListDir(ctx, &protocol.ListDir{NodeId: c.cfg.NodeID, Path: t.TempDir()}); err == nil {
		t.Fatal("a reached c through the ring")
	}
	waitForWithin(t, stateFresh+maxHops*2*time.Second, "a to lose c", func() bool { return a.routing()[c.cfg.NodeID].hops == 0 })
}

// A node that is not let in asks to be; the other's front-end sees the
// request and allows it, after which the first drives the second.
func TestANodeAsksToBeLetIn(t *testing.T) {
	server := meshDaemon(t, "server")
	laptop := meshDaemon(t, "laptop", p2pAddr(server))
	addSession(t, server, "on-server")
	fromLaptop, fromServer := dialDaemon(t, laptop), dialDaemon(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waitFor(t, "the laptop to know the server", func() bool { return nodeOf(ctx, t, fromLaptop, server.cfg.NodeID) != nil })
	if lists(ctx, fromLaptop, "on-server") {
		t.Fatal("the laptop lists a session of a server that has not let it in")
	}
	if _, err := fromLaptop.Access(ctx, &protocol.Access{Op: "request", NodeId: server.cfg.NodeID, Note: "my laptop"}); err != nil {
		t.Fatal(err)
	}
	info, err := fromServer.Access(ctx, &protocol.Access{})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.GetRequests()) != 1 || info.GetRequests()[0].GetNode().GetId() != laptop.cfg.NodeID || info.GetRequests()[0].GetNote() != "my laptop" {
		t.Fatalf("requests on the server: %v", info.GetRequests())
	}
	if got, want := info.GetRequests()[0].GetKey().GetFingerprint(), laptop.keyPrint; got != want {
		t.Fatalf("the request's key %s, want the laptop's %s", got, want)
	}
	info, err = fromServer.Access(ctx, &protocol.Access{Op: "allow", NodeId: laptop.cfg.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.GetRequests()) != 0 || len(info.GetKeys()) != 1 {
		t.Fatalf("after allowing: %v", info)
	}
	waitFor(t, "the server's session on the laptop", func() bool { return lists(ctx, fromLaptop, "on-server") })

	// Revoked, the laptop is out again.
	if _, err := fromServer.Access(ctx, &protocol.Access{Op: "revoke", Key: laptop.keyPrint}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the laptop to be let out", func() bool { return !lists(ctx, fromLaptop, "on-server") })
}

// Only this user reaches the daemon's socket; a daemon given another's
// host and port finds it there by a probe of its libp2p port; and a peer
// may not change who is let in.
func TestTheSocketIsTheUsersAndPeersAreFoundByProbe(t *testing.T) {
	d := meshDaemon(t, "d")
	info, err := os.Stat(d.Socket())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the socket: %v %v", info.Mode(), err)
	}

	other := meshDaemon(t, "other", d.p2p.HostPort())
	letIn(t, d, other)
	waitFor(t, "other to find d by its host and port", func() bool {
		p, ok := other.linkedPeer(&protocol.Node{PeerId: d.peerID()})
		return ok && p.TrustedBy
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := other.peerStream(ctx, &protocol.Node{Name: "d", PeerId: d.peerID(), Multiaddrs: d.multiaddrs()})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := transport.Call[*protocol.AccessInfo](stream, protocol.TypeAccess, &protocol.Access{Op: "add", Key: other.accessInfo().GetKey().GetLine()}); err == nil {
		t.Fatal("a peer changed who is let in")
	}
}

// An attach to another node's session reaches the front-end in plain
// frames, though the node sends its output compressed.
func TestAnAttachAcrossTheMeshComesPlain(t *testing.T) {
	laptop := meshDaemon(t, "laptop")
	server := meshDaemon(t, "server", p2pAddr(laptop))
	letIn(t, server, laptop)
	s, err := session.Start(session.Spec{
		ID: "loud", NodeID: server.cfg.NodeID, Agent: "shell", Project: t.TempDir(),
		Command: []string{"sh", "-c", "i=0; while [ $i -lt 3000 ]; do echo a line the session printed; i=$((i+1)); done; sleep 30"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	server.sessions.add(s)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fromLaptop := dialDaemon(t, laptop)
	waitFor(t, "the server's session on the laptop", func() bool { return lists(ctx, fromLaptop, "loud") })

	stream, _, err := transport.Dial(ctx, laptop.Socket(), &protocol.Hello{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.Send(protocol.TypeAttach, "", &protocol.Attach{SessionId: "loud", NodeId: server.cfg.NodeID, Replay: true}); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(20 * time.Second))
	var got int
	for got < 64<<10 {
		var hdr [4]byte
		if _, err := io.ReadFull(stream, hdr[:]); err != nil {
			t.Fatalf("after %d bytes of output: %v", got, err)
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n&(1<<31) != 0 {
			t.Fatal("a compressed frame reached the socket")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(stream, body); err != nil {
			t.Fatal(err)
		}
		env := &protocol.Envelope{}
		if err := proto.Unmarshal(body, env); err != nil {
			t.Fatal(err)
		}
		got += len(env.GetOutput().GetData())
	}
}
