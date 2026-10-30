package gui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestProjectAgentsPanelFiltersMessagesAndOpensSessions(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	old := currentLanguage()
	setLanguage("en")
	t.Cleanup(func() { setLanguage(old) })
	m := newModel(config.Default(), "")
	root := fileRoot{Node: "node", Path: t.TempDir()}
	snapshot := projectAgentsPreviewData(root)
	m.ProjectAgents.Set(projectAgentsView{Root: root, Data: snapshot, Loaded: true, Updated: time.Now()})
	for _, a := range snapshot.GetAgents() {
		m.dialing[a.GetSession().GetId()] = true
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return projectAgentsBody(m, root) }, ggui.Sz(360, 1000))
	defer p.Close()
	p.Frame()
	for _, text := range []string{"1 working · 1 waiting · 1 pending", "Review cancellation", "Build passed", "Awaiting acknowledgement"} {
		if _, ok := p.Semantics().Find(ggui.RoleText, text); !ok {
			t.Fatalf("missing %q:\n%s", text, p.Semantics().String())
		}
	}
	// An acknowledged message is the usual case and says nothing of it.
	if _, ok := p.Semantics().Find(ggui.RoleText, "Acknowledged"); ok {
		t.Fatal("an acknowledged message was labelled")
	}
	if _, ok := p.Find("Messages divider"); !ok {
		t.Fatal("the agents and their messages cannot be resized")
	}
	p.Tap("Filter messages for Reviewer")
	if _, ok := p.Semantics().Find(ggui.RoleText, "Build passed"); ok {
		t.Fatal("another agent's message was shown after filtering")
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, "Review cancellation"); !ok {
		t.Fatal("filtered message disappeared")
	}
	p.Tap("All agents")
	if _, ok := p.Semantics().Find(ggui.RoleText, "Build passed"); !ok {
		t.Fatal("clearing the filter did not restore messages")
	}
	p.Tap("Open session Reviewer")
	if ggui.Peek(m.Selected) != "reviewer" {
		t.Fatal("opening the session did not select it")
	}
	if _, ok := m.session("reviewer"); !ok {
		t.Fatal("a newly spawned session was not added before opening")
	}
}

func TestProjectAgentHierarchyHandlesMissingParentsAndCycles(t *testing.T) {
	makeAgent := func(id, parent string) *protocol.ProjectAgent {
		return &protocol.ProjectAgent{Session: &protocol.SessionInfo{Id: id}, ParentSessionId: parent}
	}
	rows := projectAgentRows([]*protocol.ProjectAgent{makeAgent("child", "parent"), makeAgent("parent", ""), makeAgent("orphan", "missing"), makeAgent("a", "b"), makeAgent("b", "a")})
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.Agent.GetSession().GetId())
	}
	if !slices.Equal(ids, []string{"parent", "child", "orphan", "a", "b"}) || rows[1].Depth != 1 {
		t.Fatalf("hierarchy: %v", rows)
	}
}

func TestProjectAgentMonitorPollsOnlyWhileShownAndChangesProjects(t *testing.T) {
	m, p := daemonApp(t)
	m.launch("shell", "", t.TempDir())
	probeUntil(t, p, "first session", func() bool { return len(ggui.Peek(m.Sessions)) == 1 })
	first := ggui.Peek(m.Selected)
	m.DetailTab.Set(agentsTab)
	probeUntil(t, p, "agent monitor", func() bool { return ggui.Peek(m.ProjectAgents).Loaded })
	v := ggui.Peek(m.ProjectAgents)
	if v.Err != "" || len(v.Data.GetAgents()) != 1 {
		t.Fatalf("monitor: %+v", v)
	}
	m.DetailsOpen.Set(false)
	p.Frame()
	before := m.projectAgentsGen
	deadline := time.Now().Add(2200 * time.Millisecond)
	for time.Now().Before(deadline) {
		p.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if m.projectAgentsGen != before {
		t.Fatal("hidden monitor kept polling")
	}
	m.launch("shell", "", t.TempDir())
	probeUntil(t, p, "second session", func() bool { return len(ggui.Peek(m.Sessions)) == 2 && ggui.Peek(m.Selected) != first })
	m.DetailsOpen.Set(true)
	probeUntil(t, p, "second project monitor", func() bool {
		v := ggui.Peek(m.ProjectAgents)
		return v.Loaded && v.Root == m.filesRoot() && len(v.Data.GetAgents()) == 1 && v.Data.GetAgents()[0].GetSession().GetId() != first
	})
}

func TestProjectAgentMonitorExplainsOlderDaemons(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n", Revision: protocol.RevisionProjectAgents - 1}})
	m.loadProjectAgents(fileRoot{Node: "n", Path: "/project"})
	if v := ggui.Peek(m.ProjectAgents); v.Err == "" || v.Loading {
		t.Fatal("old daemon was not explained")
	}
}

func TestProjectMessagePreviewPreservesUnicode(t *testing.T) {
	text := strings.Repeat("메시지", 100) + "\ncontinued"
	if got := projectMessagePreview(text); len([]rune(got)) != 181 || !strings.HasSuffix(got, "…") {
		t.Fatalf("preview: %q", got)
	}
}
