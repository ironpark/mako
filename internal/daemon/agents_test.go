package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
)

// An agent edited in the config file is launched as edited, and the node
// says so, without a restart.
func TestAgentsFollowTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Switched on, as an agent not installed here, as on CI, is not offered.
	on := true
	write(`{"agents": {"claude": {"command": ["claude"], "enabled": true}}}`)
	d := New(&config.Config{NodeID: "n1", Agents: map[string]agentspec.Agent{"claude": {Command: []string{"claude"}, Enabled: &on}}}, nil)
	d.FollowAgents(path)

	later := time.Now().Add(time.Second)
	write(`{"agents": {"claude": {"command": ["claude"], "enabled": true, "profiles": [{"name": "opus", "args": ["--model", "opus"]}]}}}`)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if p, ok := d.agents()["claude"].Profile("opus"); !ok || len(p.Args) != 2 {
		t.Fatalf("claude after the edit = %+v", d.agents()["claude"])
	}
	var profiles []string
	for _, a := range d.Self().GetAgents() {
		if a.GetName() == "claude" {
			profiles = a.GetProfiles()
		}
	}
	if len(profiles) != 1 || profiles[0] != "opus" {
		t.Fatalf("the node advertises claude's profiles as %q", profiles)
	}

	write(`{"agents": `) // half written
	if err := os.Chtimes(path, later.Add(time.Second), later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.agents()["claude"].Profile("opus"); !ok {
		t.Fatal("a config file that does not parse replaced the agents")
	}
}

func TestSyncBuiltinSkillsSupportsOmpAndOmo(t *testing.T) {
	home := t.TempDir()
	ompDir := filepath.Join(home, ".omp", "agent")
	omoDir := filepath.Join(home, ".omo", "agent")
	if err := os.MkdirAll(ompDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(omoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("OMO_CODING_AGENT_DIR", "")

	d := New(&config.Config{NodeID: "n1"}, nil)
	d.syncBuiltinSkills(t.Context())

	for _, root := range []string{ompDir, omoDir} {
		if _, err := os.Stat(filepath.Join(root, "skills", "mako", "SKILL.md")); err != nil {
			t.Fatalf("expected synced skill under %s: %v", root, err)
		}
		if _, err := os.Stat(filepath.Join(root, "skills", "mako", "cli", "projects-and-agents.md")); err != nil {
			t.Fatalf("expected synced subfolder skill under %s: %v", root, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("uninitialized ~/.claude should be skipped, stat err=%v", err)
	}
}

func TestConfigConcurrentDecisionUpdateAndReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"node_id":"n1","name":"node"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{
		NodeID: "n1",
		Name:   "node",
		Decision: config.Decision{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
			Models:   map[string]string{"anthropic": "claude-sonnet-4-6"},
		},
	}, nil)
	d.FollowAgents(path)

	var wg sync.WaitGroup
	for g := range 6 {
		wg.Go(func() {
			for i := range 25 {
				switch g % 3 {
				case 0:
					next := d.decisionConfig()
					if next.Models == nil {
						next.Models = map[string]string{}
					}
					next.Provider = "openai"
					next.Model = fmt.Sprintf("gpt-test-%d", i)
					next.Models["openai"] = next.Model
					if err := d.setDecisionConfig(next); err != nil {
						t.Error(err)
					}
				case 1:
					_ = d.Self()
					_ = d.localDevices(t.Context())
				case 2:
					_ = d.followed()
					dc := d.decisionConfig()
					if dc.Models != nil {
						dc.Models["mutated-caller-copy"] = "safe"
					}
				}
			}
		})
	}
	wg.Wait()
	// Confirm caller-side mutation never leaked into daemon's decision config.
	if _, leaked := d.decisionConfig().Models["mutated-caller-copy"]; leaked {
		t.Fatal("caller mutation of decisionConfig().Models leaked into daemon state")
	}
}

func TestFollowedConfigStatFastPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"node_id":"n1","agents":{"claude":{"command":["claude"],"enabled":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{NodeID: "n1"}, nil)
	d.FollowAgents(path)

	reads := func() int {
		d.agentFile.mu.Lock()
		defer d.agentFile.mu.Unlock()
		return d.agentFile.reads
	}
	if got := reads(); got != 1 {
		t.Fatalf("reads after FollowAgents = %d, want 1", got)
	}
	for range 10 {
		_ = d.followed()
	}
	if got := reads(); got != 1 {
		t.Fatalf("reads after repeated unchanged followed() calls = %d, want 1", got)
	}

	// setDecisionConfig updates stat cache so subsequent followed() does not re-read.
	if err := d.setDecisionConfig(config.Decision{Provider: "anthropic", Model: "sonnet"}); err != nil {
		t.Fatal(err)
	}
	_ = d.followed()
	if got := reads(); got != 1 {
		t.Fatalf("reads after setDecisionConfig + followed() = %d, want 1", got)
	}
}

func TestFollowedConfigMissingRecreatedAndMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	orig := `{"node_id":"n1","agents":{"claude":{"command":["claude"],"enabled":true}}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{NodeID: "n1"}, nil)
	d.FollowAgents(path)

	reads := func() int {
		d.agentFile.mu.Lock()
		defer d.agentFile.mu.Unlock()
		return d.agentFile.reads
	}
	fiOrig, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Delete file -> followed() invalidates stat cache without failing;
	// recreating with identical ModTime and Size triggers re-read once.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_ = d.followed()
	recreated := `{"node_id":"n1","agents":{"claude":{"command":["c-new2"],"enabled":true}}}`
	if len(recreated) != len(orig) {
		t.Fatalf("test setup error: length mismatch %d vs %d", len(recreated), len(orig))
	}
	if err := os.WriteFile(path, []byte(recreated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fiOrig.ModTime(), fiOrig.ModTime()); err != nil {
		t.Fatal(err)
	}
	_ = d.followed()
	if got := reads(); got != 2 {
		t.Fatalf("reads after delete+recreate with same mtime/size = %d, want 2", got)
	}
	if cmd := d.agents()["claude"].Command; len(cmd) != 1 || cmd[0] != "c-new2" {
		t.Fatalf("recreated config not reloaded: %v", cmd)
	}

	// 2. 0-byte / malformed JSON is read once (cached by stat) and keeps prior agents.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	later := fiOrig.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_ = d.followed()
	}
	if got := reads(); got != 3 {
		t.Fatalf("reads on unchanged 0-byte file = %d, want 3", got)
	}
	if cmd := d.agents()["claude"].Command; len(cmd) != 1 || cmd[0] != "c-new2" {
		t.Fatalf("0-byte file clobbered prior agents: %v", cmd)
	}
}

func TestSetDecisionConfigMalformedJSONAndWriteErrorDoNotMutateState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"node_id":"n1","decision":{"provider":"anthropic","model":"claude-sonnet-4-6"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{
		NodeID: "n1",
		Decision: config.Decision{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
		},
	}, nil)
	d.FollowAgents(path)

	// 1. Malformed JSON on disk: setDecisionConfig must fail without mutating in-memory state.
	if err := os.WriteFile(path, []byte(`{"node_id": `), 0o600); err != nil {
		t.Fatal(err)
	}
	err := d.setDecisionConfig(config.Decision{
		Provider: "google",
		Model:    "gemini-probe-broken",
	})
	if err == nil {
		t.Fatal("expected setDecisionConfig to fail on malformed config.json")
	}
	if got := d.decisionConfig(); got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("d.decisionConfig() mutated on error: %+v", got)
	}
	if got := d.config().Decision; got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("d.config().Decision mutated on error: %+v", got)
	}

	// 2. fsutil.Write failure (target path is a directory): must fail without mutating in-memory state.
	badPath := filepath.Join(dir, "as-dir")
	if err := os.Mkdir(badPath, 0o700); err != nil {
		t.Fatal(err)
	}
	d.agentFile.mu.Lock()
	d.agentFile.path = badPath
	d.agentFile.mu.Unlock()
	if err := d.setDecisionConfig(config.Decision{
		Provider: "openai",
		Model:    "gpt-broken",
	}); err == nil {
		t.Fatal("expected setDecisionConfig to fail when target path is a directory")
	}
	if got := d.decisionConfig(); got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("d.decisionConfig() mutated on write error: %+v", got)
	}
	if got := d.config().Decision; got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("d.config().Decision mutated on write error: %+v", got)
	}
}

func TestSetDecisionConfigSyncsExternalDiskEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"node_id":"n1","agents":{"claude":{"command":["claude-v1"],"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{NodeID: "n1"}, nil)
	d.FollowAgents(path)

	// External edit to agents on disk before setDecisionConfig (without calling followed() first).
	edited := `{"node_id":"n1","agents":{"codex":{"command":["codex-v2"],"enabled":true}}}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	if err := d.setDecisionConfig(config.Decision{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatalf("setDecisionConfig: %v", err)
	}

	agentsMap := d.agents()
	if _, hasClaude := agentsMap["claude"]; hasClaude {
		t.Fatalf("stale claude agent remained after external disk removal: %+v", agentsMap)
	}
	if cmd := agentsMap["codex"].Command; len(cmd) != 1 || cmd[0] != "codex-v2" {
		t.Fatalf("d.agents() did not reflect disk edit: %+v", agentsMap)
	}
	if cfgAgents := d.config().Agents; len(cfgAgents["codex"].Command) != 1 || cfgAgents["codex"].Command[0] != "codex-v2" {
		t.Fatalf("d.config().Agents did not reflect disk edit: %+v", cfgAgents)
	}
	if dec := d.decisionConfig(); dec.Provider != "openai" || dec.Model != "gpt-5" {
		t.Fatalf("d.decisionConfig() = %+v", dec)
	}
}
