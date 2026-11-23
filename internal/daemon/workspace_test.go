package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/workspace"
	"github.com/ironpark/mako/pkg/crdt"
)

// mark asks for a creation or an add in a front-end's delta.
func mark() *protocol.HLC { return &protocol.HLC{Logical: 1} }

// newSpace is a delta creating a space.
func newSpace(id, name string) *protocol.WorkspaceState {
	return &protocol.WorkspaceState{Spaces: map[string]*protocol.SpaceEntry{
		id: {CreatedAt: mark(), Name: &protocol.StringReg{Value: name}},
	}}
}

// newProject is a delta creating a project of one root, at the folders
// given by their location keys.
func newProject(id string, locations ...string) *protocol.WorkspaceState {
	root := &protocol.ProjectRootEntry{CreatedAt: mark(), Locations: map[string]*protocol.ElementMeta{}}
	for _, l := range locations {
		root.Locations[l] = &protocol.ElementMeta{AddedAt: mark()}
	}
	return &protocol.WorkspaceState{Projects: map[string]*protocol.ProjectEntry{
		id: {CreatedAt: mark(), Name: &protocol.StringReg{Value: id}, Roots: map[string]*protocol.ProjectRootEntry{"r": root}},
	}}
}

// spaceNames are the names of the spaces a state shows, in order.
func spaceNames(st *protocol.WorkspaceState) []string {
	var out []string
	for _, sp := range workspace.Spaces(st) {
		out = append(out, sp.GetName().GetValue())
	}
	return out
}

// legacyDoc is a workspace as a window from before the CRDT writes one.
func legacyDoc(version int64, node string, spaces ...string) []byte {
	type space struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var list []space
	for _, s := range spaces {
		list = append(list, space{s, s})
	}
	body, _ := json.Marshal(map[string]any{"version": version, "node_id": node, "doc": map[string]any{"spaces": list}})
	return body
}

// A front-end's delta is stamped by the daemon's clock, whatever the
// front-end put in, merged, and read back by the next daemon to run in
// the folder.
func TestADeltaIsStampedAndKept(t *testing.T) {
	dir := t.TempDir()
	var w workspaces
	w.load(dir, "me")
	delta := newSpace("s", "Work")
	delta.Spaces["s"].Name.Clock = &protocol.HLC{WallTime: 1 << 60, NodeId: "window"}
	if !w.edit(delta) {
		t.Fatal("no change")
	}
	got := w.get(true)
	clock := got.GetState().GetSpaces()["s"].GetName().GetClock()
	if clock.GetNodeId() != "me" || clock.GetWallTime() != 0 || clock.GetLogical() == 0 {
		t.Errorf("stamped %v: not the unanchored daemon's", clock)
	}
	if w.edit(&protocol.WorkspaceState{}) {
		t.Error("an empty delta changed the state")
	}
	var again workspaces
	again.load(dir, "me")
	if !proto.Equal(again.get(true), got) {
		t.Fatalf("read back %v\nwant %v", again.get(true), got)
	}
	// It counts on from where it was.
	again.edit(newSpace("t", "Home"))
	if c := again.get(true).GetState().GetSpaces()["t"].GetName().GetClock(); c.GetLogical() <= clock.GetLogical() {
		t.Errorf("after a restart: %v, before %v", c, clock)
	}
}

// The workspace a window kept before it was a CRDT, which it gives the
// daemon once, is taken in whole: what only it had joins what the state
// had, and nothing of either is lost.
func TestAWindowsOldWorkspaceIsTakenIn(t *testing.T) {
	dir := t.TempDir()
	var w workspaces
	w.load(dir, "me")
	if !w.edit(newSpace("mine", "Mine")) {
		t.Fatal("no change")
	}
	if !w.takeLegacy(legacyDoc(time.Now().UnixNano(), "me", "a", "b")) {
		t.Fatal("the window's workspace was not taken in")
	}
	if got := slices.Sorted(slices.Values(spaceNames(w.get(true).GetState()))); !slices.Equal(got, []string{"Mine", "a", "b"}) {
		t.Fatalf("spaces %v", got)
	}
	if !w.clock.Anchored() {
		t.Error("a timed workspace left the clock counting")
	}
	if w.takeLegacy([]byte("not a workspace")) {
		t.Error("what is no workspace changed the state")
	}
	var again workspaces
	again.load(dir, "me")
	if !proto.Equal(again.get(true), w.get(true)) {
		t.Fatal("not read back as it was")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "workspace.pb"))
	var kept protocol.Workspace
	if proto.Unmarshal(data, &kept) != nil || len(kept.GetDoc()) > 0 {
		t.Fatal("a document is kept beside the state")
	}
}

// A workspace.pb kept before the CRDT is taken in from its document, as
// any node with the same document takes it in.
func TestAnOldWorkspaceFileIsTakenIn(t *testing.T) {
	dir := t.TempDir()
	doc := legacyDoc(time.Now().UnixNano(), "laptop", "a", "b")
	data, _ := proto.Marshal(&protocol.Workspace{Doc: doc, Version: 5, NodeId: "laptop"})
	if err := os.WriteFile(filepath.Join(dir, "workspace.pb"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var w workspaces
	w.load(dir, "desk")
	want, _ := workspace.FromLegacy(doc, physNow())
	got := w.get(true)
	if !proto.Equal(got.GetState(), want) || string(got.GetDigest()) != string(workspace.Digest(want)) {
		t.Fatalf("taken in as %v", got.GetState())
	}
	if !w.clock.Anchored() {
		t.Error("a timed workspace left the clock counting")
	}
}

// A peer's entries stamped too far ahead are left out, and do not drag
// this node's clock along; a peer from before the CRDT, sending a
// document, is taken in only into a state that holds nothing.
func TestAPeersWorkspaceIsMergedWithCare(t *testing.T) {
	var w workspaces
	w.load(t.TempDir(), "me")
	now := physNow()
	far := &protocol.HLC{WallTime: now + time.Hour.Milliseconds(), NodeId: "fast"}
	near := &protocol.HLC{WallTime: now, NodeId: "fast"}
	peer := &protocol.WorkspaceState{Spaces: map[string]*protocol.SpaceEntry{
		"near": {Id: "near", CreatedAt: near, Name: &protocol.StringReg{Value: "Near", Clock: near}},
		"far":  {Id: "far", CreatedAt: far},
	}}
	if !w.merge(&protocol.Workspace{State: peer}) {
		t.Fatal("nothing merged")
	}
	if got := spaceNames(w.get(true).GetState()); !slices.Equal(got, []string{"Near"}) {
		t.Errorf("spaces %v", got)
	}
	if next := w.clock.Now(physNow()); next.WallTime > physNow()+1000 {
		t.Errorf("the clock was dragged ahead: %v", next)
	}
	if !w.clock.Anchored() {
		t.Error("not anchored after a peer's state")
	}
	if w.merge(&protocol.Workspace{Doc: legacyDoc(time.Now().UnixNano(), "old", "x"), Version: 1}) {
		t.Error("an old peer's document was merged into a state")
	}

	var empty workspaces
	empty.load(t.TempDir(), "me")
	if !empty.merge(&protocol.Workspace{Doc: legacyDoc(time.Now().UnixNano(), "old", "x"), Version: 1}) {
		t.Fatal("an old peer's document was not taken into an empty state")
	}
	if got := spaceNames(empty.get(true).GetState()); !slices.Equal(got, []string{"x"}) {
		t.Errorf("spaces %v", got)
	}
}

// A node new to the mesh counts its edits until it joins, so the mesh's
// layout, under the same key, stays; what only it had is kept, and once
// joined, its edits are timed, and kept so after a restart.
func TestANewNodeGivesWay(t *testing.T) {
	dir := t.TempDir()
	var fresh workspaces
	fresh.load(dir, "fresh")
	fresh.edit(&protocol.WorkspaceState{Layouts: map[string]*protocol.LayoutEntry{
		"all|":   {Doc: []byte("one tab")},
		"fresh|": {Doc: []byte("mine")},
	}})
	mesh := &protocol.WorkspaceState{}
	old := crdt.NewClock("old", true)
	d := &protocol.WorkspaceState{Layouts: map[string]*protocol.LayoutEntry{"all|": {Doc: []byte("months of tabs")}}}
	workspace.Stamp(d, old, physNow()-time.Hour.Milliseconds())
	workspace.Merge(mesh, d, nil, physNow())
	fresh.merge(&protocol.Workspace{State: mesh})
	st := fresh.get(true).GetState()
	if got := string(workspace.Layout(st, "all|").GetDoc()); got != "months of tabs" {
		t.Errorf("all| is %q", got)
	}
	if got := string(workspace.Layout(st, "fresh|").GetDoc()); got != "mine" {
		t.Errorf("fresh| is %q", got)
	}
	var again workspaces
	again.load(dir, "fresh")
	if !again.clock.Anchored() {
		t.Fatal("not anchored after a restart")
	}
	again.edit(&protocol.WorkspaceState{Layouts: map[string]*protocol.LayoutEntry{"all|": {Doc: []byte("joined")}}})
	if got := string(workspace.Layout(again.get(true).GetState(), "all|").GetDoc()); got != "joined" {
		t.Errorf("all| after joining is %q", got)
	}
}

// A node that joined another while both only counted their stamps times
// its own from then on, after a restart too, though its state holds no
// timed stamp yet: an edit it makes later than another node's, of the
// same name, wins.
func TestAJoinedNodeStaysAnchoredWithOnlyCountedStamps(t *testing.T) {
	dir := t.TempDir()
	var a, b workspaces
	a.load(t.TempDir(), "a")
	b.load(dir, "b")
	a.edit(newSpace("s", "a's"))
	b.edit(newSpace("t", "b's"))
	a.merge(b.get(true))
	b.merge(a.get(true))
	if !a.clock.Anchored() || !b.clock.Anchored() {
		t.Fatal("not anchored once joined")
	}
	if workspace.Anchored(b.get(true).GetState(), "b") != true {
		t.Fatal("the joined state does not say it is anchored")
	}

	// a renames s; b, restarted, renames it after.
	a.edit(&protocol.WorkspaceState{Spaces: map[string]*protocol.SpaceEntry{"s": {Name: &protocol.StringReg{Value: "a's rename"}}}})
	var again workspaces
	again.load(dir, "b")
	if !again.clock.Anchored() {
		t.Fatal("not anchored after a restart")
	}
	time.Sleep(2 * time.Millisecond)
	again.edit(&protocol.WorkspaceState{Spaces: map[string]*protocol.SpaceEntry{"s": {Name: &protocol.StringReg{Value: "b's later rename"}}}})
	again.merge(a.get(true))
	if got := again.get(true).GetState().GetSpaces()["s"].GetName().GetValue(); got != "b's later rename" {
		t.Errorf("s is %q", got)
	}
}

// Two nodes that trust each other both ways edit apart, and each ends with
// both's spaces and projects, its own projects following; a node that
// lets one of them in, but is not let in, takes nothing.
func TestWorkspacesMergeAcrossNodesThatTrustEachOther(t *testing.T) {
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

	aKey, bKey := workspace.LocationKey(a.config().NodeID, "/src/pa"), workspace.LocationKey(b.config().NodeID, "/src/pa-b")
	for _, d := range []*protocol.WorkspaceState{newSpace("sa", "A's"), newProject("pa", aKey, bKey)} {
		if got, err := ca.SetWorkspace(ctx, &protocol.Workspace{State: d}); err != nil || got.GetState() == nil {
			t.Fatalf("a's edit: %v %v", got, err)
		}
	}
	for _, d := range []*protocol.WorkspaceState{newSpace("sb", "B's"), newProject("pb", workspace.LocationKey(b.config().NodeID, "/src/pb"))} {
		if _, err := cb.SetWorkspace(ctx, &protocol.Workspace{State: d}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*Daemon{a, b} {
		waitForWithin(t, 15*time.Second, d.config().Name+" holds both's spaces", func() bool {
			return len(workspace.Spaces(d.workspace.get(true).GetState())) == 2
		})
	}
	for heard := false; !heard; {
		select {
		case ch := <-changes:
			heard = ch.GetWorkspace()
		case <-ctx.Done():
			t.Fatal("b's window did not hear of the workspace")
		}
	}
	got, err := cb.Workspace(ctx)
	if err != nil || !proto.Equal(got.GetState(), a.workspace.get(true).GetState()) {
		t.Fatalf("b's workspace %v %v", got, err)
	}
	paths := func(d *Daemon) map[string][]string {
		out := map[string][]string{}
		for _, s := range d.projects.get() {
			out[s.GetId()] = s.GetPaths()
		}
		return out
	}
	waitFor(t, "a's projects", func() bool {
		p := paths(a)
		return len(p) == 1 && slices.Equal(p["pa"], []string{"/src/pa"})
	})
	waitFor(t, "b's projects", func() bool {
		p := paths(b)
		return len(p) == 2 && slices.Equal(p["pa"], []string{"/src/pa-b"}) && slices.Equal(p["pb"], []string{"/src/pb"})
	})

	waitFor(t, "a's state on the server", func() bool { _, ok := server.peers.get(a.config().NodeID); return ok })
	time.Sleep(2 * time.Second)
	if !workspace.Empty(server.workspace.get(true).GetState()) {
		t.Fatal("the server took up a's workspace")
	}
}

// A node that does not share its workspace sends none, and takes none of
// its peers'; its front-ends still edit it.
func TestAWorkspaceNotSharedStaysHome(t *testing.T) {
	a := meshDaemon(t, "a")
	off := false
	b := meshDaemonWith(t, "b", func(cfg *config.Config) { cfg.Workspace.Sync = &off }, p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)
	ca, cb := dialDaemon(t, a), dialDaemon(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := ca.SetWorkspace(ctx, &protocol.Workspace{State: newSpace("sa", "A's")}); err != nil {
		t.Fatal(err)
	}
	got, err := cb.SetWorkspace(ctx, &protocol.Workspace{State: newSpace("sb", "B's")})
	if err != nil || !slices.Equal(spaceNames(got.GetState()), []string{"B's"}) {
		t.Fatalf("b's own edit: %v %v", got, err)
	}
	if b.state().GetWorkspace() != nil {
		t.Fatal("b's state carries its workspace")
	}
	waitFor(t, "b's state on a", func() bool { _, ok := a.peers.get(b.config().NodeID); return ok })
	waitFor(t, "a's state on b", func() bool { _, ok := b.peers.get(a.config().NodeID); return ok })
	time.Sleep(2 * time.Second)
	if got := spaceNames(a.workspace.get(true).GetState()); !slices.Equal(got, []string{"A's"}) {
		t.Errorf("a holds %v", got)
	}
	if got := spaceNames(b.workspace.get(true).GetState()); !slices.Equal(got, []string{"B's"}) {
		t.Errorf("b holds %v", got)
	}
}

// Once the workspace holds projects, they are the node's: a front-end
// giving its own is answered with them.
func TestTheWorkspacesProjectsOutrankSetProjects(t *testing.T) {
	d := meshDaemon(t, "a")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	given := []*protocol.ProjectScope{{Id: "given", Paths: []string{"/src/given"}}}
	got, err := c.SetProjects(ctx, &protocol.SetProjects{Projects: given})
	if err != nil || len(got.GetProjects()) != 1 || got.GetProjects()[0].GetId() != "given" {
		t.Fatalf("before the workspace held projects: %v %v", got, err)
	}
	if _, err := c.SetWorkspace(ctx, &protocol.Workspace{State: newProject("p", workspace.LocationKey(d.config().NodeID, "/src/p"))}); err != nil {
		t.Fatal(err)
	}
	if list := d.projects.get(); len(list) != 1 || list[0].GetId() != "p" {
		t.Fatalf("projects after the workspace's %v", list)
	}
	got, err = c.SetProjects(ctx, &protocol.SetProjects{Projects: given})
	if err != nil || len(got.GetProjects()) != 1 || got.GetProjects()[0].GetId() != "p" {
		t.Fatalf("SetProjects after: %v %v", got, err)
	}
}

// A window reads the workspace of a node that lets its own in, to copy
// its spaces; one that does not let it in shows it none.
func TestAWorkspaceIsReadFromANodeThatLetsThisOneIn(t *testing.T) {
	a := meshDaemon(t, "a")
	server := meshDaemon(t, "server", p2pAddr(a))
	other := meshDaemon(t, "other", p2pAddr(a))
	letIn(t, server, a)
	letIn(t, a, other)
	server.workspace.edit(newSpace("s", "Server's"))
	other.workspace.edit(newSpace("x", "Secret"))
	ca := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "the server among a's peers", func() bool { _, ok := a.peers.get(server.config().NodeID); return ok })
	got, err := ca.WorkspaceOf(ctx, server.config().NodeID)
	if err != nil || !slices.Equal(spaceNames(got.GetState()), []string{"Server's"}) {
		t.Fatalf("the server's workspace: %v %v", got, err)
	}
	if got, err := ca.WorkspaceOf(ctx, other.config().NodeID); err == nil && !workspace.Empty(got.GetState()) {
		t.Fatalf("read the workspace of a node that does not let a in: %v", got.GetState())
	}
}

// A peer that sent only its workspace's digest, one that differs, is
// asked for its workspace, which is merged in.
func TestAWorkspaceThatDiffersIsAskedFor(t *testing.T) {
	b := meshDaemon(t, "b")
	server := meshDaemon(t, "server", p2pAddr(b))
	// The server lets b in, so it answers b, but b does not let it in, so
	// it never sends b its workspace of its own accord.
	letIn(t, server, b)
	server.workspace.edit(newSpace("s", "Server's"))
	waitFor(t, "the server among b's peers", func() bool { _, ok := b.peers.get(server.config().NodeID); return ok })
	time.Sleep(time.Second)
	if !workspace.Empty(b.workspace.get(true).GetState()) {
		t.Fatal("b took the server's workspace unasked")
	}
	b.takeWorkspace(server.config().NodeID, &protocol.Workspace{Digest: server.workspace.currentDigest()})
	waitFor(t, "the server's workspace on b", func() bool {
		return slices.Equal(spaceNames(b.workspace.get(true).GetState()), []string{"Server's"})
	})
}

// A node that does not share its workspace still shows it to a node it
// lets in that asks, to copy from, but without the digest a peer merging
// asks for: the asker copies, and merges nothing.
func TestAWorkspaceNotSharedIsStillShown(t *testing.T) {
	a := meshDaemon(t, "a")
	off := false
	b := meshDaemonWith(t, "b", func(cfg *config.Config) { cfg.Workspace.Sync = &off }, p2pAddr(a))
	letIn(t, b, a)
	b.workspace.edit(newSpace("s", "B's"))
	ca := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "b among a's peers", func() bool { _, ok := a.peers.get(b.config().NodeID); return ok })
	got, err := ca.WorkspaceOf(ctx, b.config().NodeID)
	if err != nil || !slices.Equal(spaceNames(got.GetState()), []string{"B's"}) || len(got.GetDigest()) > 0 {
		t.Fatalf("b's workspace %v %v", got, err)
	}
	a.takeWorkspace(b.config().NodeID, &protocol.Workspace{Digest: []byte("another")})
	time.Sleep(time.Second)
	if !workspace.Empty(a.workspace.get(true).GetState()) {
		t.Fatal("a merged the workspace of a node not sharing it")
	}
}

// A node that lets a phone in takes the phone's edits of its workspace,
// given through the phone's daemon by to_node, as its own front-ends':
// stamped by its own clock, merged and kept. The phone, which lets no one
// in, keeps none of it itself.
func TestANodeLetInEditsTheWorkspace(t *testing.T) {
	phone := meshDaemon(t, "phone")
	server := meshDaemon(t, "server", p2pAddr(phone))
	letIn(t, server, phone)
	server.workspace.edit(newSpace("s", "Server's"))
	c := dialDaemon(t, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "the server among the phone's peers", func() bool { _, ok := phone.peers.get(server.config().NodeID); return ok })
	delta := newSpace("p", "Phone's")
	delta.Spaces["p"].Name.Clock = &protocol.HLC{WallTime: 1 << 60, NodeId: "phone's window"}
	got, err := c.SetWorkspace(ctx, &protocol.Workspace{State: delta, ToNode: server.config().NodeID})
	if err != nil || !slices.Equal(spaceNames(got.GetState()), []string{"Phone's", "Server's"}) {
		t.Fatalf("the answer: %v %v", got.GetState(), err)
	}
	if !proto.Equal(got.GetState(), server.workspace.get(false).GetState()) {
		t.Fatal("the answer is not the state the server holds")
	}
	if clock := got.GetState().GetSpaces()["p"].GetName().GetClock(); clock.GetNodeId() != server.config().NodeID || clock.GetWallTime() == 1<<60 {
		t.Errorf("stamped %v: not by the server's clock", clock)
	}
	var kept workspaces
	kept.load(server.workspace.dir, server.config().NodeID)
	if !slices.Equal(spaceNames(kept.get(false).GetState()), []string{"Phone's", "Server's"}) {
		t.Errorf("kept on disk: %v", spaceNames(kept.get(false).GetState()))
	}
	if !workspace.Empty(phone.workspace.get(false).GetState()) {
		t.Errorf("the phone kept %v", phone.workspace.get(false).GetState())
	}
	// The phone learns of the server's changes from its record.
	waitFor(t, "the server's digest in the phone's record of it", func() bool {
		n, _ := phone.peers.get(server.config().NodeID)
		return len(n.GetWorkspaceDigest()) > 0 && bytes.Equal(n.GetWorkspaceDigest(), server.workspace.currentDigest())
	})
}

// A node takes no edit from a node it does not let in, and from a peer it
// lets in a delta only: the document of a window from before the CRDT is
// its own front-ends' to give.
func TestOnlyANodeLetInGivesADelta(t *testing.T) {
	phone := meshDaemon(t, "phone")
	server := meshDaemon(t, "server", p2pAddr(phone))
	stranger := meshDaemon(t, "stranger", p2pAddr(phone))
	letIn(t, server, phone)
	c := dialDaemon(t, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "both among the phone's peers", func() bool {
		_, a := phone.peers.get(server.config().NodeID)
		_, b := phone.peers.get(stranger.config().NodeID)
		return a && b
	})
	if _, err := c.SetWorkspace(ctx, &protocol.Workspace{State: newSpace("p", "Phone's"), ToNode: stranger.config().NodeID}); err == nil {
		t.Error("a node that does not let the phone in took its edit")
	}
	if !workspace.Empty(stranger.workspace.get(false).GetState()) {
		t.Errorf("the stranger holds %v", stranger.workspace.get(false).GetState())
	}
	if _, err := c.SetWorkspace(ctx, &protocol.Workspace{Doc: legacyDoc(5, "phone", "Old"), ToNode: server.config().NodeID}); err == nil {
		t.Error("the server took a peer's old document")
	}
	if _, err := c.SetWorkspace(ctx, &protocol.Workspace{State: newSpace("p", "Phone's"), Doc: legacyDoc(5, "phone", "Old"), ToNode: server.config().NodeID}); err == nil {
		t.Error("the server took a peer's old document with a delta")
	}
	if !workspace.Empty(server.workspace.get(false).GetState()) {
		t.Errorf("the server holds %v", server.workspace.get(false).GetState())
	}
}

// An edit goes down a chain of nodes each letting in the one before, as
// any request does.
func TestAnEditIsRoutedAlongAChain(t *testing.T) {
	phone := meshDaemon(t, "phone")
	server := meshDaemon(t, "server", p2pAddr(phone))
	box := meshDaemon(t, "box", p2pAddr(server))
	letIn(t, server, phone)
	letIn(t, box, server)
	c := dialDaemon(t, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitFor(t, "a route to the box", func() bool {
		n, ok := phone.peers.get(box.config().NodeID)
		return ok && phone.routing()[n.GetId()].via == server.config().NodeID
	})
	got, err := c.SetWorkspace(ctx, &protocol.Workspace{State: newSpace("p", "Phone's"), ToNode: box.config().NodeID})
	if err != nil || !slices.Equal(spaceNames(box.workspace.get(false).GetState()), []string{"Phone's"}) || !proto.Equal(got.GetState(), box.workspace.get(false).GetState()) {
		t.Fatalf("the box after: %v, answered %v %v", box.workspace.get(false).GetState(), got.GetState(), err)
	}
	if !workspace.Empty(server.workspace.get(false).GetState()) {
		t.Errorf("the server on the way kept %v", server.workspace.get(false).GetState())
	}
}
