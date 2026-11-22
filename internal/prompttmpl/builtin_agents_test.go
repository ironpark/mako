package prompttmpl_test

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// Built-in maker templates (//template and //plugin) are offered to the agents
// that get the mako MCP server, and to no other.
func TestMakerIsForAgentsWithMCP(t *testing.T) {
	var want []string
	for _, d := range agents.All() {
		if _, ok := d.(agent.Servers); ok {
			want = append(want, d.Info().Name)
		}
	}
	slices.Sort(want)
	for _, name := range []string{prompttmpl.MakerName, prompttmpl.PluginMakerName} {
		got := slices.Clone(prompttmpl.BuiltinTemplate(name).Agents)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("//%s is for %v; the agents with MCP servers are %v", name, got, want)
		}
	}
}
