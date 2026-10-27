package daemon

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMergeKeepsGossipLastSeen(t *testing.T) {
	p := newPeerTable()
	stale := time.Now().Add(-2 * peerTTL)
	p.merge(&protocol.Node{Id: "c", Addr: "10.0.0.3:4242", LastSeen: timestamppb.New(stale)})

	got, ok := p.get("c")
	if !ok || !got.GetLastSeen().AsTime().Equal(stale) || got.GetSource() != "gossip" {
		t.Fatalf("merge new node = %+v, %v", got, ok)
	}

	// Relaying the same stale record again must not refresh it.
	p.merge(&protocol.Node{Id: "c", LastSeen: timestamppb.New(stale)})
	p.prune(peerTTL)
	if _, ok := p.get("c"); ok {
		t.Fatal("stale gossiped node survived prune")
	}
}

func TestMergeKeepsKnownAddrAndSource(t *testing.T) {
	p := newPeerTable()
	p.upsert(&protocol.Node{Id: "b", Addr: "10.0.0.2:4242", Source: "static"})
	before, _ := p.get("b")

	time.Sleep(time.Millisecond)
	later := time.Now()
	p.merge(&protocol.Node{Id: "b", Addr: "[::]:4242", Source: "self", LastSeen: timestamppb.New(later)})

	got, _ := p.get("b")
	if got.GetAddr() != "10.0.0.2:4242" || got.GetSource() != "static" || !got.GetLastSeen().AsTime().Equal(later) || !later.After(before.GetLastSeen().AsTime()) {
		t.Fatalf("merge known node = %+v", got)
	}
}

func TestUpsertKeepsStaticSource(t *testing.T) {
	p := newPeerTable()
	p.upsert(&protocol.Node{Id: "b", Addr: "10.0.0.2:4242", Source: "static", Os: "linux"})
	p.upsert(&protocol.Node{Id: "b", Addr: "10.0.0.2:4242", Source: "inbound"})

	got, _ := p.get("b")
	if got.GetSource() != "static" || got.GetOs() != "linux" {
		t.Fatalf("upsert over static = %+v", got)
	}
}
