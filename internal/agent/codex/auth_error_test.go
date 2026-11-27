package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestClassifyTurnError(t *testing.T) {
	t.Run("structured unauthorized with refresh token reuse", func(t *testing.T) {
		ae := ClassifyTurnError(TurnError{
			CodexErrorInfo:    json.RawMessage(`"unauthorized"`),
			Message:           "Your access token could not be refreshed because your refresh token was already used (refresh_token_reused). Please log out and sign in again. token=eyJsecret https://auth.internal.example",
			AdditionalDetails: "refresh_token_reused",
		})
		if ae == nil {
			t.Fatal("expected AuthError")
		}
		if ae.GetKind() != protocol.AuthErrorOAuthExpired {
			t.Fatalf("kind = %q, want %q", ae.GetKind(), protocol.AuthErrorOAuthExpired)
		}
		if ae.GetProvider() != "codex" || ae.GetCommand() != LoginCommand {
			t.Fatalf("unexpected provider/command: %+v", ae)
		}
		for _, want := range []string{"codex login", "same OS user", "separate from Mako node access", "has not retried"} {
			if !strings.Contains(ae.GetGuidance(), want) {
				t.Errorf("guidance missing %q: %q", want, ae.GetGuidance())
			}
		}
		for _, forbidden := range []string{"eyJsecret", "auth.internal.example"} {
			if strings.Contains(ae.GetSummary(), forbidden) || strings.Contains(ae.GetGuidance(), forbidden) {
				t.Errorf("raw secret/URL %q leaked: %+v", forbidden, ae)
			}
		}
	})

	t.Run("structured unauthorized with API key does not claim expired", func(t *testing.T) {
		ae := ClassifyTurnError(TurnError{
			CodexErrorInfo: json.RawMessage(`"unauthorized"`),
			Message:        "Incorrect API key provided: sk-proj-secret123",
		})
		if ae == nil || ae.GetKind() != protocol.AuthErrorProviderAuth {
			t.Fatalf("expected provider_auth, got %+v", ae)
		}
		if strings.Contains(strings.ToLower(ae.GetSummary()), "expired") {
			t.Fatalf("provider_auth summary should not claim expired: %q", ae.GetSummary())
		}
		if strings.Contains(ae.GetGuidance(), "sk-proj-secret123") {
			t.Fatalf("raw secret leaked: %q", ae.GetGuidance())
		}
	})

	t.Run("untyped and other fallback match exact anchored refresh failures", func(t *testing.T) {
		for _, msg := range []string{
			"Your access token could not be refreshed because it expired.",
			"Failed to refresh access token: invalid_grant",
			"Authentication token has been invalidated, please sign in again.",
			"Provided authentication token is expired.",
		} {
			for _, info := range []json.RawMessage{nil, json.RawMessage(`"other"`)} {
				ae := ClassifyTurnError(TurnError{Message: msg, CodexErrorInfo: info})
				if ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
					t.Fatalf("msg=%q info=%s: got %+v, want oauth_expired", msg, info, ae)
				}
			}
		}
	})

	t.Run("non-auth codexErrorInfo variants and arbitrary 401s never trigger", func(t *testing.T) {
		negatives := []struct {
			name string
			err  TurnError
		}{
			{
				name: "rateLimitExceeded",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`"rateLimitExceeded"`), Message: "Your access token could not be refreshed"},
			},
			{
				name: "usageLimitExceeded",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`"usageLimitExceeded"`), Message: "usage limit"},
			},
			{
				name: "contextWindowExceeded",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`"contextWindowExceeded"`), Message: "context window"},
			},
			{
				name: "sandboxError",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`"sandboxError"`), Message: "sandbox"},
			},
			{
				name: "httpConnectionFailed with 401",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`{"httpConnectionFailed":{"httpStatusCode":401}}`), Message: "Your access token could not be refreshed"},
			},
			{
				name: "responseStreamDisconnected with 401",
				err:  TurnError{CodexErrorInfo: json.RawMessage(`{"responseStreamDisconnected":{"httpStatusCode":401}}`), Message: "401 Unauthorized"},
			},
			{
				name: "bare 401 without codexErrorInfo",
				err:  TurnError{Message: "401 Unauthorized"},
			},
			{
				name: "MCP prefix with refresh text",
				err:  TurnError{Message: "MCP server github: Your access token could not be refreshed"},
			},
			{
				name: "quoted conversation text",
				err:  TurnError{Message: `User asked about "Your access token could not be refreshed"`},
			},
		}
		for _, tc := range negatives {
			if ae := ClassifyTurnError(tc.err); ae != nil {
				t.Errorf("%s: unexpected AuthError %+v", tc.name, ae)
			}
		}
	})
}

func TestRolloutAuthErrorEvent(t *testing.T) {
	var a Agent

	cases := []struct {
		name        string
		line        string
		wantKind    string
		wantCleared bool
	}{
		{
			name:     "event_msg error snake_case unauthorized oauth expired",
			line:     `{"type":"event_msg","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"error","codex_error_info":"unauthorized","message":"Your access token could not be refreshed because your refresh token was expired."}}`,
			wantKind: protocol.AuthErrorOAuthExpired,
		},
		{
			name:     "event_msg error camelCase unauthorized api key",
			line:     `{"type":"event_msg","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"error","codexErrorInfo":"unauthorized","message":"Incorrect API key provided"}}`,
			wantKind: protocol.AuthErrorProviderAuth,
		},
		{
			name:     "event_msg error untyped anchored fallback",
			line:     `{"type":"event_msg","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"error","message":"Provided authentication token is expired."}}`,
			wantKind: protocol.AuthErrorOAuthExpired,
		},
		{
			name:        "event_msg task_started clears",
			line:        `{"type":"event_msg","timestamp":"2026-11-28T01:00:05Z","payload":{"type":"task_started"}}`,
			wantCleared: true,
		},
		{
			name:        "event_msg task_complete clears",
			line:        `{"type":"event_msg","timestamp":"2026-11-28T01:00:10Z","payload":{"type":"task_complete"}}`,
			wantCleared: true,
		},
		{
			name: "response_item user message quoting refresh error ignored",
			line: `{"type":"response_item","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Your access token could not be refreshed"}]}}`,
		},
		{
			name: "response_item function_call_output quoting event_msg ignored",
			line: `{"type":"response_item","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"function_call_output","output":"{\"type\":\"event_msg\",\"payload\":{\"type\":\"error\",\"codex_error_info\":\"unauthorized\"}}"}}`,
		},
		{
			name: "event_msg error rateLimitExceeded ignored",
			line: `{"type":"event_msg","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"error","codex_error_info":"rateLimitExceeded","message":"Rate limit hit"}}`,
		},
		{
			name: "event_msg error httpConnectionFailed 401 ignored",
			line: `{"type":"event_msg","timestamp":"2026-11-28T01:00:00Z","payload":{"type":"error","codex_error_info":{"httpConnectionFailed":{"httpStatusCode":401}},"message":"HTTP 401"}}`,
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

func TestCodexClassifyOneShotError(t *testing.T) {
	if ae := ClassifyOneShotError(nil, []byte("Your access token could not be refreshed because your refresh token was revoked.")); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("expected oauth_expired, got %+v", ae)
	}
	if ae := ClassifyOneShotError(nil, []byte("401 Unauthorized")); ae != nil {
		t.Fatalf("bare 401 should not match: %+v", ae)
	}
	if ae := ClassifyOneShotError(nil, []byte("MCP error: Your access token could not be refreshed")); ae != nil {
		t.Fatalf("MCP prefix should not match: %+v", ae)
	}
}
