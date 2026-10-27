package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestServersAreAConfigFile(t *testing.T) {
	dir := t.TempDir()
	servers := []agent.Server{{ID: "gh", Command: "/bin/gh-mcp", Args: []string{"stdio"}, Env: map[string]string{"TOKEN": "x"}}}
	cmd, cleanup, err := New().WithServers([]string{"/usr/local/bin/claude", "--verbose", "hello"}, servers, dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp-s1.json")
	want := []string{"/usr/local/bin/claude", "--mcp-config=" + path, "--verbose", "hello"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("command %q, want %q", cmd, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type, Command string
			Args          []string
			Env           map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if s := cfg.Servers["gh"]; s.Type != "stdio" || s.Command != "/bin/gh-mcp" || s.Env["TOKEN"] != "x" || !slices.Equal(s.Args, []string{"stdio"}) {
		t.Fatalf("config %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", fi.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s", path)
	}
}
