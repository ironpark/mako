package gui

import (
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
	sessions := []protocol.SessionInfo{{ID: "a", NodeID: "n1", Project: "/src/sanbo"}}
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
	// A kept folder is still the root a session in a subfolder joins.
	sub := []protocol.SessionInfo{{ID: "b", NodeID: "n1", Project: "/src/sanbo/internal"}}
	groups = groupSessions(sub, groupProject, nil, projects)
	if len(groups) != 1 || groups[0].Project != want.Path || len(groups[0].Sessions) != 1 {
		t.Fatalf("groups with a subfolder session = %+v", groups)
	}
	m.forgetProject(want)
	if got := ggui.Peek(m.Prefs).Projects; len(got) != 0 {
		t.Fatalf("projects after forget = %+v", got)
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
