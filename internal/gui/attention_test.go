package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestStatusOutOfViewIsUnread(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs { p.Notify = false; return p })
	at := time.Now()
	shown := &protocol.SessionInfo{Id: "a", State: protocol.StateRunning, Status: protocol.AgentWorking, StatusAt: timestamppb.New(at)}
	hidden := &protocol.SessionInfo{Id: "b", State: protocol.StateRunning, Status: protocol.AgentWorking, StatusAt: timestamppb.New(at)}
	m.Sessions.Set([]*protocol.SessionInfo{shown, hidden})
	// show puts a tab in view without a daemon to attach it to.
	show := func(id string) {
		m.editLayout(func(root *layoutNode) int {
			g := root.groups()[0]
			if !slices.Contains(g.Tabs, id) {
				g.insert(id, len(g.Tabs))
			}
			g.Active = id
			return g.ID
		})
	}
	show("a")

	// The next listing, as a poll brings it: new messages.
	shown, hidden = proto.CloneOf(shown), proto.CloneOf(hidden)
	shown.Status, shown.StatusAt = protocol.AgentDone, timestamppb.New(at.Add(time.Second))
	hidden.Status, hidden.StatusAt = protocol.AgentDone, timestamppb.New(at.Add(time.Second))
	next := []*protocol.SessionInfo{shown, hidden}
	m.noteStatus(next)
	m.Sessions.Set(next)
	if u := ggui.Peek(m.Unread); u["a"] || !u["b"] {
		t.Fatalf("unread %v", u)
	}
	if m.dotOf(hidden) != dotDone || m.dotOf(shown) != dotRest {
		t.Fatalf("dots %v %v", m.dotOf(hidden), m.dotOf(shown))
	}
	// An approval shows amber, whether or not it was seen.
	shown.Approval = &protocol.Approval{Id: "x", Tool: "Bash"}
	if m.dotOf(shown) != dotWaiting {
		t.Fatal("approval not shown")
	}
	// Looking at it clears the mark.
	show("b")
	m.seen()
	if ggui.Peek(m.Unread)["b"] {
		t.Fatal("seen session still unread")
	}
	// A session gone takes its mark with it.
	m.Unread.Set(map[string]bool{"gone": true})
	m.noteStatus(next)
	if len(ggui.Peek(m.Unread)) != 0 {
		t.Fatalf("unread %v", ggui.Peek(m.Unread))
	}
}

func TestSidebarLabels(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "1", Agent: "claude", Project: "/w/api", Branch: "feat/login"},
		{Id: "2", Agent: "shell", Project: "/oss/api"},
		{Id: "3", Agent: "codex", Project: "/w/web"},
	}
	groups := groupSessions(sessions, groupProject, nil, nil)
	labels := projectLabels(groups, nil)
	var names []string
	for _, g := range groups {
		names = append(names, labels[g.Key])
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"", "oss/api", "w/api"}) {
		t.Fatalf("labels %q", names)
	}
	// The same folder on two hosts goes by its host.
	twin := groupSessions([]*protocol.SessionInfo{{Id: "1", NodeId: "a", Project: "/w/api"}, {Id: "2", NodeId: "b", Project: "/w/api"}}, groupProject, []*protocol.Node{{Id: "a", Name: "mac"}, {Id: "b", Name: "box"}}, nil)
	labels = projectLabels(twin, []*protocol.Node{{Id: "a", Name: "mac"}, {Id: "b", Name: "box"}})
	if labels[twin[0].Key] != "api · mac" && labels[twin[0].Key] != "api · box" || labels[twin[0].Key] == labels[twin[1].Key] {
		t.Fatalf("twin labels %v", labels)
	}
}

func TestRankFilesPrefersNamesAndRuns(t *testing.T) {
	paths := []string{"internal/gui/view.go", "internal/gui/viewer_test.go", "docs/overview.md", "cmd/mako/main.go", "vendor/x/very/long/path/view.go"}
	ranked := func(q string) []string {
		var out []string
		for _, f := range rankFiles(paths, q, 10) {
			out = append(out, f.Path)
		}
		return out
	}
	got := ranked("view")
	if len(got) != 4 || got[0] != "internal/gui/view.go" {
		t.Fatalf("ranked %q", got)
	}
	if got := ranked("makomain"); len(got) != 1 || got[0] != "cmd/mako/main.go" {
		t.Fatalf("ranked %q", got)
	}
	if got := ranked("zzz"); len(got) != 0 {
		t.Fatalf("ranked %q", got)
	}
}

func TestSidebarGroupsFoldAndWidthsStayInBounds(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	g := sessionGroup{Key: dirKey("n", "/w/api")}
	key := foldKey(groupProject, g)
	m.toggleFold(key)
	if !slices.Contains(ggui.Peek(m.Prefs).Collapsed, key) {
		t.Fatal("the group did not fold")
	}
	if foldKey(groupHost, g) == key {
		t.Fatal("a host's fold is a project's")
	}
	m.toggleFold(key)
	if len(ggui.Peek(m.Prefs).Collapsed) != 0 {
		t.Fatal("the group did not open")
	}
	p := prefs{SidebarWidth: 5000}.normalized()
	if p.SidebarWidth != maxSidebarWidth {
		t.Fatalf("sidebar width %v", p.SidebarWidth)
	}
}
