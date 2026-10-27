package gui

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
)

func TestProfileServersRoundTrip(t *testing.T) {
	none, some := []string{}, []string{"a", "b"}
	agents := map[string]agentspec.Agent{"claude": {Profiles: []agentspec.Profile{
		{Name: "all"}, {Name: "none", ContextServers: &none}, {Name: "some", ContextServers: &some},
	}}}
	got, err := applyAgents(draftAgents(agents))
	if err != nil {
		t.Fatal(err)
	}
	// Saving and reopening must preserve an explicitly empty selection.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	ps := got["claude"].Profiles
	if len(ps) != 3 || ps[0].ContextServers != nil || ps[1].ContextServers == nil || len(*ps[1].ContextServers) != 0 ||
		ps[2].ContextServers == nil || !slices.Equal(*ps[2].ContextServers, some) {
		t.Fatalf("profiles %+v", ps)
	}
}

func TestSessionTitleUsesCustomName(t *testing.T) {
	s := &protocol.SessionInfo{Agent: "claude", Profile: "work", Name: "API 작업"}
	if got := sessionTitle(s, "program title"); got != s.GetName() {
		t.Fatalf("custom title = %q", got)
	}
	s.Name = ""
	if got := sessionTitle(s, "program title"); got != "program title" {
		t.Fatalf("cleared title = %q", got)
	}
	if got := sessionTitle(s, ""); got != "claude · work" {
		t.Fatalf("fallback title = %q", got)
	}
}
