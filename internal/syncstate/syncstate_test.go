package syncstate

import (
	"bytes"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

func TestPluginConfigSyncCRDTProperties(t *testing.T) {
	now := int64(1_700_000_000_000)
	cA := crdt.NewClock("node-a", true)
	cB := crdt.NewClock("node-b", true)

	t1 := cA.Now(now)
	t2 := cB.Now(now + 10)
	t3 := cA.Now(now + 20)

	a := &protocol.PluginConfigSyncState{
		Registries: map[string]*protocol.ElementMeta{
			"https://reg.example/a": {AddedAt: FromHLC(t1)},
		},
		Installs: map[string]*protocol.PluginInstallSyncEntry{
			"kanban": {Installed: true, Registry: "https://reg.example/a", Key: "key-1", Version: "1.0.0", Clock: FromHLC(t1)},
		},
		Disabled: map[string]*protocol.BoolReg{
			"kanban": {Value: false, Clock: FromHLC(t1)},
		},
		Settings: map[string]*protocol.BytesReg{
			SettingSyncKey("kanban", "", "columns"): {Value: []byte(`"todo,done"`), Clock: FromHLC(t1)},
		},
	}
	b := &protocol.PluginConfigSyncState{
		Registries: map[string]*protocol.ElementMeta{
			"https://reg.example/b": {AddedAt: FromHLC(t2)},
		},
		Installs: map[string]*protocol.PluginInstallSyncEntry{
			"kanban": {Installed: true, Registry: "https://reg.example/a", Key: "key-1", Version: "1.1.0", Clock: FromHLC(t2)},
		},
		Disabled: map[string]*protocol.BoolReg{
			"kanban": {Value: true, Clock: FromHLC(t3)},
		},
		Settings: map[string]*protocol.BytesReg{
			SettingSyncKey("kanban", "", "columns"): {Deleted: true, Clock: FromHLC(t2)},
		},
	}

	// Commutativity: merge(a, b) == merge(b, a)
	ab := proto.CloneOf(a)
	MergePluginConfig(ab, b, crdt.NewClock("node-a", true), now+50)
	ba := proto.CloneOf(b)
	MergePluginConfig(ba, a, crdt.NewClock("node-b", true), now+50)

	if !bytes.Equal(DigestPluginConfig(ab), DigestPluginConfig(ba)) {
		t.Fatalf("merge not commutative:\nab=%+v\nba=%+v", ab, ba)
	}
	// Idempotency: merging again makes no change.
	if MergePluginConfig(ab, ba, crdt.NewClock("node-a", true), now+50) {
		t.Fatal("expected idempotent re-merge to report changed=false")
	}
	if regs := LiveRegistries(ab); len(regs) != 2 {
		t.Fatalf("LiveRegistries = %v, want 2", regs)
	}
	if ab.Installs["kanban"].GetVersion() != "1.1.0" {
		t.Fatalf("install version = %q, want 1.1.0", ab.Installs["kanban"].GetVersion())
	}
	if !ab.Disabled["kanban"].GetValue() {
		t.Fatal("expected disabled=true from newer clock t3")
	}
	if !ab.Settings[SettingSyncKey("kanban", "", "columns")].GetDeleted() {
		t.Fatal("expected setting tombstone from newer clock t2")
	}
}

func TestQuarantineAndHeal(t *testing.T) {
	now := int64(1_700_000_000_000)
	future := crdt.HLC{WallTime: now + 120_000, Logical: 1, NodeID: "bad-node"}
	dst := &protocol.AgentSyncState{}
	src := &protocol.AgentSyncState{
		Templates: map[string]*protocol.BytesReg{
			"future.md": {Value: []byte("bad"), Clock: FromHLC(future)},
		},
	}
	clock := crdt.NewClock("good-node", true)
	if MergeAgentConfig(dst, src, clock, now) {
		t.Fatal("expected future-drifted entry to be quarantined")
	}
	if len(dst.GetTemplates()) != 0 {
		t.Fatalf("expected quarantined template ignored, got %+v", dst.GetTemplates())
	}

	// Self-healing when bad-node's own clock is fixed back to now.
	badClock := crdt.NewClock("bad-node", true)
	if !HealAgentConfig(src, badClock, now) {
		t.Fatal("expected HealAgentConfig to restamp future self entry")
	}
	if crdt.Quarantined(ToHLC(src.Templates["future.md"].GetClock()), now) {
		t.Fatal("healed entry still quarantined")
	}
}

func TestAgentAndMCPSanitizationPreservesLocalEnv(t *testing.T) {
	on := true
	servers := []string{"mako"}
	orig := agentspec.Agent{
		Command:    []string{"claude"},
		Args:       []string{"--verbose"},
		Env:        map[string]string{"ANTHROPIC_API_KEY": "secret-a"},
		Model:      "claude-sonnet-4-6",
		Effort:     "medium",
		Permission: "acceptEdits",
		Yolo:       true,
		Enabled:    &on,
		Profiles: []agentspec.Profile{
			{
				Name:           "fast",
				Description:    "Quick helper",
				Model:          "haiku",
				Effort:         "low",
				Permission:     "plan",
				Args:           []string{"--strict"},
				ReplaceArgs:    true,
				Env:            map[string]string{"PROFILE_SECRET": "secret-p"},
				ContextServers: &servers,
			},
		},
	}
	raw, err := MarshalAgent(orig)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-a")) || bytes.Contains(raw, []byte("secret-p")) {
		t.Fatalf("sanitized agent leaked env: %s", string(raw))
	}

	var decoded agentspec.Agent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	local := agentspec.Agent{
		Env: map[string]string{"ANTHROPIC_API_KEY": "local-key"},
		Profiles: []agentspec.Profile{
			{Name: "fast", Env: map[string]string{"LOCAL_P": "keep-me"}},
		},
	}
	merged := MergeLocalAgentEnv(decoded, local)
	if merged.Env["ANTHROPIC_API_KEY"] != "local-key" {
		t.Fatalf("lost local agent env: %+v", merged.Env)
	}
	if merged.Profiles[0].Env["LOCAL_P"] != "keep-me" {
		t.Fatalf("lost local profile env: %+v", merged.Profiles[0].Env)
	}
	if !merged.Yolo || merged.Model != "claude-sonnet-4-6" || merged.Effort != "medium" || merged.Permission != "acceptEdits" {
		t.Fatalf("synced agent fields not applied: %+v", merged)
	}
	if p := merged.Profiles[0]; p.Model != "haiku" || p.Effort != "low" || p.Permission != "plan" || !p.ReplaceArgs {
		t.Fatalf("synced profile fields not applied: %+v", p)
	}

	// Built-in default detection ignores env, but catches model/effort/permission.
	if !IsDefaultAgent("claude", agentspec.Agent{Command: []string{"claude"}, Env: map[string]string{"A": "B"}}) {
		t.Fatal("expected untouched built-in with local env to count as default")
	}
	if IsDefaultAgent("claude", agentspec.Agent{Command: []string{"claude"}, Permission: "plan"}) {
		t.Fatal("built-in with custom permission should not count as default")
	}
	if IsDefaultAgent("claude", agentspec.Agent{Command: []string{"claude"}, Model: "opus"}) {
		t.Fatal("built-in with custom model should not count as default")
	}
	if IsDefaultAgent("claude", agentspec.Agent{Command: []string{"claude"}, Claude: agentspec.ClaudeSettings{CommitSignature: true}}) {
		t.Fatal("built-in with custom claude settings should not count as default")
	}
	fastOn := true
	if IsDefaultAgent("codex", agentspec.Agent{Command: []string{"codex"}, Fast: &fastOn}) {
		t.Fatal("built-in with custom fast setting should not count as default")
	}
	if IsDefaultAgent("claude", orig) {
		t.Fatal("customized built-in should not count as default")
	}

	// MCP server sanitization strips Command.Env and preserves local Command.Env.
	srv := mcp.Server{
		Enabled:  &on,
		Settings: json.RawMessage(`{ "b": 2, "a": 1 }`),
		Command:  &mcp.Command{Path: "npx", Args: []string{"-y", "srv"}, Env: map[string]string{"TOKEN": "nope"}},
	}
	srvBytes, err := MarshalMCPServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(srvBytes, []byte("nope")) {
		t.Fatalf("sanitized MCP server leaked env: %s", string(srvBytes))
	}
	var decSrv mcp.Server
	if err := json.Unmarshal(srvBytes, &decSrv); err != nil {
		t.Fatal(err)
	}
	mergedSrv := MergeLocalMCPEnv(decSrv, mcp.Server{
		Command: &mcp.Command{Path: "npx", Env: map[string]string{"LOCAL_TOKEN": "ok"}},
	})
	if mergedSrv.Command.Env["LOCAL_TOKEN"] != "ok" {
		t.Fatalf("lost local MCP env: %+v", mergedSrv.Command.Env)
	}
}

func TestValidationAndEmptyDigests(t *testing.T) {
	if DigestPluginConfig(&protocol.PluginConfigSyncState{}) != nil {
		t.Fatal("empty plugin config state should have nil digest")
	}
	if DigestAgentConfig(&protocol.AgentSyncState{}) != nil {
		t.Fatal("empty agent config state should have nil digest")
	}
	if !ValidTemplateFilename("review.md") || ValidTemplateFilename("../evil.md") || ValidTemplateFilename(".hidden.md") || ValidTemplateFilename("plain.txt") {
		t.Fatal("ValidTemplateFilename failed")
	}
	if !ValidSkillName("mako") || ValidSkillName("../etc") || ValidSkillName(".git") {
		t.Fatal("ValidSkillName failed")
	}
	if !ValidSkillKey("mako") || !ValidSkillKey("mako/cli/projects-and-agents.md") ||
		ValidSkillKey("mako/../evil.md") || ValidSkillKey("mako/.hidden.md") || ValidSkillKey("mako/plain.txt") {
		t.Fatal("ValidSkillKey failed")
	}
}
