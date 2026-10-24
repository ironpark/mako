package daemon

import (
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestMergeKeepsGossipLastSeen(t *testing.T) {
	p := newPeerTable()
	stale := time.Now().Add(-2 * peerTTL)
	p.merge(protocol.Node{ID: "c", Addr: "10.0.0.3:4242", LastSeen: stale})

	got, ok := p.get("c")
	if !ok || !got.LastSeen.Equal(stale) || got.Source != "gossip" {
		t.Fatalf("merge new node = %+v, %v", got, ok)
	}

	// Relaying the same stale record again must not refresh it.
	p.merge(protocol.Node{ID: "c", LastSeen: stale})
	p.prune(peerTTL)
	if _, ok := p.get("c"); ok {
		t.Fatal("stale gossiped node survived prune")
	}
}

func TestMergeKeepsKnownAddrAndSource(t *testing.T) {
	p := newPeerTable()
	p.upsert(protocol.Node{ID: "b", Addr: "10.0.0.2:4242", Source: "static"})
	before, _ := p.get("b")

	time.Sleep(time.Millisecond)
	later := time.Now()
	p.merge(protocol.Node{ID: "b", Addr: "[::]:4242", Source: "self", LastSeen: later})

	got, _ := p.get("b")
	if got.Addr != "10.0.0.2:4242" || got.Source != "static" || !got.LastSeen.Equal(later) || !later.After(before.LastSeen) {
		t.Fatalf("merge known node = %+v", got)
	}
}

func TestUpsertKeepsStaticSource(t *testing.T) {
	p := newPeerTable()
	p.upsert(protocol.Node{ID: "b", Addr: "10.0.0.2:4242", Source: "static", OS: "linux"})
	p.upsert(protocol.Node{ID: "b", Addr: "10.0.0.2:4242", Source: "inbound"})

	got, _ := p.get("b")
	if got.Source != "static" || got.OS != "linux" {
		t.Fatalf("upsert over static = %+v", got)
	}
}
