package codex

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestServersAreOverrides(t *testing.T) {
	servers := []agent.Server{{ID: "my.server", Command: "node", Args: nil, Env: map[string]string{"B": "2", "A": `q"`}}}
	cmd, _, err := New().WithServers([]string{"codex", "exec"}, servers, t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex",
		"-c", `mcp_servers."my.server".command="node"`,
		"-c", `mcp_servers."my.server".args=[]`,
		"-c", `mcp_servers."my.server".env={"A"="q\"","B"="2"}`,
		"exec"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("command %q\nwant %q", cmd, want)
	}
}
