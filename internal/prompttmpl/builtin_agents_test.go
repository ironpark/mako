package prompttmpl_test

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// //template is offered to the agents that get the mako MCP server, whose
// save_template it asks them to call, and to no other.
func TestMakerIsForAgentsWithMCP(t *testing.T) {
	var want []string
	for _, d := range agents.All() {
		if _, ok := d.(agent.Servers); ok {
			want = append(want, d.Info().Name)
		}
	}
	got := slices.Clone(prompttmpl.BuiltinTemplate(prompttmpl.MakerName).Agents)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("//%s is for %v; the agents with MCP servers are %v", prompttmpl.MakerName, got, want)
	}
}
