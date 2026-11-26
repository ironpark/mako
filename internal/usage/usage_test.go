package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectAgyUsage(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")

	var calls atomic.Int32
	c := NewCollector()
	c.RunCommand = func(_ context.Context, bin string, args []string, env map[string]string) ([]byte, error) {
		calls.Add(1)
		if got := strings.Join(args, " "); got != "-p /usage --output-format json" {
			t.Fatalf("unexpected agy args: %q", got)
		}
		return []byte(`{
			"status": "SUCCESS",
			"command": {
				"name": "usage",
				"data": {
					"groups": [
						{
							"name": "Gemini Models",
							"buckets": [
								{"id": "g-5h", "name": "5h Limit Remaining", "window": "5h", "remaining_fraction": 0.75, "reset_time": "2026-11-27T12:00:00Z"},
								{"id": "g-wk", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 0.4, "reset_time": "2026-12-01T00:00:00Z"},
								{"id": "g-off", "name": "Experimental", "window": "weekly", "disabled": true}
							]
						}
					]
				}
			}
		}`), nil
	}

	env := map[string]string{"SSH_TTY": "/dev/pts/3"}
	res := c.Collect(t.Context(), []AgentSpec{{Name: "agy", Program: "/usr/local/bin/agy", Env: env}}, false)
	if len(res.GetAccounts()) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(res.GetAccounts()))
	}
	acc := res.GetAccounts()[0]
	if acc.GetStatus() != "available" || !strings.Contains(acc.GetDetail(), "SSH_TTY") {
		t.Fatalf("acc = %+v", acc)
	}
	if len(acc.GetLimits()) != 3 {
		t.Fatalf("limits len = %d, want 3", len(acc.GetLimits()))
	}
	if acc.GetLimits()[0].GetGroup() != "Gemini" || acc.GetLimits()[0].GetUsed() != 25 || acc.GetLimits()[0].GetMinutes() != 300 {
		t.Fatalf("first limit = %+v", acc.GetLimits()[0])
	}
	if acc.GetLimits()[1].GetUsed() != 60 || acc.GetLimits()[1].GetMinutes() != 7*24*60 {
		t.Fatalf("second limit = %+v", acc.GetLimits()[1])
	}
	if !acc.GetLimits()[2].GetDisabled() {
		t.Fatalf("third limit should be disabled: %+v", acc.GetLimits()[2])
	}

	// Second call without refresh hits TTL cache.
	_ = c.Collect(t.Context(), []AgentSpec{{Name: "agy", Program: "/usr/local/bin/agy", Env: env}}, false)
	if calls.Load() != 1 {
		t.Fatalf("expected cached call count 1, got %d", calls.Load())
	}
	// Refresh bypasses cache.
	_ = c.Collect(t.Context(), []AgentSpec{{Name: "agy", Program: "/usr/local/bin/agy", Env: env}}, true)
	if calls.Load() != 2 {
		t.Fatalf("expected refreshed call count 2, got %d", calls.Load())
	}
}

func TestCollectClaudeUsageReconcilesLimitsAndHandles429(t *testing.T) {
	var hitCount atomic.Int32
	var return429 atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-beta") != claudeOAuthBetaHeader {
			t.Errorf("missing anthropic-beta header: %q", r.Header.Get("anthropic-beta"))
		}
		if return429.Load() {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour":      map[string]any{"utilization": 32.5, "resets_at": "2026-11-27T10:00:00Z"},
			"seven_day":      map[string]any{"utilization": 51.0, "resets_at": "2026-12-01T00:00:00Z"},
			"seven_day_opus": map[string]any{"utilization": 18.0, "resets_at": "2026-12-01T00:00:00Z"},
			"limits": []any{
				map[string]any{
					"kind":      "weekly_scoped",
					"percent":   22.0,
					"resets_at": "2026-12-01T00:00:00Z",
					"scope":     map[string]any{"model": map[string]any{"display_name": "Opus"}},
				},
				map[string]any{
					"kind":      "weekly_scoped",
					"percent":   10.0,
					"resets_at": "2026-12-01T00:00:00Z",
					"scope":     map[string]any{"surface": map[string]any{"id": "claude_code", "display_name": "Claude Code"}},
				},
				"malformed-entry-should-be-ignored",
			},
		})
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	c := NewCollector()
	c.Now = func() time.Time { return now }
	c.ClaudeEndpoint = srv.URL
	c.ReadClaudeKeychain = func(context.Context, string, string) (string, error) {
		return `{"claudeAiOauth":{"accessToken":"tok-secret","subscriptionType":"max","rateLimitTier":"default_claude_max_5x"}}`, nil
	}

	res := c.Collect(t.Context(), []AgentSpec{{Name: "claude", Env: map[string]string{}}}, true)
	acc := res.GetAccounts()[0]
	if acc.GetStatus() != "available" || acc.GetPlan() != "Max 5x" {
		t.Fatalf("claude acc = %+v", acc)
	}
	// Expected: 5h, Weekly, Weekly Opus (reconciled to 22% from limits[]), Weekly Claude Code (surface) = 4 limits.
	if len(acc.GetLimits()) != 4 {
		t.Fatalf("limits len = %d, want 4: %+v", len(acc.GetLimits()), acc.GetLimits())
	}
	if acc.GetLimits()[2].GetGroup() != "Opus" || acc.GetLimits()[2].GetUsed() != 22 {
		t.Fatalf("reconciled Opus limit = %+v, want 22%%", acc.GetLimits()[2])
	}
	if acc.GetLimits()[3].GetGroup() != "Claude Code" || acc.GetLimits()[3].GetUsed() != 10 {
		t.Fatalf("surface limit = %+v", acc.GetLimits()[3])
	}

	// 429 triggers Retry-After backoff so subsequent refresh does not hit server until expired.
	return429.Store(true)
	res2 := c.Collect(t.Context(), []AgentSpec{{Name: "claude", Env: map[string]string{}}}, true)
	if res2.GetAccounts()[0].GetStatus() != "error" || !strings.Contains(res2.GetAccounts()[0].GetDetail(), "429") {
		t.Fatalf("expected 429 error, got %+v", res2.GetAccounts()[0])
	}
	hitsAfter429 := hitCount.Load()
	res3 := c.Collect(t.Context(), []AgentSpec{{Name: "claude", Env: map[string]string{}}}, true)
	if hitCount.Load() != hitsAfter429 {
		t.Fatalf("expected backoff to suppress HTTP request, hits went %d -> %d", hitsAfter429, hitCount.Load())
	}
	if !strings.Contains(res3.GetAccounts()[0].GetDetail(), "Rate limited") {
		t.Fatalf("expected rate-limited detail, got %+v", res3.GetAccounts()[0])
	}
}

func TestCollectCodexUsageAndCredits(t *testing.T) {
	codexHome := t.TempDir()
	authJSON := `{"tokens":{"access_token":"codex-tok","account_id":"acct-42"}}`
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(authJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer codex-tok" || r.Header.Get("ChatGPT-Account-Id") != "acct-42" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window": {"used_percent": 14, "limit_window_seconds": 18000, "reset_at": 1795700000},
				"secondary_window": {"used_percent": 48.5, "limit_window_seconds": 604800, "reset_at": 1796200000}
			},
			"code_review_rate_limit": {
				"primary_window": {"used_percent": 5, "limit_window_seconds": 604800, "reset_at": 1796200000}
			},
			"credits": {"balance": "12.50"}
		}`))
	}))
	defer srv.Close()

	c := NewCollector()
	c.CodexEndpoint = srv.URL
	res := c.Collect(t.Context(), []AgentSpec{{Name: "codex", Env: map[string]string{"CODEX_HOME": codexHome, "OPENAI_BASE_URL": ""}}}, true)
	acc := res.GetAccounts()[0]
	if acc.GetStatus() != "available" || acc.GetPlan() != "Pro" {
		t.Fatalf("codex acc = %+v", acc)
	}
	if acc.Credits == nil || acc.GetCredits() != 12.5 {
		t.Fatalf("credits = %v, want 12.5", acc.Credits)
	}
	if len(acc.GetLimits()) != 3 {
		t.Fatalf("limits len = %d, want 3", len(acc.GetLimits()))
	}
	if acc.GetLimits()[0].GetLabel() != "5h" || acc.GetLimits()[0].GetUsed() != 14 {
		t.Fatalf("primary limit = %+v", acc.GetLimits()[0])
	}
	if acc.GetLimits()[1].GetLabel() != "Weekly" || acc.GetLimits()[1].GetUsed() != 48.5 {
		t.Fatalf("secondary limit = %+v", acc.GetLimits()[1])
	}
	if acc.GetLimits()[2].GetGroup() != "Code review" || acc.GetLimits()[2].GetUsed() != 5 {
		t.Fatalf("code review limit = %+v", acc.GetLimits()[2])
	}
}
