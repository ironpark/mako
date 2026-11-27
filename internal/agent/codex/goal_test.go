package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ironpark/mako/internal/protocol"
)

func TestMapGoalUpdatedAndCleared(t *testing.T) {
	now := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)

	active := MapGoalUpdated(ThreadGoalUpdatedNotification{
		ThreadID: "thread-1",
		Goal: &ThreadGoal{
			ThreadID:        "thread-1",
			Objective:       "finish the release",
			Status:          "active",
			TokensUsed:      42,
			TimeUsedSeconds: 7,
			CreatedAt:       1781680000,
			UpdatedAt:       1781680007,
		},
	}, "thread-1", true, true, now)
	if active == nil || active.GetSource() != "codex" || active.GetStatus() != protocol.GoalActive ||
		active.GetText() != "finish the release" || active.GetProviderStatus() != "active" ||
		active.GetSourceSessionId() != "thread-1" || active.GetSourceRevision() != "1781680007" ||
		!active.GetCanEdit() || !active.GetCanClear() {
		t.Fatalf("unexpected active goal: %v", active)
	}

	// Paused, blocked, and limited states remain visible as active goals with ProviderStatus set.
	for _, st := range []string{"paused", "blocked", "usageLimited", "budgetLimited"} {
		g := MapGoalUpdated(ThreadGoalUpdatedNotification{
			ThreadID: "thread-1",
			Goal: &ThreadGoal{
				ThreadID:  "thread-1",
				Objective: "goal " + st,
				Status:    st,
				UpdatedAt: 2,
			},
		}, "thread-1", false, true, now)
		if g == nil || g.GetStatus() != protocol.GoalActive || g.GetProviderStatus() != st || g.GetText() != "goal "+st || g.GetCanEdit() || !g.GetCanClear() {
			t.Fatalf("status %s mapped unexpectedly: %v", st, g)
		}
	}

	// Complete and cleared map to inactive states.
	comp := MapGoalUpdated(ThreadGoalUpdatedNotification{
		ThreadID: "thread-1",
		Goal:     &ThreadGoal{ThreadID: "thread-1", Objective: "done", Status: "complete", UpdatedAt: 9},
	}, "thread-1", true, true, now)
	if comp == nil || comp.GetStatus() != protocol.GoalInactive || comp.GetReason() != protocol.GoalReasonCompleted || comp.GetSourceRevision() != "9" {
		t.Fatalf("unexpected complete goal: %v", comp)
	}

	cleared := MapGoalCleared(ThreadGoalClearedNotification{ThreadIDSnake: "thread-1"}, "thread-1", now)
	if cleared == nil || cleared.GetStatus() != protocol.GoalInactive || cleared.GetReason() != protocol.GoalReasonCleared || cleared.GetSourceSessionId() != "thread-1" {
		t.Fatalf("unexpected cleared goal: %v", cleared)
	}

	// Malformed goal updates become unavailable/malformed.
	for name, notif := range map[string]ThreadGoalUpdatedNotification{
		"nil goal":        {ThreadID: "thread-1"},
		"blank objective": {ThreadID: "thread-1", Goal: &ThreadGoal{ThreadID: "thread-1", Objective: "   ", Status: "active"}},
		"unknown status":  {ThreadID: "thread-1", Goal: &ThreadGoal{ThreadID: "thread-1", Objective: "x", Status: "mystery"}},
	} {
		g := MapGoalUpdated(notif, "thread-1", true, true, now)
		if g == nil || g.GetStatus() != protocol.GoalUnavailable || g.GetReason() != protocol.GoalReasonMalformed {
			t.Fatalf("%s: expected unavailable/malformed, got %v", name, g)
		}
	}

	// Notifications for another thread (subagent / old thread) are ignored.
	if MapGoalUpdated(ThreadGoalUpdatedNotification{
		ThreadID: "sub-thread",
		Goal:     &ThreadGoal{ThreadID: "sub-thread", Objective: "sub goal", Status: "active"},
	}, "parent-thread", true, true, now) != nil {
		t.Fatal("expected nil for mismatched thread update")
	}
	if MapGoalCleared(ThreadGoalClearedNotification{ThreadID: "sub-thread"}, "parent-thread", now) != nil {
		t.Fatal("expected nil for mismatched thread clear")
	}
}

func TestParseGoalCommand(t *testing.T) {
	action, obj, ok := ParseGoalCommand("/goal finish the release")
	if !ok || action != protocol.GoalActionSet || obj != "finish the release" {
		t.Fatalf("got (%q, %q, %v)", action, obj, ok)
	}
	action, obj, ok = ParseGoalCommand("  /GOAL   clear  ")
	if !ok || action != protocol.GoalActionClear || obj != "" {
		t.Fatalf("got (%q, %q, %v)", action, obj, ok)
	}
	for _, invalid := range []string{"/goal", "/goal   ", "please /goal x", "/goalpost x"} {
		if _, _, ok := ParseGoalCommand(invalid); ok {
			t.Errorf("expected %q not to parse as goal command", invalid)
		}
	}
}

func TestAppServerClientThreadGoalRPCs(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var unsupported bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			switch req.Method {
			case "thread/goal/set":
				if unsupported {
					_ = conn.WriteJSON(map[string]any{
						"id":    req.ID,
						"error": map[string]any{"code": -32601, "message": "method not found"},
					})
					continue
				}
				var p ThreadGoalSetParams
				_ = json.Unmarshal(req.Params, &p)
				obj := ""
				if p.Objective != nil {
					obj = *p.Objective
				}
				_ = conn.WriteJSON(map[string]any{
					"id": req.ID,
					"result": map[string]any{
						"goal": map[string]any{
							"threadId":  p.ThreadID,
							"objective": obj,
							"status":    "active",
							"updatedAt": 1781680010,
						},
					},
				})
			case "thread/goal/clear":
				var p ThreadGoalClearParams
				_ = json.Unmarshal(req.Params, &p)
				_ = conn.WriteJSON(map[string]any{
					"id":     req.ID,
					"result": map[string]any{"cleared": true},
				})
			}
		}
	}))
	defer srv.Close()

	c, err := DialAppServerWS(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	obj := "ship v2"
	res, err := c.ThreadGoalSet(t.Context(), ThreadGoalSetParams{ThreadID: "th-1", Objective: &obj})
	if err != nil {
		t.Fatalf("ThreadGoalSet: %v", err)
	}
	if res.Goal == nil || res.Goal.Objective != "ship v2" || res.Goal.Status != "active" || res.Goal.UpdatedAt != 1781680010 {
		t.Fatalf("unexpected ThreadGoalSet result: %+v", res)
	}

	clearRes, err := c.ThreadGoalClear(t.Context(), "th-1")
	if err != nil {
		t.Fatalf("ThreadGoalClear: %v", err)
	}
	if clearRes.Cleared == nil || !*clearRes.Cleared {
		t.Fatalf("unexpected ThreadGoalClear result: %+v", clearRes)
	}

	unsupported = true
	_, err = c.ThreadGoalSet(t.Context(), ThreadGoalSetParams{ThreadID: "th-1", Objective: &obj})
	if !IsMethodNotFound(err) {
		t.Fatalf("expected MethodNotFound error, got %v", err)
	}
}
