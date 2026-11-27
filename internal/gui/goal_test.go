package gui

import (
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestGoalBannerDisplaysAndOpensEditDialog(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })

	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:    "s1",
		State: protocol.StateRunning,
		Agent: "codex",
		Goal: &protocol.GoalStatus{
			Source:         "codex",
			Status:         protocol.GoalActive,
			Text:           "ship unified goal status",
			ProviderStatus: "paused",
			CanEdit:        true,
			CanClear:       true,
		},
	}})

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Stack(widget.Column(goalBanner(m, "s1")).Stretch(), goalDialog(m)).Expand()
	}, ggui.Sz(700, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "Goal · Paused"); !ok {
		t.Fatal("expected Goal · Paused label in goalBanner")
	}
	if _, ok := sem.Find(ggui.RoleText, "ship unified goal status"); !ok {
		t.Fatal("expected goal objective text in goalBanner")
	}
	editBtn, ok := sem.Find(ggui.RoleButton, "Edit goal")
	if !ok {
		t.Fatal("expected Edit goal button")
	}
	if _, ok := sem.Find(ggui.RoleButton, "Clear goal"); !ok {
		t.Fatal("expected Clear goal button")
	}

	p.Click(editBtn.Rect.Center())
	p.Frame()
	if !ggui.Peek(m.GoalOpen) || ggui.Peek(m.GoalText) != "ship unified goal status" {
		t.Fatalf("goalDialog did not open prefilled: open=%v text=%q", ggui.Peek(m.GoalOpen), ggui.Peek(m.GoalText))
	}

	// Empty submission sets a validation error without closing.
	m.GoalText.Set("   ")
	m.submitGoal()
	if ggui.Peek(m.GoalErr) == "" || !ggui.Peek(m.GoalOpen) {
		t.Fatalf("expected empty objective error, open=%v err=%q", ggui.Peek(m.GoalOpen), ggui.Peek(m.GoalErr))
	}

	// Inactive goal hides the banner.
	m.GoalOpen.Set(false)
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:    "s1",
		State: protocol.StateRunning,
		Agent: "codex",
		Goal: &protocol.GoalStatus{
			Source: "codex",
			Status: protocol.GoalInactive,
			Reason: protocol.GoalReasonCleared,
		},
	}})
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "ship unified goal status"); ok {
		t.Fatal("cleared goal should not remain visible in goalBanner")
	}
}
