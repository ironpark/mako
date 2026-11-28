package codex

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestServersAreOverrides(t *testing.T) {
	servers := []agent.Server{{ID: "my.server", Command: "node", Args: nil, Env: map[string]string{"B": "2", "A": `q"`}}}
	l := agent.Launch{Command: []string{"codex", "exec"}}
	if _, err := New().WithServers(&l, servers, t.TempDir(), "s1"); err != nil {
		t.Fatal(err)
	}
	cmd := l.Command
	want := []string{"codex",
		"-c", `mcp_servers."my.server".command="node"`,
		"-c", `mcp_servers."my.server".args=[]`,
		"-c", `mcp_servers."my.server".env={"A"="q\"","B"="2"}`,
		"exec"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("command %q\nwant %q", cmd, want)
	}
}

// A server at a URL is reached over HTTP, its token in Codex's environment
// and never on its command line, which any process may list.
func TestServerAtAURLKeepsItsTokenOffTheCommandLine(t *testing.T) {
	servers := []agent.Server{{ID: "mako", Command: "bridge", URL: "http://127.0.0.1:9/mcp", BearerToken: "secret"}}
	l := agent.Launch{Command: []string{"codex"}}
	if _, err := New().WithServers(&l, servers, t.TempDir(), "s1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"codex",
		"-c", `mcp_servers.mako.url="http://127.0.0.1:9/mcp"`,
		"-c", `mcp_servers.mako.bearer_token_env_var="MAKO_MCP_TOKEN_MAKO"`}
	if !slices.Equal(l.Command, want) {
		t.Fatalf("command %q\nwant %q", l.Command, want)
	}
	if l.Env["MAKO_MCP_TOKEN_MAKO"] != "secret" {
		t.Fatalf("environment %v lacks the token", l.Env)
	}
}

func TestLoopbackHTTPServerSetsProxyBypass(t *testing.T) {
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "corp.local,127.0.0.1")

	l := agent.Launch{Command: []string{"codex"}}
	if _, err := New().WithServers(&l, []agent.Server{{ID: "mako", URL: "http://127.0.0.1:9/mcp", BearerToken: "secret"}}, t.TempDir(), "s1"); err != nil {
		t.Fatal(err)
	}
	want := "corp.local,127.0.0.1,localhost,::1"
	if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
		t.Fatalf("proxy bypass env = %v, want %q in both NO_PROXY and no_proxy", l.Env, want)
	}

	// Stdio-only and remote HTTPS servers do not touch NO_PROXY/no_proxy.
	l2 := agent.Launch{Command: []string{"codex"}}
	if _, err := New().WithServers(&l2, []agent.Server{
		{ID: "local", Command: "node"},
		{ID: "remote", URL: "https://mcp.example.com/mcp"},
	}, t.TempDir(), "s2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := l2.Env["NO_PROXY"]; ok {
		t.Fatalf("non-loopback servers unexpectedly set NO_PROXY: %v", l2.Env)
	}
	if _, ok := l2.Env["no_proxy"]; ok {
		t.Fatalf("non-loopback servers unexpectedly set no_proxy: %v", l2.Env)
	}
}

func TestSplitRemoteLaunch(t *testing.T) {
	cmd := []string{
		"/usr/local/bin/codex",
		"-c", `mcp_servers.mako.url="http://127.0.0.1:9/mcp"`,
		"-c", `hooks.Stop=[{hooks=[{type="command",command="mako hook",timeout=5}]}]`,
		"--enable", "hooks",
		"--disable", "foo",
		"--strict-config",
		"--model", "gpt-6.2-sol",
		"-c", `model_reasoning_effort="high"`,
		"--dangerously-bypass-approvals-and-sandbox",
		"-C", "/work/proj",
		"--add-dir", "/work/shared-a",
		"--add-dir=/work/shared-b",
		"--no-daemon",
		"fix the tests",
	}
	got := SplitRemoteLaunch(cmd)
	if got.SkipRemote {
		t.Fatal("unexpected SkipRemote")
	}
	if got.Program != "/usr/local/bin/codex" {
		t.Fatalf("Program = %q", got.Program)
	}
	wantServer := []string{
		"-c", `mcp_servers.mako.url="http://127.0.0.1:9/mcp"`,
		"-c", `hooks.Stop=[{hooks=[{type="command",command="mako hook",timeout=5}]}]`,
		"--enable", "hooks",
		"--disable", "foo",
		"--strict-config",
		"-c", `model_reasoning_effort="high"`,
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="danger-full-access"`,
		"-c", `sandbox_workspace_write.writable_roots=["/work/shared-a","/work/shared-b"]`,
	}
	if !slices.Equal(got.ServerArgs, wantServer) {
		t.Fatalf("ServerArgs = %q\nwant %q", got.ServerArgs, wantServer)
	}
	// Permission overrides must stay off TUIArgs because `codex --remote ... resume` rejects them,
	// while NewTUIArgs carries them for brand-new `codex --remote` sessions so `thread/start`
	// does not default `sandbox` to "read-only".
	wantTUI := []string{
		"--model", "gpt-6.2-sol",
		"-C", "/work/proj",
	}
	if !slices.Equal(got.TUIArgs, wantTUI) {
		t.Fatalf("TUIArgs = %q\nwant %q", got.TUIArgs, wantTUI)
	}
	wantNewTUI := []string{"--dangerously-bypass-approvals-and-sandbox"}
	if !slices.Equal(got.NewTUIArgs, wantNewTUI) {
		t.Fatalf("NewTUIArgs = %q\nwant %q", got.NewTUIArgs, wantNewTUI)
	}
	if got.Model != "gpt-6.2-sol" || got.ApprovalPolicy != "never" || got.Sandbox != "danger-full-access" || got.Cwd != "/work/proj" {
		t.Fatalf("extracted settings = %+v", got)
	}
	if !got.HasPrompt || got.Prompt != "fix the tests" {
		t.Fatalf("prompt = (%v, %q)", got.HasPrompt, got.Prompt)
	}

	// Resumed launch preserves thread ID and strips resume + permission flags from TUIArgs.
	resumed := SplitRemoteLaunch([]string{
		"codex", "-c", `model_reasoning_effort="low"`, "resume", "01a1fa9c-4bba-78b2-8feb-b560a85e0978",
		"-a", "on-request", "-s", "workspace-write", "--no-daemon",
	})
	if resumed.SkipRemote || resumed.ResumeID != "01a1fa9c-4bba-78b2-8feb-b560a85e0978" {
		t.Fatalf("resumed = %+v", resumed)
	}
	if resumed.ApprovalPolicy != "on-request" || resumed.Sandbox != "workspace-write" {
		t.Fatalf("resumed policies = %+v", resumed)
	}
	if len(resumed.TUIArgs) != 0 {
		t.Fatalf("resumed TUIArgs = %q, want empty", resumed.TUIArgs)
	}
	if !slices.Equal(resumed.NewTUIArgs, []string{"-a", "on-request", "-s", "workspace-write"}) {
		t.Fatalf("resumed NewTUIArgs = %q", resumed.NewTUIArgs)
	}
	wantResumedServer := []string{
		"-c", `model_reasoning_effort="low"`,
		"-c", `approval_policy="on-request"`,
		"-c", `sandbox_mode="workspace-write"`,
	}
	if !slices.Equal(resumed.ServerArgs, wantResumedServer) {
		t.Fatalf("resumed ServerArgs = %q\nwant %q", resumed.ServerArgs, wantResumedServer)
	}

	// --approve-for-me moves to app-server config (guardian_subagent + workspace-write),
	// stays off TUIArgs for resume, and propagates via NewTUIArgs for new sessions.
	afm := SplitRemoteLaunch([]string{"codex", "--approve-for-me", "--", "-fix-me"})
	if afm.SkipRemote || afm.Sandbox != "workspace-write" || !afm.HasPrompt || afm.Prompt != "-fix-me" {
		t.Fatalf("approve-for-me + dash prompt = %+v", afm)
	}
	if len(afm.TUIArgs) != 0 {
		t.Fatalf("approve-for-me TUIArgs = %q, want empty", afm.TUIArgs)
	}
	if !slices.Equal(afm.NewTUIArgs, []string{"--approve-for-me"}) {
		t.Fatalf("approve-for-me NewTUIArgs = %q", afm.NewTUIArgs)
	}
	wantAfmServer := []string{
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `approvals_reviewer="guardian_subagent"`,
	}
	if !slices.Equal(afm.ServerArgs, wantAfmServer) {
		t.Fatalf("approve-for-me ServerArgs = %q\nwant %q", afm.ServerArgs, wantAfmServer)
	}

	// --full-auto translates to -a/-s in NewTUIArgs (newer Codex CLIs removed --full-auto),
	// and a later --yolo overrides an earlier --approve-for-me cleanly.
	fa := SplitRemoteLaunch([]string{"codex", "--full-auto"})
	if !slices.Equal(fa.NewTUIArgs, []string{"-a", "on-request", "-s", "workspace-write"}) {
		t.Fatalf("full-auto NewTUIArgs = %q", fa.NewTUIArgs)
	}
	override := SplitRemoteLaunch([]string{"codex", "--approve-for-me", "--yolo"})
	if !slices.Equal(override.NewTUIArgs, []string{"--dangerously-bypass-approvals-and-sandbox"}) {
		t.Fatalf("approve-for-me + yolo NewTUIArgs = %q", override.NewTUIArgs)
	}

	// Non-TUI subcommands, pickers, worktree, help/version, and explicit --remote skip managed app-server.
	for _, skip := range [][]string{
		{"codex", "exec", "hello"},
		{"codex", "resume"},
		{"codex", "resume", "--last"},
		{"codex", "--remote", "ws://127.0.0.1:9999"},
		{"codex", "--fork"},
		{"codex", "--worktree"},
		{"codex", "-h"},
		{"codex", "--help"},
		{"codex", "-V"},
		{"codex", "--version"},
	} {
		if s := SplitRemoteLaunch(skip); !s.SkipRemote {
			t.Fatalf("expected SkipRemote for %q, got %+v", skip, s)
		}
	}
}

func TestCodexWithFastAndSplitRemoteLaunchServiceTier(t *testing.T) {
	c := New()
	l := agent.Launch{Command: []string{"codex", "--model", "gpt-5.4"}}
	c.WithFast(&l, true)
	split := SplitRemoteLaunch(l.Command)
	if split.ServiceTier != "fast" {
		t.Fatalf("SplitRemoteLaunch ServiceTier = %q, want fast", split.ServiceTier)
	}
	if !slices.Contains(split.ServerArgs, `service_tier="fast"`) {
		t.Fatalf("ServerArgs missing service_tier override: %q", split.ServerArgs)
	}

	lOff := agent.Launch{Command: []string{"codex"}}
	c.WithFast(&lOff, false)
	if splitOff := SplitRemoteLaunch(lOff.Command); splitOff.ServiceTier != "default" {
		t.Fatalf("SplitRemoteLaunch ServiceTier(off) = %q, want default", splitOff.ServiceTier)
	}
}

func TestWithoutOwnSandbox(t *testing.T) {
	// Codex takes -s once: one the command has is changed, not added to.
	in := []string{"codex", "-a", "on-request", "-s", "read-only", "resume", "abc", "--", "-s", "prompt"}
	got := WithoutOwnSandboxTopLevel(in)
	want := []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "-a", "on-request", "-s", FullAccess, "resume", "abc", "--", "-s", "prompt"}
	if !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandboxTopLevel = %q\nwant %q", got, want)
	}
	got = WithoutOwnSandboxTopLevel([]string{"codex", "--sandbox=workspace-write", "--", "-s"})
	if want := []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "--sandbox=" + FullAccess, "--", "-s"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandboxTopLevel = %q\nwant %q", got, want)
	}
	got = WithoutOwnSandbox([]string{"codex", "-c", `sandbox_mode="read-only"`, "-c", "model=x"})
	if want := []string{"codex", "-c", `sandbox_mode="danger-full-access"`, "-c", `sandbox_mode="danger-full-access"`, "-c", "model=x"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandbox = %q\nwant %q", got, want)
	}
	got = WithoutOwnSandboxTopLevel([]string{"codex", "--", "-s"})
	if want := []string{"codex", "-s", FullAccess, "-c", `sandbox_mode="danger-full-access"`, "--", "-s"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandboxTopLevel = %q\nwant %q", got, want)
	}
	split := SplitRemoteLaunch(got)
	if split.Sandbox != FullAccess || !slices.Contains(split.NewTUIArgs, FullAccess) {
		t.Fatalf("split = %+v", split)
	}
	if !slices.Contains(split.ServerArgs, `sandbox_mode="danger-full-access"`) {
		t.Fatalf("server args = %q", split.ServerArgs)
	}
	approve := SplitRemoteLaunch(WithoutOwnSandboxTopLevel([]string{"codex", "--approve-for-me"}))
	if approve.Sandbox != FullAccess {
		t.Fatalf("--approve-for-me took back Codex's own sandbox: %+v", approve)
	}
	if got := WithoutOwnSandbox([]string{"codex"}); !slices.Equal(got, []string{"codex", "-c", `sandbox_mode="danger-full-access"`}) {
		t.Fatalf("WithoutOwnSandbox = %q", got)
	}
	// Each form Codex takes an option in.
	full := `sandbox_mode="danger-full-access"`
	got = WithoutOwnSandbox([]string{"codex", `--config=sandbox_mode="read-only"`, `-c=sandbox_mode="read-only"`, `-csandbox_mode="read-only"`, "-sread-only", "-s=read-only", "-cmodel=x", "--config", "model=y"})
	if want := []string{"codex", "-c", full, "--config=" + full, "-c=" + full, "-c" + full, "-s" + FullAccess, "-s=" + FullAccess, "-cmodel=x", "--config", "model=y"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandbox = %q\nwant %q", got, want)
	}
	if got := WithoutOwnSandboxTopLevel([]string{"codex", "-sread-only"}); slices.Contains(got, "-s") {
		t.Fatalf("WithoutOwnSandboxTopLevel added a second -s: %q", got)
	}
	// codex exec, which --approve-for-me would give Codex's own sandbox
	// back without a top-level -s.
	got = WithoutOwnSandboxTopLevel([]string{"codex", "--approve-for-me"})
	if want := []string{"codex", "-s", FullAccess, "-c", full, "--approve-for-me"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutOwnSandboxTopLevel = %q\nwant %q", got, want)
	}
}

func TestOneShotReadsNoLinkOutOfItsFolder(t *testing.T) {
	temp, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &agent.Launch{Command: []string{"codex"}}
	prep, err := New().PrepareOneShot(l, agent.OneShotRequest{Prompt: "hi"}, "", temp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(temp, "codex-out.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := prep.Parse([]byte("stdout"), nil)
	if err != nil || got != "stdout" {
		t.Fatalf("Parse = %q, %v; want the run's own output, not the linked file", got, err)
	}
}
