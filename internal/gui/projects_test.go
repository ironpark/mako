package gui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

// A project folder stays in the sidebar after the last session in it is
// gone, and its menu drops it for good.
func TestProjectsOutliveTheirSessions(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	sessions := []protocol.SessionInfo{{ID: "a", NodeID: "n1", Project: "/src/sanbo", Repo: "/src/sanbo"}}
	m.keepProjects(sessions)

	projects := ggui.Peek(m.Prefs).Projects
	want := fileRoot{Node: "n1", Path: "/src/sanbo"}
	if len(projects) != 1 || projects[0] != want {
		t.Fatalf("projects = %+v, want %+v", projects, want)
	}
	// The session is killed; the folder keeps a section of its own.
	groups := groupSessions(nil, groupProject, nil, projects)
	if len(groups) != 1 || groups[0].Project != want.Path || len(groups[0].Sessions) != 0 {
		t.Fatalf("groups = %+v", groups)
	}
	// A kept repository is still the section a session in its subfolder
	// joins.
	sub := []protocol.SessionInfo{{ID: "b", NodeID: "n1", Project: "/src/sanbo/internal", Repo: "/src/sanbo"}}
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
	sessions := []protocol.SessionInfo{
		{ID: "home", NodeID: "n1", Project: "/home/me"},
		{ID: "sanbo", NodeID: "n1", Project: "/home/me/src/sanbo", Repo: "/home/me/src/sanbo"},
		{ID: "tree", NodeID: "n1", Project: "/home/me/src/sanbo/.claude/worktrees/x", Repo: "/home/me/src/sanbo"},
		{ID: "away", NodeID: "n1", Project: "/tmp/sanbo-fix", Repo: "/home/me/src/sanbo"},
		{ID: "toy", NodeID: "n1", Project: "/home/me/toys/rabbithole", Repo: "/home/me/toys/rabbithole"},
		{ID: "notes", NodeID: "n1", Project: "/home/me/notes"},
	}
	kept := []fileRoot{{Node: "n1", Path: "/home/me"}}
	got := map[string][]string{}
	for _, g := range groupSessions(sessions, groupProject, nil, kept) {
		for _, s := range g.Sessions {
			got[g.Project] = append(got[g.Project], s.ID)
		}
	}
	want := map[string][]string{
		"/home/me":                 {"home"},
		"/home/me/src/sanbo":       {"sanbo", "tree", "away"},
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
	t.Setenv("SANBO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = []fileRoot{{Node: "n1", Path: "/src/sanbo"}}
		return p
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1000, 700))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, label := range []string{Tf("New session in %s", "sanbo"), T("No sessions"), T("Folder actions")} {
		if !strings.Contains(page, label) {
			t.Fatalf("the sidebar has no %q:\n%s", label, page)
		}
	}
}

// The composer keeps a draft for each session, so a pane's message is not
// typed into another's.
func TestComposerDraftsArePerSession(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
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
