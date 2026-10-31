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
	l := agent.Launch{Command: []string{"/usr/local/bin/claude", "--verbose", "hello"}}
	cleanup, err := New().WithServers(&l, servers, dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := l.Command
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

// A server at a URL is given as one Claude Code reaches over HTTP, with
// its token as a header, and spares the process its command would run.
func TestServerAtAURLIsReachedOverHTTP(t *testing.T) {
	dir := t.TempDir()
	servers := []agent.Server{{ID: "mako", Command: "bridge", URL: "http://127.0.0.1:9/mcp", BearerToken: "secret"}}
	l := agent.Launch{Command: []string{"claude"}}
	if _, err := New().WithServers(&l, servers, dir, "s1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "mcp-s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Servers["mako"]
	headers, _ := got["headers"].(map[string]any)
	if got["type"] != "http" || got["url"] != "http://127.0.0.1:9/mcp" || headers["Authorization"] != "Bearer secret" || got["command"] != nil {
		t.Fatalf("server %v", got)
	}
}
