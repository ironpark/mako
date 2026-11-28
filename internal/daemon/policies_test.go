//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/policy"
	"github.com/ironpark/mako/internal/protocol"
)

// A front-end reads and replaces the daemon's rules, and reads the
// approvals they decided, over the daemon's socket: the daemon's own
// configuration directory, wherever the window runs.
func TestPoliciesOverSocket(t *testing.T) {
	d := meshDaemon(t, "pol")
	if err := policy.AppendAudit(d.dir, policy.AuditEntry{Time: 1700000000, Session: "s", Agent: "claude", Tool: "Bash", Detail: "ls", Action: policy.ActionAllow, RuleID: "r1"}); err != nil {
		t.Fatal(err)
	}
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got, err := c.Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetRules()) != 0 || len(got.GetDecisions()) != 1 || got.GetDecisions()[0].GetDetail() != "ls" || got.GetDecisions()[0].GetTime().GetSeconds() != 1700000000 {
		t.Fatalf("policies = %v", got)
	}

	rule := &protocol.PolicyRule{Name: "No force", Enabled: true, Action: "deny", Tool: "Bash", Pattern: "*--force*", Message: "Ask first", Agents: []string{"codex"}}
	got, err = c.SetPolicies(ctx, &protocol.SetPolicies{Rules: []*protocol.PolicyRule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetRules()) != 1 || got.GetRules()[0].GetId() == "" || got.GetRules()[0].GetMessage() != "Ask first" {
		t.Fatalf("saved = %v", got.GetRules())
	}
	saved, err := policy.LoadRules(d.dir)
	if err != nil || len(saved) != 1 || saved[0].Pattern != "*--force*" || saved[0].Agents[0] != "codex" {
		t.Fatalf("policies.json = %+v, %v", saved, err)
	}

	// A rule that does not validate is refused, and nothing changes.
	if _, err := c.SetPolicies(ctx, &protocol.SetPolicies{Rules: []*protocol.PolicyRule{{Enabled: true, Action: "allow"}}}); err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("an allow rule with no tool: err = %v", err)
	}
	if saved, _ := policy.LoadRules(d.dir); len(saved) != 1 || saved[0].Action != policy.ActionDeny {
		t.Fatalf("a refused save changed policies.json: %+v", saved)
	}
}

// An audit log that cannot be read leaves the rules to read and replace,
// and says why it is not shown; it is kept as it is.
func TestPoliciesStandABrokenAuditLog(t *testing.T) {
	d := meshDaemon(t, "polaudit")
	audit := filepath.Join(d.dir, "policies-audit.json")
	if err := os.WriteFile(audit, []byte(`[{"time":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := policy.SaveRules(d.dir, []policy.Rule{{ID: "r", Enabled: true, Action: policy.ActionDeny, Tool: "Bash"}}); err != nil {
		t.Fatal(err)
	}
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got, err := c.Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetRules()) != 1 || got.GetDecisionsError() == "" {
		t.Fatalf("policies beside a broken audit log = %v", got)
	}
	got, err = c.SetPolicies(ctx, &protocol.SetPolicies{})
	if err != nil {
		t.Fatalf("set_policies beside a broken audit log: %v", err)
	}
	if len(got.GetRules()) != 0 || got.GetDecisionsError() == "" {
		t.Fatalf("saved = %v", got)
	}
	if b, _ := os.ReadFile(audit); string(b) != `[{"time":` {
		t.Fatalf("the audit log now holds %q", b)
	}
}

func TestPoliciesAreRefusedToPeers(t *testing.T) {
	d := New(nil, nil)
	if err := d.handleListPolicies(nil, &protocol.Envelope{Id: "1"}, true); err != errPolicyPeer {
		t.Fatalf("list_policies from a peer: err = %v", err)
	}
	if err := d.handleSetPolicies(nil, &protocol.Envelope{Id: "1"}, true); err != errPolicyPeer {
		t.Fatalf("set_policies from a peer: err = %v", err)
	}
}
