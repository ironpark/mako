package p2p

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
)

func newHost(t *testing.T, psk string, mod ...func(*Config)) *Host {
	t.Helper()
	cfg := Config{Dir: t.TempDir(), Host: "127.0.0.1", PSK: psk, NoNAT: true}
	for _, m := range mod {
		m(&cfg)
	}
	h, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// echo serves the mako protocol on h by sending back what it reads.
func echo(h *Host) {
	h.HandleStreams(func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})
}

// roundTrip opens a stream from a to b at addrs and checks it echoes.
func roundTrip(ctx context.Context, a, b *Host, addrs []string) error {
	s, err := a.Open(ctx, b.ID().String(), addrs)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.Write([]byte("ping")); err != nil {
		return err
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(s, got); err != nil {
		return err
	}
	if string(got) != "ping" {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// Hosts that know the same key reach each other's mako streams; one that
// does not is hung up on, and its streams go nowhere either way.
func TestHostsWithTheKeyAloneTalk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, b, stranger := newHost(t, "key"), newHost(t, "key"), newHost(t, "other")
	echo(a)
	echo(b)
	if err := roundTrip(ctx, a, b, b.Addrs()); err != nil {
		t.Fatalf("a to b: %v", err)
	}
	if !a.auth.ok(b.ID()) || !b.auth.ok(a.ID()) {
		t.Fatal("the two did not take each other in")
	}
	if err := roundTrip(ctx, stranger, b, b.Addrs()); err == nil {
		t.Fatal("a host with another key reached b")
	}
	if b.auth.ok(stranger.ID()) {
		t.Fatal("b took in a host with another key")
	}
	// Dialed the other way, b's side refuses the stranger as well.
	stranger.h.Peerstore().AddAddrs(b.ID(), b.h.Addrs(), time.Minute)
	if err := roundTrip(ctx, b, stranger, stranger.Addrs()); err == nil {
		t.Fatal("b reached a host with another key")
	}
}

// A host the others cannot dial is reached through a relay of the mesh;
// the relay keeps no slot for a host without the key.
func TestHostsMeetThroughAMeshRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	relayHost := newHost(t, "key", func(c *Config) { c.RelayService = true })
	a, b := newHost(t, "key"), newHost(t, "key")
	echo(b)
	info := peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.h.Addrs()}
	if err := b.h.Connect(ctx, info); err != nil {
		t.Fatal(err)
	}
	b.auth.begin(relayHost.ID())
	if _, err := client.Reserve(ctx, b.h, info); err != nil {
		t.Fatalf("b's reservation: %v", err)
	}
	circuit := relayHost.h.Addrs()[0].String() + "/p2p/" + relayHost.ID().String() + "/p2p-circuit"
	if err := roundTrip(ctx, a, b, []string{circuit}); err != nil {
		t.Fatalf("a to b through the relay: %v", err)
	}

	stranger := newHost(t, "other")
	if err := stranger.h.Connect(ctx, info); err == nil {
		stranger.auth.begin(relayHost.ID())
		if _, err := client.Reserve(ctx, stranger.h, info); err == nil {
			t.Fatal("the relay kept a slot for a host with another key")
		}
	}
}

// The mesh's topics carry messages among hosts that know the key.
func TestTopicsCarryTheMeshsMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, b := newHost(t, "key"), newHost(t, "key")
	accept := func(peer.ID, []byte) (any, bool) { return nil, true }
	ta, _, err := a.Topic("t", accept)
	if err != nil {
		t.Fatal(err)
	}
	_, sb, err := b.Topic("t", accept)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAddr(ctx, b.Addrs()[0]+"/p2p/"+b.ID().String()); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		for {
			msg, err := sb.Next(ctx)
			if err != nil {
				return
			}
			if msg.GetFrom() == a.ID() {
				got <- string(msg.GetData())
				return
			}
		}
	}()
	// The topic's mesh forms over a heartbeat or two.
	for {
		_ = ta.Publish(ctx, []byte("hello"))
		select {
		case m := <-got:
			if m != "hello" {
				t.Fatalf("got %q", m)
			}
			return
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("no message came through")
		}
	}
}
