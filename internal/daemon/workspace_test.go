package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// The later workspace wins, a tie going to the greater node, and what is
// kept is read back by the next daemon to run in the folder.
func TestTheLaterWorkspaceWins(t *testing.T) {
	dir := t.TempDir()
	var w workspaces
	w.load(dir)
	if w.put(&protocol.Workspace{}) {
		t.Fatal("an unversioned workspace was kept")
	}
	if !w.put(&protocol.Workspace{Doc: []byte("a"), Version: 2, NodeId: "a"}) {
		t.Fatal("the first was not kept")
	}
	if w.put(&protocol.Workspace{Doc: []byte("old"), Version: 1, NodeId: "z"}) || w.put(&protocol.Workspace{Doc: []byte("tie"), Version: 2, NodeId: "0"}) {
		t.Fatal("an older one, or a tie to a lesser node, was kept")
	}
	if !w.put(&protocol.Workspace{Doc: []byte("b"), Version: 2, NodeId: "b"}) {
		t.Fatal("a tie to a greater node was not kept")
	}
	var again workspaces
	again.load(dir)
	if string(again.get().GetDoc()) != "b" {
		t.Fatalf("read back %q", again.get().GetDoc())
	}
}

// A window's workspace goes to the nodes that trust its node both ways,
// whose watching windows hear of it; one that only lets it in does not
// take it up.
func TestWorkspacesFollowAcrossNodesThatTrustEachOther(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	server := meshDaemon(t, "server", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)
	letIn(t, server, a)
	ca, cb := dialDaemon(t, a), dialDaemon(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	changes := make(chan *protocol.Changed, 16)
	go func() { _ = cb.Watch(ctx, func(ch *protocol.Changed) { changes <- ch }) }()
	<-changes

	ws := &protocol.Workspace{Doc: []byte("tabs"), Version: time.Now().UnixNano(), NodeId: a.cfg.NodeID}
	got, err := ca.SetWorkspace(ctx, ws)
	if err != nil || string(got.GetDoc()) != "tabs" {
		t.Fatalf("set: %v %v", got, err)
	}
	for heard := false; !heard; {
		select {
		case ch := <-changes:
			heard = ch.GetWorkspace()
		case <-ctx.Done():
			t.Fatal("b's window did not hear of a's workspace")
		}
	}
	if got, err := cb.Workspace(ctx); err != nil || string(got.GetDoc()) != "tabs" {
		t.Fatalf("b holds %v %v", got, err)
	}
	// The server lets a in but a does not let it in: by now it has had
	// a's state, and keeps no workspace of a's.
	waitFor(t, "a's state on the server", func() bool { _, ok := server.peers.get(a.cfg.NodeID); return ok })
	time.Sleep(2 * time.Second)
	if server.workspace.get() != nil {
		t.Fatal("the server took up a's workspace")
	}
}
