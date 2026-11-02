package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// Agents in two folders of one project are one project's: they list and
// message each other, and a third folder's agent stays out. The daemon
// keeps the projects across a restart.
func TestAProjectOfTwoFoldersSharesItsAgents(t *testing.T) {
	d, parent := projectMCPDaemon(t)
	d.projects.load(d.dir)
	web := t.TempDir()
	other, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := parent.Info().GetProject()
	d.projects.set([]*protocol.ProjectScope{{Id: "product", Name: "Product", Paths: []string{server, web, "relative"}}})
	sub := filepath.Join(web, "src")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ui, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: sub})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := d.projectScope(ui.Info()), "project:product"; got != want || d.projectScope(parent.Info()) != want {
		t.Fatalf("scopes %q and %q, want %q", got, d.projectScope(parent.Info()), want)
	}
	client := projectMCPClient(t, d, parent)
	listed := callProjectTool[listAgentsResult](t, client, "list_agents", struct{}{})
	if !slices.ContainsFunc(listed.Agents, func(a agentSummary) bool { return a.ID == ui.ID() }) {
		t.Fatalf("the web folder's agent is not listed: %+v", listed.Agents)
	}
	if slices.ContainsFunc(listed.Agents, func(a agentSummary) bool { return a.ID == other.ID() }) {
		t.Fatal("another folder's agent is listed")
	}
	callProjectTool[agentMessage](t, client, "send_message", sendMessageArgs{To: ui.ID(), Text: "the API changed"})

	var kept Daemon
	kept.projects.load(d.dir)
	if got := kept.projects.get(); len(got) != 1 || len(got[0].GetPaths()) != 2 {
		t.Fatalf("kept %v", got)
	}
}

// A project that gives its folders gives an agent started in one the
// others, as the agent's option takes them, and none inside its own.
func TestAProjectGivesItsFolders(t *testing.T) {
	d, _ := projectMCPDaemon(t)
	server, web := t.TempDir(), t.TempDir()
	inner := filepath.Join(server, "docs")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	d.projects.set([]*protocol.ProjectScope{{Id: "p", Paths: []string{server, web, inner, filepath.Join(web, "gone")}, GiveDirs: true}})
	if got := d.projectDirs(server); !slices.Equal(got, []string{web}) {
		t.Fatalf("dirs %v", got)
	}
	if got := withProjectDirs("claude", []string{"claude"}, []string{web}); !slices.Equal(got, []string{"claude", "--add-dir=" + web}) {
		t.Fatalf("claude %v", got)
	}
	if got := withProjectDirs("codex", []string{"codex"}, []string{web}); !slices.Equal(got, []string{"codex", "--add-dir", web}) {
		t.Fatalf("codex %v", got)
	}
	if got := withProjectDirs("shell", []string{"zsh"}, []string{web}); !slices.Equal(got, []string{"zsh"}) {
		t.Fatalf("shell %v", got)
	}
	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: server})
	if err != nil {
		t.Fatal(err)
	}
	if cmd := strings.Join(s.Info().GetCommand(), " "); !strings.Contains(cmd, "--add-dir="+web) {
		t.Fatalf("started as %q", cmd)
	}
	d.projects.set([]*protocol.ProjectScope{{Id: "p", Paths: []string{server, web}}})
	if got := d.projectDirs(server); got != nil {
		t.Fatalf("a project that does not give its folders gave %v", got)
	}
}
