package claude

import (
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// The usage says what the latest reply was made with and what the agent
// may do unasked, as of the latest prompt; a subagent's reply says
// nothing of either, and each reply's tokens count once.
func TestUsageSaysTheModelAndThePermissionMode(t *testing.T) {
	lines := []string{
		`{"type":"user","permissionMode":"default","message":{"content":"hi"}}`,
		`{"type":"assistant","effort":"medium","message":{"id":"m1","model":"claude-sonnet-5-5","usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","permissionMode":"bypassPermissions","message":{"content":"go"}}`,
		`{"type":"assistant","effort":"high","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":20,"output_tokens":7}}}`,
		`{"type":"assistant","effort":"high","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":20,"output_tokens":7}}}`,
		`{"type":"assistant","isSidechain":true,"effort":"low","message":{"id":"s1","model":"claude-haiku-5-5","usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"user","isSidechain":true,"permissionMode":"plan","message":{"content":"sub"}}`,
		`{"type":"assistant","message":{"id":"m3","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	count := (&Agent{}).NewUsage()
	total := &protocol.Usage{}
	for _, l := range lines {
		count.Count([]byte(l), total)
	}
	if total.GetModel() != "claude-opus-5-5" || total.GetEffort() != "high" || total.GetPermissionMode() != "bypassPermissions" {
		t.Fatalf("model %q effort %q mode %q", total.GetModel(), total.GetEffort(), total.GetPermissionMode())
	}
	if total.GetInput() != 33 || total.GetOutput() != 13 {
		t.Fatalf("tokens in %d out %d", total.GetInput(), total.GetOutput())
	}
}
