package daemon

import (
	"os"
	"path/filepath"
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
	write(`{"agents": {"claude": ["claude"]}}`)
	d := New(&config.Config{NodeID: "n1", Agents: map[string]agentspec.Agent{"claude": {Command: []string{"claude"}}}}, nil)
	d.FollowAgents(path)

	later := time.Now().Add(time.Second)
	write(`{"agents": {"claude": {"command": ["claude"], "profiles": [{"name": "opus", "args": ["--model", "opus"]}]}}}`)
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
