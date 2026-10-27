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
