package gui

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCollectInboxItemsPriorityAndFiltering(t *testing.T) {
	now := time.Now()
	sessions := []*protocol.SessionInfo{
		{Id: "done-unread", State: protocol.StateRunning, Status: protocol.AgentDone, StatusAt: timestamppb.New(now)},
		{Id: "waiting-1", State: protocol.StateRunning, Status: protocol.AgentWaiting, StatusAt: timestamppb.New(now.Add(-2 * time.Minute))},
		{Id: "auth-err", State: protocol.StateRunning, AuthError: &protocol.AuthError{Summary: "expired"}, StatusAt: timestamppb.New(now.Add(-3 * time.Minute))},
		{Id: "approval-old", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a1", Tool: "Bash"}, StatusAt: timestamppb.New(now.Add(-5 * time.Minute))},
		{Id: "approval-new", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a2", Tool: "Edit"}, StatusAt: timestamppb.New(now.Add(-1 * time.Minute))},
		{Id: "working-quiet", State: protocol.StateRunning, Status: protocol.AgentWorking},
		{Id: "stopped-unread", State: protocol.StateStopped},
	}
	unread := map[string]bool{
		"done-unread":    true,
		"stopped-unread": true,
		"approval-new":   true, // already categorized as approval, must not duplicate
	}

	items := collectInboxItems(sessions, unread)
	wantOrder := []string{"approval-new", "approval-old", "auth-err", "waiting-1", "done-unread", "stopped-unread"}
	if len(items) != len(wantOrder) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(wantOrder), items)
	}
	for i, wantID := range wantOrder {
		if items[i].Session.GetId() != wantID {
			t.Errorf("items[%d] = %s (kind %d), want %s", i, items[i].Session.GetId(), items[i].Kind, wantID)
		}
	}

	if got := filterInboxItems(items, inboxFilterApprovals); len(got) != 2 {
		t.Errorf("approvals filter len = %d, want 2", len(got))
	}
	if got := filterInboxItems(items, inboxFilterWaiting); len(got) != 2 {
		t.Errorf("waiting filter len = %d, want 2", len(got))
	}
	if got := filterInboxItems(items, inboxFilterUnread); len(got) != 2 {
		t.Errorf("unread filter len = %d, want 2", len(got))
	}
}

func TestIsBatchSafeApproval(t *testing.T) {
	cases := []struct {
		name string
		a    *protocol.Approval
		want bool
	}{
		{"nil", nil, false},
		{"bash tool", &protocol.Approval{Id: "1", Tool: "Bash"}, true},
		{"question", &protocol.Approval{Id: "2", Questions: []*protocol.Question{{Question: "Pick?"}}}, false},
		{"plan", &protocol.Approval{Id: "3", Kind: protocol.ToolExitPlan}, false},
		{"layout", &protocol.Approval{Id: "4", Kind: protocol.ToolLayout}, false},
		{"template", &protocol.Approval{Id: "5", Kind: protocol.ToolTemplate}, false},
	}
	for _, tc := range cases {
		if got := isBatchSafeApproval(tc.a); got != tc.want {
			t.Errorf("%s: isBatchSafeApproval = %v, want %v", tc.name, got, tc.want)
		}
	}
}
