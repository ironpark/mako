package gui

import (
	"context"
	"log/slog"
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
