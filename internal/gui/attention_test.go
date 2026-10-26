package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestTitlesLoseTheirSpinner(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Fix the login": "Fix the login",
		"⠋ Thinking":      "Thinking",
		"◐ ◑ 제외된 항목":      "제외된 항목",
		"* Claude Code":   "Claude Code",
		"plain title":     "plain title",
		"v1.2 · release":  "v1.2 · release",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestStatusOutOfViewIsUnread(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs { p.Notify = false; return p })
	at := time.Now()
	shown := protocol.SessionInfo{ID: "a", State: protocol.StateRunning, Status: protocol.AgentWorking, StatusAt: at}
	hidden := protocol.SessionInfo{ID: "b", State: protocol.StateRunning, Status: protocol.AgentWorking, StatusAt: at}
	m.Sessions.Set([]protocol.SessionInfo{shown, hidden})
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

	shown.Status, shown.StatusAt = protocol.AgentDone, at.Add(time.Second)
	hidden.Status, hidden.StatusAt = protocol.AgentDone, at.Add(time.Second)
	next := []protocol.SessionInfo{shown, hidden}
	m.noteStatus(next)
	m.Sessions.Set(next)
	if u := ggui.Peek(m.Unread); u["a"] || !u["b"] {
		t.Fatalf("unread %v", u)
	}
	if m.dotOf(hidden) != dotDone || m.dotOf(shown) != dotRest {
		t.Fatalf("dots %v %v", m.dotOf(hidden), m.dotOf(shown))
	}
	// An approval shows amber, whether or not it was seen.
	shown.Approval = &protocol.Approval{ID: "x", Tool: "Bash"}
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

func TestSidebarFilterAndLabels(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{ID: "1", Agent: "claude", Project: "/w/api", Branch: "feat/login"},
		{ID: "2", Agent: "shell", Project: "/oss/api"},
		{ID: "3", Agent: "codex", Project: "/w/web"},
	}
	got := filterSessions(sessions, "API login", nil, nil)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("filtered %+v", got)
	}
	if len(filterSessions(sessions, "", nil, nil)) != 3 {
		t.Fatal("an empty filter left sessions out")
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
	twin := groupSessions([]protocol.SessionInfo{{ID: "1", NodeID: "a", Project: "/w/api"}, {ID: "2", NodeID: "b", Project: "/w/api"}}, groupProject, []protocol.Node{{ID: "a", Name: "mac"}, {ID: "b", Name: "box"}}, nil)
	labels = projectLabels(twin, []protocol.Node{{ID: "a", Name: "mac"}, {ID: "b", Name: "box"}})
	if labels[twin[0].Key] != "api · mac" && labels[twin[0].Key] != "api · box" || labels[twin[0].Key] == labels[twin[1].Key] {
		t.Fatalf("twin labels %v", labels)
	}
}

func TestRankFilesPrefersNamesAndRuns(t *testing.T) {
	paths := []string{"internal/gui/view.go", "internal/gui/viewer_test.go", "docs/overview.md", "cmd/sanbo/main.go", "vendor/x/very/long/path/view.go"}
	got := rankFiles(paths, "view", 10)
	if len(got) != 4 || got[0] != "internal/gui/view.go" {
		t.Fatalf("ranked %q", got)
	}
	if got := rankFiles(paths, "sanbomain", 10); len(got) != 1 || got[0] != "cmd/sanbo/main.go" {
		t.Fatalf("ranked %q", got)
	}
	if got := rankFiles(paths, "zzz", 10); len(got) != 0 {
		t.Fatalf("ranked %q", got)
	}
}
