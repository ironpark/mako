package codex

import (
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
