package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

func TestWithHooksRegistersStopFailure(t *testing.T) {
	var a Agent
	l := &agent.Launch{Command: []string{"claude"}}
	if !a.WithHooks(l, "/bin/makod hook /tmp/h.sock", "", "") {
		t.Fatal("WithHooks returned false")
	}
	i := slices.Index(l.Command, "--settings")
	if i < 0 || i+1 >= len(l.Command) {
		t.Fatalf("missing --settings in %q", l.Command)
	}
	var doc struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(l.Command[i+1]), &doc); err != nil {
		t.Fatalf("invalid settings JSON: %v", err)
	}
	if _, ok := doc.Hooks["StopFailure"]; !ok {
		t.Fatalf("StopFailure hook not registered in %v", doc.Hooks)
	}
}

func TestStopFailureAuthErrorDetection(t *testing.T) {
	var a Agent

	t.Run("oauth expired via structured authentication_failed", func(t *testing.T) {
		raw := `{
			"hook_event_name": "StopFailure",
			"session_id": "conv-1",
			"transcript_path": "/tmp/conv-1.jsonl",
			"error": "authentication_failed",
			"error_details": "Failed to authenticate: OAuth session expired and could not be refreshed. Please run /login. secret=sk-ant-leak https://internal.corp/oauth"
		}`
		ev, ok := a.Event([]byte(raw))
		if !ok {
			t.Fatal("expected event")
		}
		if ev.Kind != agent.Idle {
			t.Fatalf("kind = %v, want Idle", ev.Kind)
		}
		ae := ev.AuthError
		if ae == nil {
			t.Fatal("expected AuthError")
		}
		if ae.GetKind() != protocol.AuthErrorOAuthExpired {
			t.Fatalf("kind = %q, want %q", ae.GetKind(), protocol.AuthErrorOAuthExpired)
		}
		if ae.GetProvider() != "claude" || ae.GetCommand() != LoginCommand {
			t.Fatalf("unexpected provider/command: %+v", ae)
		}
		for _, want := range []string{"claude auth login", "same OS user", "separate from Mako node access", "has not retried"} {
			if !strings.Contains(ae.GetGuidance(), want) {
				t.Errorf("guidance missing %q: %q", want, ae.GetGuidance())
			}
		}
		for _, forbidden := range []string{"sk-ant-leak", "internal.corp"} {
			if strings.Contains(ae.GetSummary(), forbidden) || strings.Contains(ae.GetGuidance(), forbidden) {
				t.Errorf("raw secret/URL %q leaked into AuthError: %+v", forbidden, ae)
			}
		}
	})

	t.Run("api key authentication_failed does not claim expired", func(t *testing.T) {
		raw := `{
			"hook_event_name": "StopFailure",
			"session_id": "conv-1",
			"error": "authentication_failed",
			"error_details": {"message": "Invalid API key sk-ant-api03-secret"}
		}`
		ev, ok := a.Event([]byte(raw))
		if !ok || ev.AuthError == nil {
			t.Fatalf("expected provider auth error, got ok=%v ev=%+v", ok, ev)
		}
		if ev.AuthError.GetKind() != protocol.AuthErrorProviderAuth {
			t.Fatalf("kind = %q, want %q", ev.AuthError.GetKind(), protocol.AuthErrorProviderAuth)
		}
		if strings.Contains(strings.ToLower(ev.AuthError.GetSummary()), "expired") {
			t.Fatalf("provider auth summary should not claim expired: %q", ev.AuthError.GetSummary())
		}
		if strings.Contains(ev.AuthError.GetGuidance(), "sk-ant-api03-secret") {
			t.Fatalf("raw API key leaked: %q", ev.AuthError.GetGuidance())
		}
	})

	t.Run("error_type alias and last_assistant_message", func(t *testing.T) {
		raw := `{
			"hook_event_name": "StopFailure",
			"session_id": "conv-1",
			"error_type": "authentication_failed",
			"last_assistant_message": "Failed to authenticate: OAuth session expired and could not be refreshed."
		}`
		ev, ok := a.Event([]byte(raw))
		if !ok || ev.AuthError == nil || ev.AuthError.GetKind() != protocol.AuthErrorOAuthExpired {
			t.Fatalf("expected oauth_expired via error_type, got ok=%v ev=%+v", ok, ev)
		}
	})

	t.Run("untyped StopFailure with anchored oauth expiry", func(t *testing.T) {
		raw := `{
			"hook_event_name": "StopFailure",
			"session_id": "conv-1",
			"error_message": "Failed to authenticate: OAuth session expired and could not be refreshed"
		}`
		ev, ok := a.Event([]byte(raw))
		if !ok || ev.AuthError == nil || ev.AuthError.GetKind() != protocol.AuthErrorOAuthExpired {
			t.Fatalf("expected oauth_expired fallback, got ok=%v ev=%+v", ok, ev)
		}
	})

	t.Run("non-auth StopFailure codes never trigger", func(t *testing.T) {
		for _, code := range []string{
			"rate_limit",
			"billing_error",
			"overloaded",
			"server_error",
			"unknown",
			"max_output_tokens",
			"oauth_org_not_allowed",
		} {
			raw := `{"hook_event_name":"StopFailure","session_id":"c","error":"` + code + `","error_details":"Failed to authenticate: OAuth session expired and could not be refreshed"}`
			ev, ok := a.Event([]byte(raw))
			if !ok {
				t.Fatalf("%s: expected Idle event", code)
			}
			if ev.Kind != agent.Idle {
				t.Fatalf("%s: kind = %v, want Idle", code, ev.Kind)
			}
			if ev.AuthError != nil {
				t.Fatalf("%s: unexpected AuthError %+v", code, ev.AuthError)
			}
		}
	})

	t.Run("untyped StopFailure rejects bare 401, MCP prefixes, and quoted text", func(t *testing.T) {
		for _, detail := range []string{
			"401 Unauthorized",
			"HTTP 401 from upstream",
			"MCP server linear: Failed to authenticate: OAuth session expired and could not be refreshed",
			"User quoted: Failed to authenticate: OAuth session expired and could not be refreshed",
		} {
			payload, _ := json.Marshal(map[string]any{
				"hook_event_name": "StopFailure",
				"session_id":      "c",
				"error_message":   detail,
			})
			ev, ok := a.Event(payload)
			if !ok || ev.AuthError != nil {
				t.Fatalf("detail %q should not trigger auth error: ok=%v err=%+v", detail, ok, ev.AuthError)
			}
		}
	})

	t.Run("subagent StopFailure is ignored", func(t *testing.T) {
		raw := `{
			"hook_event_name": "StopFailure",
			"session_id": "conv-1",
			"agent_id": "sub-1",
			"error": "authentication_failed",
			"error_details": "Failed to authenticate: OAuth session expired and could not be refreshed."
		}`
		if ev, ok := a.Event([]byte(raw)); ok {
			t.Fatalf("subagent StopFailure should be ignored, got %+v", ev)
		}
	})
}

func TestTranscriptAuthErrorEvent(t *testing.T) {
	var a Agent

	cases := []struct {
		name        string
		line        string
		wantKind    string
		wantCleared bool
	}{
		{
			name:     "assistant authentication_failed with oauth expired text",
			line:     `{"type":"assistant","timestamp":"2026-11-28T01:00:00Z","error":"authentication_failed","isApiErrorMessage":true,"message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed. Please run /login."}]}}`,
			wantKind: protocol.AuthErrorOAuthExpired,
		},
		{
			name:     "assistant authentication_failed with api key error",
			line:     `{"type":"assistant","timestamp":"2026-11-28T01:00:00Z","error":"authentication_failed","isApiErrorMessage":true,"message":{"model":"<synthetic>","role":"assistant","content":"Invalid API key"}}`,
			wantKind: protocol.AuthErrorProviderAuth,
		},
		{
			name:     "result is_error subtype success oauth expired",
			line:     `{"type":"result","timestamp":"2026-11-28T01:00:00Z","is_error":true,"subtype":"success","result":"Failed to authenticate: OAuth session expired and could not be refreshed."}`,
			wantKind: protocol.AuthErrorOAuthExpired,
		},
		{
			name:     "result is_error errors array oauth expired",
			line:     `{"type":"result","timestamp":"2026-11-28T01:00:00Z","is_error":true,"subtype":"error_during_execution","errors":["Failed to authenticate: OAuth session expired and could not be refreshed"]}`,
			wantKind: protocol.AuthErrorOAuthExpired,
		},
		{
			name: "user message quoting oauth expired text does not trigger",
			line: `{"type":"user","timestamp":"2026-11-28T01:00:00Z","message":{"role":"user","content":"Why did I see: Failed to authenticate: OAuth session expired and could not be refreshed"}}`,
		},
		{
			name: "tool_result error quoting oauth expired text does not trigger",
			line: `{"type":"user","timestamp":"2026-11-28T01:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"Failed to authenticate: OAuth session expired and could not be refreshed"}]}}`,
		},
		{
			name: "system init MCP error does not trigger",
			line: `{"type":"system","subtype":"init","mcp_servers":[{"name":"linear","status":"Failed to authenticate: OAuth session expired and could not be refreshed"}]}`,
		},
		{
			name: "sidechain assistant authentication_failed ignored",
			line: `{"type":"assistant","isSidechain":true,"error":"authentication_failed","message":{"role":"assistant","content":"Failed to authenticate: OAuth session expired and could not be refreshed"}}`,
		},
		{
			name: "meta assistant authentication_failed ignored",
			line: `{"type":"assistant","isMeta":true,"error":"authentication_failed","message":{"role":"assistant","content":"Failed to authenticate: OAuth session expired and could not be refreshed"}}`,
		},
		{
			name: "subagent parent_tool_use_id assistant ignored",
			line: `{"type":"assistant","parent_tool_use_id":"toolu_123","error":"authentication_failed","message":{"role":"assistant","content":"Failed to authenticate: OAuth session expired and could not be refreshed"}}`,
		},
		{
			name: "subagent parentToolUseId assistant ignored",
			line: `{"type":"assistant","parentToolUseId":"toolu_123","error":"authentication_failed","message":{"role":"assistant","content":"Failed to authenticate: OAuth session expired and could not be refreshed"}}`,
		},
		{
			name: "assistant rate_limit error ignored and does not clear",
			line: `{"type":"assistant","error":"rate_limit","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Rate limited"}]}}`,
		},
		{
			name: "result is_error MCP prefixed text ignored",
			line: `{"type":"result","is_error":true,"subtype":"error_during_execution","errors":["MCP server github: Failed to authenticate: OAuth session expired and could not be refreshed"]}`,
		},
		{
			name: "result is_error bare 401 ignored",
			line: `{"type":"result","is_error":true,"subtype":"error_during_execution","errors":["401 Unauthorized"]}`,
		},
		{
			name: "synthetic assistant message does not clear",
			line: `{"type":"assistant","timestamp":"2026-11-28T01:00:01Z","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Local notice"}]}}`,
		},
		{
			name: "api error assistant message without error code does not clear",
			line: `{"type":"assistant","timestamp":"2026-11-28T01:00:01Z","isApiErrorMessage":true,"message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Temporary failure"}]}}`,
		},
		{
			name:        "normal top-level assistant reply clears",
			line:        `{"type":"assistant","timestamp":"2026-11-28T01:00:02Z","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Here is the fix."}]}}`,
			wantCleared: true,
		},
		{
			name:        "successful result clears even if quoting expired text",
			line:        `{"type":"result","timestamp":"2026-11-28T01:00:03Z","is_error":false,"subtype":"success","result":"Failed to authenticate: OAuth session expired and could not be refreshed"}`,
			wantCleared: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := a.AuthErrorEvent([]byte(tc.line))
			if tc.wantKind == "" {
				if ev.AuthError != nil {
					t.Fatalf("unexpected AuthError: %+v", ev.AuthError)
				}
			} else {
				if ev.AuthError == nil || ev.AuthError.GetKind() != tc.wantKind {
					t.Fatalf("AuthError = %+v, want kind %q", ev.AuthError, tc.wantKind)
				}
			}
			if ev.Cleared != tc.wantCleared {
				t.Fatalf("Cleared = %v, want %v", ev.Cleared, tc.wantCleared)
			}
		})
	}
}

func TestClassifyOneShotError(t *testing.T) {
	if ae := ClassifyOneShotError(
		[]byte(`{"is_error":true,"error":"authentication_failed","result":"Failed to authenticate: OAuth session expired and could not be refreshed."}`),
		nil,
	); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("expected oauth_expired, got %+v", ae)
	}

	if ae := ClassifyOneShotError(
		[]byte(`{"is_error":true,"error":"authentication_failed","result":"Invalid API key"}`),
		nil,
	); ae == nil || ae.GetKind() != protocol.AuthErrorProviderAuth {
		t.Fatalf("expected provider_auth, got %+v", ae)
	}

	if ae := ClassifyOneShotError(
		nil,
		[]byte("Failed to authenticate: OAuth session expired and could not be refreshed. Please run /login."),
	); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("expected stderr oauth_expired, got %+v", ae)
	}

	for _, tc := range []struct {
		name           string
		stdout, stderr string
	}{
		{"success envelope quoting text", `{"is_error":false,"result":"Failed to authenticate: OAuth session expired and could not be refreshed"}`, ""},
		{"rate_limit envelope", `{"is_error":true,"error":"rate_limit","result":"Failed to authenticate: OAuth session expired and could not be refreshed"}`, ""},
		{"bare 401 stderr", "", "Error: 401 Unauthorized"},
		{"mcp prefixed stderr", "", "MCP server docs: Failed to authenticate: OAuth session expired and could not be refreshed"},
	} {
		if ae := ClassifyOneShotError([]byte(tc.stdout), []byte(tc.stderr)); ae != nil {
			t.Errorf("%s: unexpected AuthError %+v", tc.name, ae)
		}
	}
}
