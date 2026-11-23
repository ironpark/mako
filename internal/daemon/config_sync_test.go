package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/plugin/pack"
	pluginregistry "github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// TestPluginConfigSyncAcrossMesh verifies end-to-end P2P sync of registries,
// store installs, non-secret settings, disabled flags, and removals across two
// mutually trusted daemons while keeping permissions and secrets node-local.
func TestPluginConfigSyncAcrossMesh(t *testing.T) {
	testRegistries = true
	t.Cleanup(func() { testRegistries = false })

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	manifest := `id = "hello"
version = "1.0.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "hi"
title = "Hello"

[contributes.settings.greeting]
type = "string"
label = "Greeting"

[contributes.settings.api_token]
type = "secret"
label = "API Token"
`
	data, _, err := pack.Build(map[string][]byte{
		"plugin.toml": []byte(manifest),
		"main.js":     []byte(`export default { activate(m) { m.blocks.register("hi", { state: {}, render: (s, ui) => ui.text("hi") }) } }`),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sum := sha256.Sum256(data)
	idx := &pluginregistry.Index{
		Format: pluginregistry.Format,
		Name:   "test",
		Plugins: []*pluginregistry.Entry{{
			ID:        "hello",
			Publisher: "test",
			Key:       pack.PublicKey(priv),
			Versions: []*pluginregistry.Version{{
				Version:     "1.0.0",
				API:         1,
				URL:         srv.URL + "/hello.makoplugin",
				SHA256:      hex.EncodeToString(sum[:]),
				Size:        int64(len(data)),
				Permissions: []string{"storage"},
			}},
		}},
	}
	indexJSON, err := idx.Marshal(true)
	if err != nil {
		t.Fatal(err)
	}
	regURL := srv.URL + "/reg/mako-registry.json"
	mux.HandleFunc("/reg/mako-registry.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(indexJSON) })
	mux.HandleFunc("/hello.makoplugin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })

	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)

	ca := dialDaemon(t, a)
	cb := dialDaemon(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Add registry and install plugin on node A.
	if _, err := ca.RegistryAction(ctx, &protocol.RegistryAction{Op: protocol.RegistryAdd, Url: regURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.InstallPlugin(ctx, &protocol.InstallPlugin{Op: protocol.InstallOp, Id: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello loaded on A", func() bool {
		list, err := ca.Plugins(ctx)
		return err == nil && findPlugin(list.GetPlugins(), "hello") != nil
	})
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginGrant}); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginSet, Key: "greeting", Value: "annyeong"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginSet, Key: "api_token", Value: "super-secret"}); err != nil {
		t.Fatal(err)
	}

	// Verify node B receives the registry, installs hello in Waiting state (grants NOT synced),
	// receives the non-secret setting, and does NOT receive the secret setting.
	waitFor(t, "hello installed with synced non-secret setting on B", func() bool {
		regs, err := cb.Registries(ctx)
		if err != nil || len(regs.GetRegistries()) != 1 {
			return false
		}
		list, err := cb.Plugins(ctx)
		if err != nil {
			return false
		}
		p := findPlugin(list.GetPlugins(), "hello")
		if p == nil || !p.GetInstalled() || p.GetState() != protocol.PluginWaiting {
			return false
		}
		var gotGreeting string
		var gotSecretSet bool
		for _, s := range p.GetSettings() {
			if s.GetKey() == "greeting" {
				gotGreeting = s.GetValue()
			}
			if s.GetKey() == "api_token" {
				gotSecretSet = s.GetSet()
			}
		}
		return gotGreeting == "annyeong" && !gotSecretSet
	})

	// Disable on A -> disabled on B.
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginDisable}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello disabled on B", func() bool {
		list, err := cb.Plugins(ctx)
		if err != nil {
			return false
		}
		p := findPlugin(list.GetPlugins(), "hello")
		return p != nil && p.GetState() == protocol.PluginDisabled
	})

	// Enable on A -> returns to Waiting (still ungranted) on B.
	if _, err := ca.PluginAction(ctx, &protocol.PluginAction{PluginId: "hello", Op: protocol.PluginEnable}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello re-enabled on B", func() bool {
		list, err := cb.Plugins(ctx)
		if err != nil {
			return false
		}
		p := findPlugin(list.GetPlugins(), "hello")
		return p != nil && p.GetState() == protocol.PluginWaiting
	})

	// Remove on A -> removed on B.
	if _, err := ca.InstallPlugin(ctx, &protocol.InstallPlugin{Op: protocol.RemoveOp, Id: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello removed on B", func() bool {
		list, err := cb.Plugins(ctx)
		return err == nil && findPlugin(list.GetPlugins(), "hello") == nil
	})

	// Non-peer caller must be refused.
	env, err := protocol.Encode(protocol.TypeSyncPluginConfig, "1", &protocol.SyncPluginConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, nil, env, false); err == nil || !strings.Contains(err.Error(), "mutually trusted peer") {
		t.Fatalf("non-peer sync call: got %v, want peer-only refusal", err)
	}
}

func writeConfigJSON(t *testing.T, path string, cfg *config.Config) {
	t.Helper()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAgentConfigSyncAcrossMesh verifies end-to-end P2P sync of agent
// definitions/profiles, MCP context servers, skill overrides, and prompt
// templates while strictly keeping node-local Env maps untouched.
func TestAgentConfigSyncAcrossMesh(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)

	cfgPathA := filepath.Join(a.dir, "config.json")
	cfgPathB := filepath.Join(b.dir, "config.json")

	// Node B starts with node-local secrets in Env on claude, profile fast, and MCP server localmcp.
	cfgB := cloneConfig(b.config())
	cfgB.Agents["claude"] = agentspec.Agent{
		Command: []string{"claude"},
		Env:     map[string]string{"ANTHROPIC_API_KEY": "b-local-secret"},
		Profiles: []agentspec.Profile{
			{Name: "fast", Env: map[string]string{"PROFILE_SECRET": "b-profile-secret"}},
		},
	}
	cfgB.ContextServers = map[string]mcp.Server{
		"localmcp": {
			Command: &mcp.Command{Path: "npx", Env: map[string]string{"MCP_SECRET": "b-mcp-secret"}},
		},
	}
	writeConfigJSON(t, cfgPathB, cfgB)
	b.FollowAgents(cfgPathB)
	// Ensure Node A's subsequent edits carry strictly later timestamps than Node B's initial state.
	time.Sleep(5 * time.Millisecond)

	// Node A configures custom agent, claude profile, MCP server, skill override, and prompt template.
	cfgA := cloneConfig(a.config())
	cfgA.Agents["claude"] = agentspec.Agent{
		Command: []string{"claude"},
		Args:    []string{"--verbose"},
		Yolo:    true,
		Env:     map[string]string{"ANTHROPIC_API_KEY": "a-secret-do-not-sync"},
		Profiles: []agentspec.Profile{
			{
				Name:        "fast",
				Description: "Quick model",
				Model:       "haiku",
				Env:         map[string]string{"PROFILE_SECRET": "a-profile-secret-do-not-sync"},
			},
		},
	}
	cfgA.Agents["mybot"] = agentspec.Agent{
		Command: []string{"mybot"},
		Args:    []string{"--run"},
	}
	on := true
	cfgA.ContextServers = map[string]mcp.Server{
		"localmcp": {
			Enabled: &on,
			Command: &mcp.Command{Path: "npx", Args: []string{"-y", "srv"}, Env: map[string]string{"MCP_SECRET": "a-mcp-secret-do-not-sync"}},
		},
	}
	writeConfigJSON(t, cfgPathA, cfgA)
	a.FollowAgents(cfgPathA)

	skillContent := "---\nname: mako-plugin\ndescription: Custom synced skill\n---\n# Synced skill body\n"
	skillPathA := filepath.Join(a.dir, "skills", "mako-plugin", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPathA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPathA, []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	tmplContent := "---\ndescription: Quick review\n---\nPlease review the diff.\n"
	tmplPathA := filepath.Join(a.dir, "templates", "review.md")
	if err := os.MkdirAll(filepath.Dir(tmplPathA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmplPathA, []byte(tmplContent), 0o644); err != nil {
		t.Fatal(err)
	}
	a.pokeStatus()

	// Wait for everything to converge on Node B.
	skillPathB := filepath.Join(b.dir, "skills", "mako-plugin", "SKILL.md")
	tmplPathB := filepath.Join(b.dir, "templates", "review.md")

	waitFor(t, "agent config, MCP server, skill, and template synced to B", func() bool {
		fb := b.followed()
		cl, ok := fb.agents["claude"]
		if !ok || !cl.Yolo || !slices.Equal(cl.Args, []string{"--verbose"}) {
			return false
		}
		if _, ok := fb.agents["mybot"]; !ok {
			return false
		}
		srv, ok := fb.servers["localmcp"]
		if !ok || srv.Command == nil || !slices.Equal(srv.Command.Args, []string{"-y", "srv"}) {
			return false
		}
		skData, err := os.ReadFile(skillPathB)
		if err != nil || string(skData) != skillContent {
			return false
		}
		tmData, err := os.ReadFile(tmplPathB)
		if err != nil || string(tmData) != tmplContent {
			return false
		}
		return true
	})

	// Verify Node B's local Env secrets were preserved and Node A's secrets never leaked.
	fb := b.followed()
	cl := fb.agents["claude"]
	if cl.Env["ANTHROPIC_API_KEY"] != "b-local-secret" {
		t.Fatalf("claude local env = %+v, want b-local-secret", cl.Env)
	}
	prof, ok := cl.Profile("fast")
	if !ok || prof.Model != "haiku" || prof.Env["PROFILE_SECRET"] != "b-profile-secret" {
		t.Fatalf("claude fast profile = %+v", prof)
	}
	if fb.servers["localmcp"].Command.Env["MCP_SECRET"] != "b-mcp-secret" {
		t.Fatalf("localmcp local env = %+v, want b-mcp-secret", fb.servers["localmcp"].Command.Env)
	}

	// Deleting template on A propagates deletion to B.
	if err := os.Remove(tmplPathA); err != nil {
		t.Fatal(err)
	}
	a.pokeStatus()
	waitFor(t, "deleted template removed on B", func() bool {
		_, err := os.Stat(tmplPathB)
		return os.IsNotExist(err)
	})

	// Non-peer caller must be refused.
	env, err := protocol.Encode(protocol.TypeSyncAgentConfig, "1", &protocol.SyncAgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), nil, env, false); err == nil || !strings.Contains(err.Error(), "mutually trusted peer") {
		t.Fatalf("non-peer sync call: got %v, want peer-only refusal", err)
	}
}

// TestSyncTogglesRespected verifies that turning off SyncPlugins or SyncAgents
// prevents replication even while Follow other nodes (Workspace.Sync) stays on.
func TestSyncTogglesRespected(t *testing.T) {
	testRegistries = true
	t.Cleanup(func() { testRegistries = false })

	off := false
	on := true
	a := meshDaemon(t, "a")
	b := meshDaemonWith(t, "b", func(c *config.Config) {
		c.Workspace.Sync = &on
		c.Workspace.SyncPlugins = &off
		c.Workspace.SyncAgents = &off
	}, p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)

	tmplPathA := filepath.Join(a.dir, "templates", "only-a.md")
	if err := os.MkdirAll(filepath.Dir(tmplPathA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmplPathA, []byte("# only on a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.captureLocalAgentConfig()
	a.pokeStatus()

	// Wait until mesh nodes see each other, then verify B never synced only-a.md.
	waitFor(t, "b linked with a", func() bool {
		for _, n := range b.nodes() {
			if n.GetId() == a.config().NodeID && n.GetTrusted() && n.GetTrustsUs() {
				return true
			}
		}
		return false
	})
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(b.dir, "templates", "only-a.md")); !os.IsNotExist(err) {
		t.Fatalf("expected template not synced while SyncAgents=false, err=%v", err)
	}
}
