//go:build unix

package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
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

// Starting an agent in a new project or a multi-folder project with GiveDirs
// pre-trusts both the working directory and every sub-project / --add-dir
// folder in the agent's native trust store before the process launches.
func TestSessionLaunchPreTrustsProjectAndSubprojectDirs(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())

	claudeCmd, claudeRoot := fakeAgent(t, "claude")
	codexCmd, codexRoot := fakeAgent(t, "codex")
	agyCmd, agyRoot := fakeAgent(t, "agy")
	agyHome := t.TempDir()

	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {
				Command: []string{claudeCmd},
				Env:     map[string]string{"MAKO_FAKE_AGENT": "claude", "MAKO_FAKE_ROOT": claudeRoot, "CLAUDE_CONFIG_DIR": claudeRoot},
			},
			"codex": {
				Command: []string{codexCmd},
				Env:     map[string]string{"MAKO_FAKE_AGENT": "codex", "MAKO_FAKE_ROOT": codexRoot, "CODEX_HOME": codexRoot},
			},
			"agy": {
				Command: []string{agyCmd},
				Env:     map[string]string{"MAKO_FAKE_AGENT": "agy", "MAKO_FAKE_ROOT": agyRoot, "HOME": agyHome},
			},
		},
	}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp = t.TempDir(), t.Context(), nil
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	// 1. Standalone new project (no GiveDirs scope).
	solo := t.TempDir()
	if _, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "claude", Project: solo}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "codex", Project: solo}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "agy", Project: solo}); err != nil {
		t.Fatal(err)
	}

	// 2. Multi-folder / nested project with GiveDirs enabled + an explicit --add-dir arg.
	mainDir := t.TempDir()
	sub1, sub2, sub3, manualDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	d.projects.set([]*protocol.ProjectScope{{
		Id:       "composite",
		Paths:    []string{mainDir, sub1, sub2, sub3},
		GiveDirs: true,
	}})

	if _, err := d.openSession(t.Context(), &protocol.OpenSession{
		Agent:   "claude",
		Project: mainDir,
		Args:    []string{"--add-dir=" + manualDir},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.openSession(t.Context(), &protocol.OpenSession{
		Agent:   "codex",
		Project: mainDir,
		Args:    []string{"--add-dir", manualDir},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.openSession(t.Context(), &protocol.OpenSession{
		Agent:   "agy",
		Project: mainDir,
		Args:    []string{"--add-dir", manualDir},
	}); err != nil {
		t.Fatal(err)
	}

	wantDirs := agent.CanonicalDirs([]string{solo, mainDir, sub1, sub2, sub3, manualDir})

	// Verify Claude Code .claude.json
	clBody, err := os.ReadFile(filepath.Join(claudeRoot, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var clState struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(clBody, &clState); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	for _, dir := range wantDirs {
		if !clState.Projects[dir].Accepted {
			t.Errorf("claude .claude.json missing trust for %q: %+v", dir, clState.Projects)
		}
	}

	// Verify Codex config.toml
	cxBody, err := os.ReadFile(filepath.Join(codexRoot, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config.toml: %v", err)
	}
	var cxCfg struct {
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		} `toml:"projects"`
	}
	if _, err := toml.Decode(string(cxBody), &cxCfg); err != nil {
		t.Fatalf("parse codex config.toml: %v\n%s", err, cxBody)
	}
	for _, dir := range wantDirs {
		if cxCfg.Projects[dir].TrustLevel != "trusted" {
			t.Errorf("codex config.toml missing trust for %q: %+v", dir, cxCfg.Projects)
		}
	}

	// Verify Agy settings.json
	agyBody, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatalf("read agy settings.json: %v", err)
	}
	var agySettings struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(agyBody, &agySettings); err != nil {
		t.Fatalf("parse agy settings.json: %v", err)
	}
	for _, dir := range wantDirs {
		if !slices.Contains(agySettings.TrustedWorkspaces, dir) {
			t.Errorf("agy settings.json missing trustedWorkspace %q: %q", dir, agySettings.TrustedWorkspaces)
		}
	}
}
