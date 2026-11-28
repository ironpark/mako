package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/policy"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// policyDaemon is a daemon whose configuration directory holds rules, and
// a session of agent in it.
func policyDaemon(t *testing.T, agent string, rules ...policy.Rule) (*Daemon, *session.Session) {
	t.Helper()
	d := New(&config.Config{NodeID: "node"}, nil)
	d.dir = t.TempDir()
	if err := policy.SaveRules(d.dir, rules); err != nil {
		t.Fatal(err)
	}
	s, err := session.Start(session.Spec{
		ID: "policy-" + agent, NodeID: "node", Agent: agent, Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	return d, s
}

// hook hands d one hook's input from s's agent, as its hook program does,
// and returns what it answered.
func hook(t *testing.T, d *Daemon, s *session.Session, seq int64, input string) hookReply {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serveHook(t.Context(), server)
	}()
	if err := json.NewEncoder(client).Encode(hookMessage{Session: s.ID(), Seq: seq, Input: json.RawMessage(input)}); err != nil {
		t.Fatal(err)
	}
	var reply hookReply
	_ = json.NewDecoder(client).Decode(&reply)
	_ = client.Close()
	<-done
	return reply
}

// permission is a PermissionRequest hook's input for tool called with input.
func permission(tool, input string) string {
	return `{"hook_event_name":"PermissionRequest","tool_name":"` + tool + `","tool_input":` + input + `}`
}

// The approval hook evaluates rules against the whole command, not the
// line the window shows of it.
func TestApprovalHookEvaluatesTheWholeCommand(t *testing.T) {
	d, s := policyDaemon(t, "claude", policy.Rule{ID: "git", Enabled: true, Action: policy.ActionAllow, Tool: "Bash", Pattern: "git status*"})

	if r := hook(t, d, s, 1, permission("Bash", `{"command":"git status"}`)); r.Decision != protocol.DecisionAllow {
		t.Fatalf("git status: decision %q, want allow", r.Decision)
	}
	if r := hook(t, d, s, 2, permission("Bash", `{"command":"git status\nrm -rf ~"}`)); r.Decision != "" {
		t.Fatalf("git status then rm: decision %q, want it left to the user", r.Decision)
	}
	audit, err := policy.LoadAudit(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].Detail != "git status" || audit[0].RuleID != "git" {
		t.Fatalf("audit = %+v, want the one allowed call", audit)
	}
}

// A question or a plan asked with no arguments still reads as its kind,
// which rules never decide for the user.
func TestApprovalHookNeverDecidesAQuestionWithNoInput(t *testing.T) {
	d, s := policyDaemon(t, "claude", policy.Rule{ID: "all", Enabled: true, Action: policy.ActionAllow, Tool: "*"})
	for i, tool := range []string{"AskUserQuestion", "ExitPlanMode"} {
		for j, in := range []string{`{"hook_event_name":"PermissionRequest","tool_name":"` + tool + `"}`, permission(tool, "null")} {
			if r := hook(t, d, s, int64(10*i+j+1), in); r.Decision != "" {
				t.Fatalf("%s %s: decision %q, want it left to the user", tool, in, r.Decision)
			}
		}
	}
	if r := hook(t, d, s, 100, permission("Bash", `{"command":"ls"}`)); r.Decision != protocol.DecisionAllow {
		t.Fatalf("Bash under allow *: decision %q, want allow", r.Decision)
	}
}

// A rule for Edit holds for another agent's tool of the kind, OpenCode's
// apply_patch here.
func TestApprovalHookMatchesRulesByKind(t *testing.T) {
	d, s := policyDaemon(t, "opencode", policy.Rule{ID: "env", Enabled: true, Action: policy.ActionDeny, Tool: "Edit", Pattern: "*.env*", Message: "no .env"})
	r := hook(t, d, s, 1, permission("apply_patch", `{"patchText":"*** Begin Patch\n*** Update File: src/a.go\n*** Update File: .env\n*** End Patch"}`))
	if r.Decision != protocol.DecisionDeny || r.Message != "no .env" {
		t.Fatalf("apply_patch to .env: %+v, want denied by the Edit rule", r)
	}
}

// A decided approval ends the wait the agent reported before it.
func TestApprovalHookDecisionEndsTheWait(t *testing.T) {
	d, s := policyDaemon(t, "claude", policy.Rule{ID: "ls", Enabled: true, Action: policy.ActionAllow, Tool: "Bash", Pattern: "ls*"})
	s.ReportStatus(protocol.AgentWorking, 1)
	s.ReportToolStatus(protocol.AgentWaiting, 2)
	if got := s.Info().GetStatus(); got != protocol.AgentWaiting {
		t.Fatalf("status = %s, want waiting", got)
	}
	if r := hook(t, d, s, 3, permission("Bash", `{"command":"ls -la"}`)); r.Decision != protocol.DecisionAllow {
		t.Fatalf("decision %q, want allow", r.Decision)
	}
	if got := s.Info().GetStatus(); got != protocol.AgentWorking {
		t.Fatalf("status after the decision = %s, want working", got)
	}

	// An approval the window holds for another call stands.
	if _, ok := s.BeginApproval(3, &protocol.Approval{Id: "held", Tool: "Bash"}); !ok {
		t.Fatal("BeginApproval refused")
	}
	if r := hook(t, d, s, 4, permission("Bash", `{"command":"ls"}`)); r.Decision != protocol.DecisionAllow {
		t.Fatalf("decision %q, want allow", r.Decision)
	}
	if got := s.Info().GetApproval().GetId(); got != "held" {
		t.Fatalf("held approval = %q, want it kept", got)
	}
}

// The approval hook reads a call's paths against its session's project:
// an absolute path in it reads as its relative one, and one that climbs
// out of it with ../ does not.
func TestApprovalHookReadsPathsInTheProject(t *testing.T) {
	d, s := policyDaemon(t, "claude", policy.Rule{ID: "src", Enabled: true, Action: policy.ActionAllow, Tool: "Edit", Pattern: "src/*"})
	in := func(path string) string {
		b, _ := json.Marshal(map[string]string{"file_path": path, "old_string": "a", "new_string": "b"})
		return string(b)
	}
	if r := hook(t, d, s, 1, permission("Edit", in(filepath.Join(s.Info().GetProject(), "src", "a.go")))); r.Decision != protocol.DecisionAllow {
		t.Fatalf("an edit of src/a.go by its absolute path: decision %q, want allow", r.Decision)
	}
	if r := hook(t, d, s, 2, permission("Edit", in("src/../../.bashrc"))); r.Decision != "" {
		t.Fatalf("an edit out of the project: decision %q, want it left to the user", r.Decision)
	}
}

// Rules the daemon cannot read allow no call: it is left to the user, and
// a repository's deny rule still denies.
func TestApprovalHookFailsClosedOnUnreadableRules(t *testing.T) {
	d, s := policyDaemon(t, "claude")
	if err := os.WriteFile(filepath.Join(d.dir, "policies.json"), []byte(`[{"id":"all","enabled":true,"action":"allow","tool":"*"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := hook(t, d, s, 1, permission("Bash", `{"command":"ls"}`)); r.Decision != "" {
		t.Fatalf("ls under a broken policies.json: decision %q, want it left to the user", r.Decision)
	}
	proj := s.Info().GetProject()
	if err := os.MkdirAll(filepath.Join(proj, ".mako"), 0o755); err != nil {
		t.Fatal(err)
	}
	deny := "[[rule]]\nid = \"rm\"\nenabled = true\naction = \"deny\"\ntool = \"Bash\"\npattern = \"rm *\"\n"
	if err := os.WriteFile(filepath.Join(proj, ".mako", "policy.toml"), []byte(deny), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := hook(t, d, s, 2, permission("Bash", `{"command":"rm -rf ~"}`)); r.Decision != protocol.DecisionDeny {
		t.Fatalf("rm under a project deny rule: decision %q, want deny", r.Decision)
	}
}
