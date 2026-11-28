package codex

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A turn's context says the model it ran with, its effort, and what it
// may do unasked, as the permission modes the GUI names; the latest turn's
// holds.
func TestATurnSaysWhatItRanWith(t *testing.T) {
	turn := func(model, effort, approval, sandbox, mode string) []byte {
		return []byte(`{"type":"turn_context","payload":{"model":"` + model + `","effort":"` + effort +
			`","approval_policy":"` + approval + `","sandbox_policy":{"type":"` + sandbox +
			`"},"collaboration_mode":{"mode":"` + mode + `","settings":{"reasoning_effort":"low"}}}}`)
	}
	for _, c := range []struct {
		line                []byte
		model, effort, mode string
	}{
		{turn("gpt-6-astra", "high", "never", "danger-full-access", "default"), "gpt-6-astra", "high", "bypassPermissions"},
		{turn("gpt-5.5", "", "on-request", "workspace-write", "default"), "gpt-5.5", "low", "acceptEdits"},
		{turn("gpt-5.5", "medium", "on-request", "read-only", "default"), "gpt-5.5", "medium", "readOnly"},
		{turn("gpt-5.5", "medium", "on-request", "danger-full-access", "default"), "gpt-5.5", "medium", "default"},
		{turn("gpt-5.5", "medium", "never", "danger-full-access", "plan"), "gpt-5.5", "medium", "plan"},
	} {
		var u protocol.Usage
		count := (&Agent{}).NewUsage()
		count.Count([]byte(`{"type":"turn_context","payload":{"model":"older","effort":"xhigh"}}`), &u)
		count.Count(c.line, &u)
		if u.GetModel() != c.model || u.GetEffort() != c.effort || u.GetPermissionMode() != c.mode {
			t.Errorf("%s: model %q effort %q mode %q", c.line, u.GetModel(), u.GetEffort(), u.GetPermissionMode())
		}
	}
}

func TestTurnContextParsesServiceTierFastMode(t *testing.T) {
	var u protocol.Usage
	count := (&Agent{}).NewUsage()

	// Without service_tier, FastMode stays unset (nil).
	count.Count([]byte(`{"type":"turn_context","payload":{"model":"gpt-5.4","effort":"high"}}`), &u)
	if u.FastMode != nil {
		t.Fatalf("expected FastMode nil when service_tier omitted, got %v", *u.FastMode)
	}

	// Canonical "priority" sets FastMode=true.
	count.Count([]byte(`{"type":"turn_context","payload":{"model":"gpt-5.4","service_tier":"priority"}}`), &u)
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("expected FastMode=true for priority, got %v", u.FastMode)
	}

	// Later turn_context without service_tier preserves prior FastMode.
	count.Count([]byte(`{"type":"turn_context","payload":{"model":"gpt-5.4","effort":"medium"}}`), &u)
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("expected FastMode=true preserved when omitted, got %v", u.FastMode)
	}

	// "default" flips FastMode=false.
	count.Count([]byte(`{"type":"turn_context","payload":{"model":"gpt-5.4","service_tier":"default"}}`), &u)
	if u.FastMode == nil || u.GetFastMode() {
		t.Fatalf("expected FastMode=false for default, got %v", u.FastMode)
	}

	// "fast" flips FastMode=true again.
	count.Count([]byte(`{"type":"turn_context","payload":{"model":"gpt-5.4","service_tier":"fast"}}`), &u)
	if u.FastMode == nil || !u.GetFastMode() {
		t.Fatalf("expected FastMode=true for fast, got %v", u.FastMode)
	}
}
