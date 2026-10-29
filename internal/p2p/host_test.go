package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"golang.org/x/crypto/ssh"
)

func newHost(t *testing.T, mod ...func(*Config)) *Host {
	t.Helper()
	cfg := Config{Dir: t.TempDir(), Host: "127.0.0.1", NoNAT: true, Node: "n", Name: "n"}
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

// letIn has a let b in, by b's own key.
func letIn(t *testing.T, a, b *Host) {
	t.Helper()
	if err := a.Keys().Add(b.Key(), "b"); err != nil {
		t.Fatal(err)
	}
	a.Reconsider()
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

// waitFor polls cond until it holds or the test gives up.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Trust runs one way: a host that lets another in serves it, and is not
// served by it unless that one lets it in too.
func TestTrustRunsOneWay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server, laptop := newHost(t), newHost(t)
	echo(server)
	echo(laptop)
	letIn(t, server, laptop)

	if err := roundTrip(ctx, laptop, server, server.Addrs()); err != nil {
		t.Fatalf("laptop to server: %v", err)
	}
	if err := roundTrip(ctx, server, laptop, laptop.Addrs()); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("server to laptop: %v, want ErrNotTrusted", err)
	}
	p, ok := laptop.Peer(server.ID().String())
	if !ok || p.Trusts || !p.TrustedBy || p.Name != "n" {
		t.Fatalf("the laptop's view of the server: %+v %v", p, ok)
	}

	// Let in the other way too, which holds at once.
	letIn(t, laptop, server)
	waitFor(t, "the server to be let in", func() bool {
		p, _ := server.Peer(laptop.ID().String())
		return p.TrustedBy
	})
	if err := roundTrip(ctx, server, laptop, laptop.Addrs()); err != nil {
		t.Fatalf("server to laptop, let in: %v", err)
	}

	// A key taken out holds at once as well.
	if _, err := server.Keys().Remove(ssh.FingerprintSHA256(laptop.Key())); err != nil {
		t.Fatal(err)
	}
	server.Reconsider()
	waitFor(t, "the laptop to be let out", func() bool {
		p, _ := laptop.Peer(server.ID().String())
		return !p.TrustedBy
	})
	if err := roundTrip(ctx, laptop, server, server.Addrs()); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("laptop to server, let out: %v", err)
	}
}

// A host vouched for by an SSH key is let in where the key is, and by the
// hosts the same key vouches for.
func TestAnOwnersKeyVouchesForItsHosts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	owned := func(c *Config) { c.Owner = signer }
	a, b, stranger := newHost(t, owned), newHost(t, owned), newHost(t)
	echo(a)
	echo(b)
	if err := roundTrip(ctx, a, b, b.Addrs()); err != nil {
		t.Fatalf("a to b, one owner: %v", err)
	}
	if err := roundTrip(ctx, b, a, a.Addrs()); err != nil {
		t.Fatalf("b to a, one owner: %v", err)
	}
	if err := roundTrip(ctx, stranger, a, a.Addrs()); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("stranger to a: %v", err)
	}
	// The owner's key let in lets in every host it vouches for.
	echo(stranger)
	if err := stranger.Keys().Add(signer.PublicKey(), "owner"); err != nil {
		t.Fatal(err)
	}
	stranger.Reconsider()
	waitFor(t, "a to be let in by the owner's key", func() bool {
		p, _ := a.Peer(stranger.ID().String())
		return p.TrustedBy
	})
	p, _ := stranger.Peer(a.ID().String())
	if p.Owner == nil || ssh.FingerprintSHA256(p.Owner) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("a's owner as the stranger has it: %v", p.Owner)
	}
}

// A key that may log in by SSH lets in the hosts it vouches for, unless
// its line limits it.
func TestSSHAuthorizedKeysVouch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	line := ssh.MarshalAuthorizedKey(signer.PublicKey())
	file := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(file, append([]byte(`from="10.0.0.1" `), line...), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newHost(t, func(c *Config) { c.SSHAuthorizedKeys = file })
	laptop := newHost(t, func(c *Config) { c.Owner = signer })
	echo(server)
	if err := roundTrip(ctx, laptop, server, server.Addrs()); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("a key limited to an address: %v, want ErrNotTrusted", err)
	}
	if len(server.SSHKeys().List()) != 0 {
		t.Fatalf("a limited key listed: %v", server.SSHKeys().List())
	}

	// The file is read again when it changes.
	if err := os.WriteFile(file, line, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the laptop to be let in", func() bool {
		p, _ := laptop.Peer(server.ID().String())
		return p.TrustedBy
	})
	if err := roundTrip(ctx, laptop, server, server.Addrs()); err != nil {
		t.Fatalf("laptop to server: %v", err)
	}
	if !server.SSHKeys().Has(signer.PublicKey()) || server.Keys().Has(signer.PublicKey()) {
		t.Fatal("the SSH key went into mako's own file")
	}
	if ok, err := server.SSHKeys().Remove(ssh.FingerprintSHA256(signer.PublicKey())); !ok || !errors.Is(err, ErrReadOnly) {
		t.Fatalf("taking the SSH key out: %v %v, want ErrReadOnly", ok, err)
	}
}

// A host that is not let in asks to be, and is told of being let in.
func TestAHostAsksToBeLetIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, b := newHost(t), newHost(t)
	asked := make(chan Peer, 1)
	var note string
	a.HandleAccess(func(from Peer, n string) { note = n; asked <- from })
	if err := b.RequestAccess(ctx, a.ID().String(), a.Addrs(), "it's me"); err != nil {
		t.Fatal(err)
	}
	from := <-asked
	if from.ID != b.ID() || note != "it's me" || from.Trusts {
		t.Fatalf("asked by %+v, %q", from, note)
	}
	if err := a.Keys().Add(from.Key, from.Name); err != nil {
		t.Fatal(err)
	}
	a.Reconsider()
	waitFor(t, "b to hear it is let in", func() bool {
		p, _ := b.Peer(a.ID().String())
		return p.TrustedBy
	})
}

// States go to a peer either way trust runs, and not to one with none.
func TestStatesTravelWhereTrustRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, b, stranger := newHost(t), newHost(t), newHost(t)
	got := make(chan string, 4)
	a.HandleState(func(from Peer, data []byte) { got <- string(data) })
	letIn(t, b, a) // b lets a in: b's state is for a
	if err := b.Ensure(ctx, a.ID().String(), a.Addrs()); err != nil {
		t.Fatal(err)
	}
	if err := b.SendState(ctx, a.ID(), []byte("b's")); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "b's" {
		t.Fatalf("got %q", s)
	}
	if err := stranger.Ensure(ctx, a.ID().String(), a.Addrs()); err != nil {
		t.Fatal(err)
	}
	_ = stranger.SendState(ctx, a.ID(), []byte("stranger's"))
	select {
	case s := <-got:
		t.Fatalf("a took a stranger's state %q", s)
	case <-time.After(300 * time.Millisecond):
	}
}

// A host the others cannot dial is reached through a relay that lets
// both in; the relay keeps no slot for a host it does not.
func TestHostsMeetThroughARelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	relayHost := newHost(t, func(c *Config) { c.RelayService = true })
	a, b := newHost(t), newHost(t)
	letIn(t, relayHost, a)
	letIn(t, relayHost, b)
	letIn(t, b, a)
	echo(b)
	info := peer.AddrInfo{ID: relayHost.ID(), Addrs: relayHost.h.Addrs()}
	if err := b.h.Connect(ctx, info); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Reserve(ctx, b.h, info); err != nil {
		t.Fatalf("b's reservation: %v", err)
	}
	circuit := relayHost.h.Addrs()[0].String() + "/p2p/" + relayHost.ID().String() + "/p2p-circuit"
	if err := roundTrip(ctx, a, b, []string{circuit}); err != nil {
		t.Fatalf("a to b through the relay: %v", err)
	}

	stranger := newHost(t)
	if err := stranger.h.Connect(ctx, info); err == nil {
		if _, err := client.Reserve(ctx, stranger.h, info); err == nil {
			t.Fatal("the relay kept a slot for a host it does not let in")
		}
	}
}

// The authorized_keys file keeps lines it does not know of when a key is
// taken out, and leaves out keys bound to a command.
func TestAuthorizedKeysKeepTheirFile(t *testing.T) {
	dir := t.TempDir()
	k := NewKeys(dir)
	_, p1, _ := ed25519.GenerateKey(rand.Reader)
	_, p2, _ := ed25519.GenerateKey(rand.Reader)
	s1, _ := ssh.NewSignerFromKey(p1)
	s2, _ := ssh.NewSignerFromKey(p2)
	if err := k.Add(s1.PublicKey(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := k.Add(s2.PublicKey(), "two"); err != nil {
		t.Fatal(err)
	}
	if !k.Has(s1.PublicKey()) || len(k.List()) != 2 {
		t.Fatalf("keys: %v", k.List())
	}
	if ok, err := k.Remove(ssh.FingerprintSHA256(s1.PublicKey())); !ok || err != nil {
		t.Fatalf("remove: %v %v", ok, err)
	}
	if k.Has(s1.PublicKey()) || !k.Has(s2.PublicKey()) {
		t.Fatal("the wrong key went")
	}
	restricted := parseAuthorizedKeys([]byte(`command="ls" `+string(ssh.MarshalAuthorizedKey(s1.PublicKey()))), false)
	if len(restricted) != 0 {
		t.Fatal("a key bound to a command was let in")
	}
}

// A probe of a host's TCP port learns its peer ID.
func TestAProbeLearnsThePeerID(t *testing.T) {
	h := newHost(t)
	var tcp string
	for _, a := range h.h.Addrs() {
		if ip, err := a.ValueForProtocol(4); err == nil { // ip4
			if port, err := a.ValueForProtocol(6); err == nil { // tcp
				tcp = ip + ":" + port
			}
		}
	}
	id, err := Probe(context.Background(), tcp)
	if err != nil || id != h.ID() {
		t.Fatalf("probe of %s: %v %v, want %v", tcp, id, err, h.ID())
	}
}
