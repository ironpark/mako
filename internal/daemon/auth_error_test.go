//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

func waitAuthError(t *testing.T, s *session.Session, wantKind string) *protocol.AuthError {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ae := s.Info().GetAuthError()
		if wantKind == "" && ae == nil {
			return nil
		}
		if wantKind != "" && ae != nil && ae.GetKind() == wantKind {
			return ae
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for auth error kind %q; current = %+v", wantKind, ae)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClaudeStopFailureHookSetsAndClearsAuthError(t *testing.T) {
	d, s, socket := hookedDaemon(t)
	waitConversation(t, s)
	convID := s.Info().GetConversationId()

	sendHookEvent := func(payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		runHook([]string{socket}, strings.NewReader(string(raw)), io.Discard)
	}

	// Non-auth StopFailure (rate_limit) does not set AuthError.
	sendHookEvent(map[string]any{
		"hook_event_name": "StopFailure",
		"session_id":      convID,
		"error":           "rate_limit",
		"error_details":   "Failed to authenticate: OAuth session expired and could not be refreshed.",
	})
	time.Sleep(80 * time.Millisecond)
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("rate_limit StopFailure set AuthError: %+v", ae)
	}

	// Subagent StopFailure does not set AuthError on parent session.
	sendHookEvent(map[string]any{
		"hook_event_name": "StopFailure",
		"session_id":      convID,
		"agent_id":        "subagent-42",
		"error":           "authentication_failed",
		"error_details":   "Failed to authenticate: OAuth session expired and could not be refreshed.",
	})
	time.Sleep(80 * time.Millisecond)
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("subagent StopFailure set AuthError: %+v", ae)
	}

	// Top-level StopFailure with authentication_failed sets AuthError and bumps attention_seq.
	beforeAtt := s.Info().GetAttentionSeq()
	sendHookEvent(map[string]any{
		"hook_event_name": "StopFailure",
		"session_id":      convID,
		"error":           "authentication_failed",
		"error_details":   "Failed to authenticate: OAuth session expired and could not be refreshed. secret=sk-ant-raw",
	})
	ae := waitAuthError(t, s, protocol.AuthErrorOAuthExpired)
	if s.Info().GetAttentionSeq() <= beforeAtt {
		t.Fatalf("attention_seq did not advance on auth error: before=%d after=%d", beforeAtt, s.Info().GetAttentionSeq())
	}
	if strings.Contains(ae.GetGuidance(), "sk-ant-raw") || !strings.Contains(ae.GetGuidance(), "claude auth login") {
		t.Fatalf("unexpected guidance: %q", ae.GetGuidance())
	}

	// UserPromptSubmit clears AuthError automatically.
	sendHookEvent(map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      convID,
	})
	waitAuthError(t, s, "")

	// Re-trigger auth error, then test Send clearing semantics.
	sendHookEvent(map[string]any{
		"hook_event_name": "StopFailure",
		"session_id":      convID,
		"error":           "authentication_failed",
		"error_details":   "Invalid API key",
	})
	waitAuthError(t, s, protocol.AuthErrorProviderAuth)

	sendViaHandler := func(req *protocol.Send) {
		t.Helper()
		stream, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleSend(t.Context(), st, env)
		}, protocol.TypeSend, req)
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("handleSend recv: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("handleSend err: %v", err)
		}
	}

	// Bare Escape (keys only, empty text) must NOT clear AuthError.
	sendViaHandler(&protocol.Send{SessionId: s.ID(), Keys: []byte("\x1b")})
	if s.Info().GetAuthError() == nil {
		t.Fatal("bare key send cleared active AuthError")
	}

	// Sending a prompt text clears AuthError.
	sendViaHandler(&protocol.Send{SessionId: s.ID(), Text: "retry prompt", Keys: []byte("\r")})
	if s.Info().GetAuthError() != nil {
		t.Fatal("handleSend with text did not clear AuthError")
	}
}

func TestClaudeTranscriptFallbackDetectsAndClearsAuthError(t *testing.T) {
	d, s, _ := hookedDaemon(t)
	waitConversation(t, s)
	path := s.TranscriptPath()
	if path == "" {
		t.Fatal("expected transcript path")
	}

	appendRecord := func(rec map[string]any) {
		t.Helper()
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(b, '\n'))
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC().Add(time.Second)
	// Quoted user message must not trigger.
	appendRecord(map[string]any{
		"type":      "user",
		"timestamp": now.Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "user", "content": "Failed to authenticate: OAuth session expired and could not be refreshed"},
	})
	d.refreshActivity(s)
	if s.Info().GetAuthError() != nil {
		t.Fatalf("quoted user message triggered AuthError: %+v", s.Info().GetAuthError())
	}

	// Real authentication_failed assistant record triggers AuthError.
	appendRecord(map[string]any{
		"type":              "assistant",
		"timestamp":         now.Add(time.Second).Format(time.RFC3339Nano),
		"error":             "authentication_failed",
		"isApiErrorMessage": true,
		"message": map[string]any{
			"model":   "<synthetic>",
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "Failed to authenticate: OAuth session expired and could not be refreshed."}},
		},
	})
	d.refreshActivity(s)
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired {
		t.Fatalf("expected oauth_expired from transcript, got %+v", ae)
	}

	// Subsequent normal assistant reply clears AuthError.
	appendRecord(map[string]any{
		"type":      "assistant",
		"timestamp": now.Add(2 * time.Second).Format(time.RFC3339Nano),
		"message": map[string]any{
			"model":   "claude-sonnet-4-6",
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "Back online."}},
		},
	})
	d.refreshActivity(s)
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("expected transcript recovery to clear AuthError, got %+v", ae)
	}
}

func TestCodexAppServerAuthErrorLifecycle(t *testing.T) {
	d := New(&config.Config{NodeID: "node"}, nil)
	s, err := session.Start(session.Spec{
		ID:      "codex-auth-test",
		Agent:   "codex",
		Command: testSleepCommand(),
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b := &codexBridge{
		ctx:          t.Context(),
		epoch:        1,
		tid:          "thr-1",
		bound:        true,
		itemIndices:  map[string]int32{},
		thinkingSeen: map[string]bool{},
		fgItems:      map[string]bool{},
		fgProcesses:  map[string]bool{},
		ignored:      map[string]bool{},
		abandoned:    map[string]bool{},
	}

	emitAtEpoch := func(epoch uint64, method string, params any) {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		d.handleCodexNotification(s, b, codex.Notification{
			Method: method,
			Params: raw,
		}, epoch)
	}
	emit := func(method string, params any) {
		t.Helper()
		emitAtEpoch(1, method, params)
	}

	// Stale-epoch error notification must NOT trigger AuthError.
	emitAtEpoch(0, "error", map[string]any{
		"threadId":  "thr-1",
		"turnId":    "turn-stale",
		"willRetry": false,
		"error": map[string]any{
			"codexErrorInfo": "unauthorized",
			"message":        "Your access token could not be refreshed.",
		},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("stale-epoch error set AuthError: %+v", ae)
	}

	// Retryable error must NOT trigger AuthError.
	emit("error", map[string]any{
		"threadId":  "thr-1",
		"turnId":    "turn-1",
		"willRetry": true,
		"error": map[string]any{
			"codexErrorInfo": "unauthorized",
			"message":        "Your access token could not be refreshed.",
		},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("willRetry error set AuthError: %+v", ae)
	}

	// Subagent / other thread error must NOT trigger AuthError.
	emit("error", map[string]any{
		"threadId":  "thr-subagent",
		"turnId":    "turn-sub",
		"willRetry": false,
		"error": map[string]any{
			"codexErrorInfo": "unauthorized",
			"message":        "Your access token could not be refreshed.",
		},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("other thread error set AuthError: %+v", ae)
	}

	// Terminal unauthorized OAuth refresh error sets AuthError.
	emit("error", map[string]any{
		"threadId":  "thr-1",
		"turnId":    "turn-1",
		"willRetry": false,
		"error": map[string]any{
			"codexErrorInfo": "unauthorized",
			"message":        "Your access token could not be refreshed because your refresh token was already used (refresh_token_reused).",
		},
	})
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorOAuthExpired || ae.GetCommand() != "codex login" {
		t.Fatalf("expected codex oauth_expired, got %+v", ae)
	}

	// turn/started clears AuthError.
	emit("turn/started", map[string]any{
		"threadId": "thr-1",
		"turn":     map[string]any{"id": "turn-2", "status": "inProgress"},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("turn/started did not clear AuthError: %+v", ae)
	}

	// turn/completed failed with rateLimitExceeded must NOT set AuthError.
	emit("turn/completed", map[string]any{
		"threadId": "thr-1",
		"turn": map[string]any{
			"id":     "turn-2",
			"status": "failed",
			"error": map[string]any{
				"codexErrorInfo": "rateLimitExceeded",
				"message":        "Rate limit exceeded",
			},
		},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("rateLimitExceeded set AuthError: %+v", ae)
	}

	// turn/completed failed with unauthorized API key sets provider_auth.
	emit("turn/completed", map[string]any{
		"threadId": "thr-1",
		"turn": map[string]any{
			"id":     "turn-3",
			"status": "failed",
			"error": map[string]any{
				"codexErrorInfo": "unauthorized",
				"message":        "Incorrect API key provided",
			},
		},
	})
	if ae := s.Info().GetAuthError(); ae == nil || ae.GetKind() != protocol.AuthErrorProviderAuth {
		t.Fatalf("expected codex provider_auth, got %+v", ae)
	}

	// Stale-epoch recovery notifications must NOT clear an active AuthError.
	emitAtEpoch(0, "account/login/completed", map[string]any{
		"loginId": "login-stale",
		"success": true,
	})
	emitAtEpoch(0, "turn/completed", map[string]any{
		"threadId": "thr-1",
		"turn":     map[string]any{"id": "turn-stale-ok", "status": "completed"},
	})
	if s.Info().GetAuthError() == nil {
		t.Fatal("stale-epoch recovery cleared active AuthError")
	}

	// Failed account/login/completed must NOT clear AuthError.
	emit("account/login/completed", map[string]any{
		"loginId": "login-1",
		"success": false,
		"error":   "cancelled",
	})
	if s.Info().GetAuthError() == nil {
		t.Fatal("failed account/login/completed cleared AuthError")
	}

	// Successful account/login/completed clears AuthError.
	emit("account/login/completed", map[string]any{
		"loginId": "login-2",
		"success": true,
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("successful account/login/completed did not clear AuthError: %+v", ae)
	}

	// turn/completed failed sets AuthError; subsequent turn/completed completed clears it.
	emit("turn/completed", map[string]any{
		"threadId": "thr-1",
		"turn": map[string]any{
			"id":     "turn-4",
			"status": "failed",
			"error": map[string]any{
				"codexErrorInfo": "unauthorized",
				"message":        "Provided authentication token is expired.",
			},
		},
	})
	if s.Info().GetAuthError() == nil {
		t.Fatal("expected auth error on failed turn/completed")
	}
	emit("turn/completed", map[string]any{
		"threadId": "thr-1",
		"turn":     map[string]any{"id": "turn-5", "status": "completed"},
	})
	if ae := s.Info().GetAuthError(); ae != nil {
		t.Fatalf("completed turn did not clear AuthError: %+v", ae)
	}
}

func TestRunAgentPromptAuthErrorReturnsCodedError(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
cat >/dev/null
printf '{"is_error":true,"error":"authentication_failed","result":"Failed to authenticate: OAuth session expired and could not be refreshed. secret=sk-ant-do-not-leak"}'
exit 1
`
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d := meshDaemonWith(t, "oneshot-auth", func(cfg *config.Config) {
		on := true
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {
				Command: []string{claudeBin},
				Enabled: &on,
			},
		}
	})
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	_, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		Agent:   "claude",
		Project: workDir,
		Prompt:  "hello",
		Format:  "text",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var coded *protocol.CodedError
	if !errors.As(err, &coded) || coded.Code != protocol.CodeAuthError {
		t.Fatalf("expected CodedError with code %q, got %T %v", protocol.CodeAuthError, err, err)
	}
	if !strings.Contains(err.Error(), "claude auth login") {
		t.Fatalf("expected re-login guidance in error, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "sk-ant-do-not-leak") {
		t.Fatalf("raw secret leaked in one-shot error: %q", err.Error())
	}
}
