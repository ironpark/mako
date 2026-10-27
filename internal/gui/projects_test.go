package gui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A project folder stays in the sidebar after the last session in it is
// gone, and its menu drops it for good.
func TestProjectsOutliveTheirSessions(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sessions := []*protocol.SessionInfo{{Id: "a", NodeId: "n1", Project: "/src/mako", Repo: "/src/mako"}}
	m.keepProjects(sessions)

	projects := ggui.Peek(m.Prefs).Projects
	want := fileRoot{Node: "n1", Path: "/src/mako"}
	if len(projects) != 1 || projects[0].fileRoot != want {
		t.Fatalf("projects = %+v, want %+v", projects, want)
	}
	// The session is killed; the folder keeps a section of its own.
	groups := groupSessions(nil, groupProject, nil, projects)
	if len(groups) != 1 || groups[0].Project != want.Path || len(groups[0].Sessions) != 0 {
		t.Fatalf("groups = %+v", groups)
	}
	// A kept repository is still the section a session in its subfolder
	// joins.
	sub := []*protocol.SessionInfo{{Id: "b", NodeId: "n1", Project: "/src/mako/internal", Repo: "/src/mako"}}
	groups = groupSessions(sub, groupProject, nil, projects)
	if len(groups) != 1 || groups[0].Project != want.Path || len(groups[0].Sessions) != 1 {
		t.Fatalf("groups with a subfolder session = %+v", groups)
	}
	m.forgetProject(want)
	if got := ggui.Peek(m.Prefs).Projects; len(got) != 0 {
		t.Fatalf("projects after forget = %+v", got)
	}
}

// A folder that holds others, as a home folder does, does not take their
// sessions: each repository keeps its own section, its worktrees with it,
// and a session in the home folder itself has the home's.
func TestAHomeFolderDoesNotSwallowItsProjects(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "home", NodeId: "n1", Project: "/home/me"},
		{Id: "mako", NodeId: "n1", Project: "/home/me/src/mako", Repo: "/home/me/src/mako"},
		{Id: "tree", NodeId: "n1", Project: "/home/me/src/mako/.claude/worktrees/x", Repo: "/home/me/src/mako"},
		{Id: "away", NodeId: "n1", Project: "/tmp/mako-fix", Repo: "/home/me/src/mako"},
		{Id: "toy", NodeId: "n1", Project: "/home/me/toys/rabbithole", Repo: "/home/me/toys/rabbithole"},
		{Id: "notes", NodeId: "n1", Project: "/home/me/notes"},
	}
	kept := []keptProject{{fileRoot: fileRoot{Node: "n1", Path: "/home/me"}}}
	got := map[string][]string{}
	for _, g := range groupSessions(sessions, groupProject, nil, kept) {
		for _, s := range g.Sessions {
			got[g.Project] = append(got[g.Project], s.GetId())
		}
	}
	want := map[string][]string{
		"/home/me":                 {"home"},
		"/home/me/src/mako":        {"mako", "tree", "away"},
		"/home/me/toys/rabbithole": {"toy"},
		"/home/me/notes":           {"notes"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections %v, want %v", got, want)
	}
}

// A kept folder is drawn with its heading, its start button and its menu
// while no session is in it.
func TestSidebarShowsKeptFolders(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []keptProject{{fileRoot: fileRoot{Node: "n1", Path: "/src/mako"}}}
		return p
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, label := range []string{Tf("New session in %s", "mako"), T("No sessions"), T("Folder actions")} {
		if !strings.Contains(page, label) {
			t.Fatalf("the sidebar has no %q:\n%s", label, page)
		}
	}
}

// The composer keeps a draft for each session, so a pane's message is not
// typed into another's.
func TestComposerDraftsArePerSession(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setComposerDraft("a", "one")
	m.setComposerDraft("b", "two")
	if m.composerDraft("a") != "one" || m.composerDraft("b") != "two" {
		t.Fatalf("drafts = %q, %q", m.composerDraft("a"), m.composerDraft("b"))
	}
	m.setComposerDraft("a", "")
	if m.composerDraft("a") != "" || m.composerDraft("b") != "two" {
		t.Fatalf("drafts after clearing a = %q, %q", m.composerDraft("a"), m.composerDraft("b"))
	}
}

// A kept folder remembers whose repository it is, as its sessions said.
func TestKeptProjectsRememberOwners(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	s := &protocol.SessionInfo{Id: "a", NodeId: "n1", Project: "/src/mako", Repo: "/src/mako", Owner: "github.com/ironpark"}
	m.keepProjects([]*protocol.SessionInfo{s})
	s.Owner = "git.ironpark.dev/ironpark"
	m.keepProjects([]*protocol.SessionInfo{s, s})
	want := []keptProject{{fileRoot{Node: "n1", Path: "/src/mako"}, "git.ironpark.dev/ironpark"}}
	if got := ggui.Peek(m.Prefs).Projects; !slices.Equal(got, want) {
		t.Fatalf("projects %+v, want %+v", got, want)
	}
}

// Project sections gather under their repositories' owners, a kept folder
// by the owner remembered for it, and folders no one owns come last.
func TestProjectsGroupByOwner(t *testing.T) {
	sessions := []*protocol.SessionInfo{
		{Id: "1", NodeId: "a", Project: "/w/mako", Owner: "github.com/ironpark"},
		{Id: "2", NodeId: "a", Project: "/w/go", Owner: "github.com/golang"},
		{Id: "4", NodeId: "a", Project: "/w/homescale", Owner: "git.ironpark.dev/ironpark"},
		{Id: "3", NodeId: "a", Project: "/tmp/scratch"},
	}
	kept := []keptProject{{fileRoot{Node: "a", Path: "/w/ggui"}, "github.com/ironpark"}}
	groups := groupSessions(sessions, groupProject, nil, kept)
	sections := byOwner(groups)
	var got []string
	for _, o := range sections {
		var names []string
		for _, g := range o.Groups {
			names = append(names, projectName(g.Project))
		}
		got = append(got, o.Owner+":"+strings.Join(names, ","))
	}
	if want := []string{"github.com/golang:go", "git.ironpark.dev/ironpark:homescale", "github.com/ironpark:ggui,mako", ":scratch"}; !slices.Equal(got, want) {
		t.Fatalf("sections %q, want %q", got, want)
	}
	if byOwner(groupSessions(sessions[3:], groupProject, nil, nil)) != nil {
		t.Fatal("folders no one owns are grouped by owner")
	}
}

func TestHostsWithoutSessionsAreListed(t *testing.T) {
	nodes := []*protocol.Node{{Id: "a", Name: "mac"}, {Id: "b", Name: "box"}}
	groups := withHosts(groupSessions([]*protocol.SessionInfo{{Id: "1", NodeId: "a"}}, groupHost, nodes, nil), nodes)
	if len(groups) != 2 || groups[0].NodeID != "b" || len(groups[0].Sessions) != 0 || groups[1].NodeID != "a" {
		t.Fatalf("groups %+v", groups)
	}
}
