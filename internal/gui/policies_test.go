package gui

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/policy"
	"github.com/ironpark/mako/internal/protocol"
)

// Settings > Policies reads and saves the rules of the daemon the window
// talks through, in that daemon's configuration directory, and a deny rule
// takes the message it tells the agent.
func TestPolicySettingsGoThroughTheDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MAKO_HOME", home)
	if err := policy.AppendAudit(home, policy.AuditEntry{Session: "s", Agent: "claude", Tool: "Bash", Detail: "ls -la", Action: policy.ActionAllow, RuleID: "r"}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.P2P.Disabled = true
	cfg.Discovery = config.Discovery{}
	d := daemon.New(cfg, slog.New(slog.DiscardHandler))
	dctx, dcancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(dctx) }()
	t.Cleanup(func() {
		dcancel()
		<-done
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not become ready")
	}

	// The window's own configuration directory is another: what it shows
	// and saves is the daemon's.
	t.Setenv("MAKO_HOME", t.TempDir())

	m := newModel(cfg, d.Socket())
	m.link.setEmbedded(d.Socket())
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(policySettings(m)) }, ggui.Sz(900, 1000))
	defer p.Close()
	m.post = p.Post
	p.Frame()
	// Opened before the window reached its daemon, the page waits for it,
	// and reads the policies once it does.
	if _, ok := p.Semantics().Find(ggui.RoleText, T("Connect to this node's daemon to manage its policies.")); !ok {
		t.Fatalf("the page does not wait for the daemon:\n%s", p.Semantics().String())
	}
	m.Conn.Set(connState{OK: true, Self: d.Self()})
	probeUntil(t, p, "the daemon's policies", func() bool { return ggui.Peek(m.policies.Info) != nil })
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "claude · Bash ls -la"); !ok {
		t.Fatalf("the daemon's audit log is not shown:\n%s", p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, T("Deny message")); ok {
		t.Fatal("an allow rule offers a deny message")
	}

	p.Tap(T("Action"))
	p.Frame()
	p.Tap(T("Deny"))
	p.Frame()
	typeInto(t, p, T("Pattern"), "*--force*")
	typeInto(t, p, T("Deny message"), "Ask before forcing")
	p.Tap(T("Add rule"))
	probeUntil(t, p, "the rule saved", func() bool { return len(ggui.Peek(m.policies.Info).GetRules()) == 1 })

	rules, err := policy.LoadRules(home)
	if err != nil || len(rules) != 1 {
		t.Fatalf("the daemon's policies.json holds %+v, %v", rules, err)
	}
	if r := rules[0]; r.Action != policy.ActionDeny || r.Tool != "Bash" || r.Pattern != "*--force*" || r.Message != "Ask before forcing" {
		t.Fatalf("saved %+v, want the deny rule with its message", r)
	}
	if got := ggui.Peek(m.policies.Info).GetRules()[0]; got.GetAction() != protocol.DecisionDeny {
		t.Fatalf("shown %v", got)
	}
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("Tells the agent: %s", "Ask before forcing")); !ok {
		t.Fatalf("the deny rule's message is not shown:\n%s", p.Semantics().String())
	}

	// A rule the policy refuses is said in the window's language.
	p.Tap(T("Action"))
	p.Frame()
	p.Tap(T("Allow"))
	p.Frame()
	field, _ := p.Semantics().Find(ggui.RoleTextField, T("Tool"))
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	p.Key("cmd+a", "backspace")
	p.Frame()
	p.Tap(T("Add rule"))
	p.Frame()
	if got := ggui.Peek(m.policies.Note); got != T("Name the rule's tool, or * for any tool.") {
		t.Fatalf("note = %q", got)
	}

	// Told it talks to another node, the page drops this one's rules, and
	// asks no daemon but that node's for them.
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "elsewhere", Revision: protocol.Revision}})
	p.Frame()
	if ggui.Peek(m.policies.Info) != nil {
		t.Fatal("another node's page shows this one's rules")
	}
	probeUntil(t, p, "the request refused", func() bool { return !ggui.Peek(m.policies.Loading) })
	if ggui.Peek(m.policies.Info) != nil || ggui.Peek(m.policies.Note) == "" {
		t.Fatalf("this daemon's rules were read for another node: info %v, note %q", ggui.Peek(m.policies.Info), ggui.Peek(m.policies.Note))
	}
}

// The page follows the node the window talks to: another node's policies
// are dropped at once, and so is every reply still to come for a request
// made before; no change is saved until the rules it is made to were
// read, nor while another save is in flight.
func TestPolicySettingsFollowTheNode(t *testing.T) {
	m := newModel(config.Default(), "")
	// No daemon answers: each request fails, at once.
	m.link.setEmbedded(filepath.Join(t.TempDir(), "none.sock"))
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })
	var mu sync.Mutex
	var posted []func()
	m.post = func(f func()) {
		mu.Lock()
		defer mu.Unlock()
		posted = append(posted, f)
	}
	node := func(id string, rev int64) connState {
		return connState{OK: true, Self: &protocol.Node{Id: id, Revision: rev}}
	}
	st := m.policies
	requests := 0
	asked := func() {
		t.Helper()
		if !ggui.Peek(st.Loading) && !ggui.Peek(st.Busy) {
			t.Fatal("no request went out")
		}
		requests++
	}

	m.followPolicies(node("a", protocol.Revision))
	asked()
	st.Info.Set(&protocol.Policies{Rules: []*protocol.PolicyRule{{Id: "of-a", Enabled: true, Action: "allow", Tool: "Bash"}}})

	m.followPolicies(node("b", protocol.Revision))
	asked()
	if ggui.Peek(st.Info) != nil || st.node != "b" {
		t.Fatalf("after the switch to b: info %v, node %q", ggui.Peek(st.Info), st.node)
	}
	// Before b's rules are read, no change is saved: one made to a's
	// rules would replace b's with them.
	before := st.gen
	m.savePolicyRules([]policy.Rule{{ID: "of-a", Action: policy.ActionAllow, Tool: "Bash"}}, nil)
	if st.gen != before {
		t.Fatal("a save went out before the node's rules were read")
	}

	// While one save is in flight, no other request is made.
	st.Info.Set(&protocol.Policies{})
	m.savePolicyRules(nil, nil)
	asked()
	inFlight := st.gen
	m.savePolicyRules([]policy.Rule{{Enabled: true, Action: policy.ActionDeny, Tool: "Bash"}}, nil)
	m.loadPolicies()
	if st.gen != inFlight {
		t.Fatal("a request went out while a save was in flight")
	}

	m.followPolicies(node("a", protocol.Revision))
	asked()
	// With no daemon, the page shows no node's, and every reply to come
	// is for a request made before: none changes the page.
	m.followPolicies(connState{})
	if st.node != "" || ggui.Peek(st.Info) != nil || ggui.Peek(st.Loading) || ggui.Peek(st.Busy) {
		t.Fatal("with no daemon, the page still shows a node's")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		n := len(posted)
		mu.Unlock()
		if n == requests {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d replies of %d came", n, requests)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, f := range posted {
		f()
	}
	if note := ggui.Peek(st.Note); note != "" || ggui.Peek(st.Info) != nil || ggui.Peek(st.Busy) {
		t.Fatalf("a reply to a request made before changed the page: note %q", note)
	}

	// A daemon too old to keep policies is not asked.
	m.followPolicies(node("old", protocol.RevisionPolicies-1))
	if !ggui.Peek(st.Old) || ggui.Peek(st.Loading) {
		t.Fatalf("an old daemon: old %v, loading %v", ggui.Peek(st.Old), ggui.Peek(st.Loading))
	}
}

// typeInto types text into the text field called name.
func typeInto(t *testing.T, p *ggui.Probe, name, text string) {
	t.Helper()
	field, ok := p.Semantics().Find(ggui.RoleTextField, name)
	if !ok {
		t.Fatalf("no %q field:\n%s", name, p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	p.Text(text)
	p.Frame()
}
