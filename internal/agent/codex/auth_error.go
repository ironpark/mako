package codex

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	// LoginCommand is the host CLI command to re-authenticate Codex.
	LoginCommand = "codex login"

	loginExpiredSummary  = "Codex login expired on the host"
	loginExpiredGuidance = "Run `codex login` on the host machine running this session, with the same OS user and Codex configuration. This is separate from Mako node access. After signing in, review the conversation and resend your prompt if needed; Mako has not retried it."

	providerAuthSummary  = "Codex provider authentication failed on the host"
	providerAuthGuidance = "Check this session's provider credentials and configuration on the host machine. If you use Codex login, run `codex login` with the same OS user and Codex configuration. This is separate from Mako node access. After fixing it, review the conversation and resend your prompt if needed; Mako has not retried it."
)

// TurnError mirrors codex app-server's v2 TurnError payload carried by `error`
// and `turn/completed` notifications.
type TurnError struct {
	Message           string          `json:"message"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
	AdditionalDetails string          `json:"additionalDetails,omitempty"`
}

// ErrorNotification mirrors codex app-server's `error` notification params.
type ErrorNotification struct {
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	WillRetry bool      `json:"willRetry"`
	Error     TurnError `json:"error"`
}

// TurnCompletedErrorNotification extracts Turn.Status and Turn.Error from
// `turn/completed` notifications without coupling to other Turn fields.
type TurnCompletedErrorNotification struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string     `json:"id"`
		Status string     `json:"status"`
		Error  *TurnError `json:"error,omitempty"`
	} `json:"turn"`
}

// AccountLoginCompletedNotification mirrors codex app-server's
// `account/login/completed` notification params.
type AccountLoginCompletedNotification struct {
	LoginID string `json:"loginId,omitempty"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// codexOAuthExpiredFallbackRe is a narrow anchored fallback matcher for Codex's
// OAuth/access-token refresh failures when no structured codexErrorInfo code
// is present on an explicit error surface. Never matches arbitrary 401s, MCP
// errors, or quoted conversation text.
var codexOAuthExpiredFallbackRe = regexp.MustCompile(
	`(?i)^(?:your access token could not be refreshed|failed to refresh (?:access |oauth |chatgpt )?token|authentication token has been invalidated|provided authentication token is expired)(?:[.:!,;\s]|$)`,
)

func isCodexOAuthExpiredFallback(text string) bool {
	return codexOAuthExpiredFallbackRe.MatchString(strings.TrimSpace(text))
}

// isCodexOAuthExpiredDetail distinguishes ChatGPT/Codex OAuth session expiry
// from generic API-key / custom-provider rejection once codexErrorInfo has
// already been verified as "unauthorized".
func isCodexOAuthExpiredDetail(message, details string) bool {
	combined := strings.ToLower(strings.TrimSpace(message + "\n" + details))
	if combined == "" {
		return false
	}
	for _, needle := range []string{
		"refresh_token_reused",
		"refresh_token_expired",
		"refresh_token_invalidated",
		"access token could not be refreshed",
		"failed to refresh",
		"oauth",
		"token has been invalidated",
		"token expired",
		"session expired",
		"sign in again",
		"log in again",
		"codex login",
	} {
		if strings.Contains(combined, needle) {
			return true
		}
	}
	return false
}

func codexAuthError(kind string) *protocol.AuthError {
	if kind == protocol.AuthErrorOAuthExpired {
		return &protocol.AuthError{
			Kind:     protocol.AuthErrorOAuthExpired,
			Provider: "codex",
			Summary:  loginExpiredSummary,
			Guidance: loginExpiredGuidance,
			Command:  LoginCommand,
		}
	}
	return &protocol.AuthError{
		Kind:     protocol.AuthErrorProviderAuth,
		Provider: "codex",
		Summary:  providerAuthSummary,
		Guidance: providerAuthGuidance,
		Command:  LoginCommand,
	}
}

// ClassifyTurnError classifies an explicit Codex TurnError from `error`,
// `turn/completed`, or rollout `event_msg` (`payload.type == "error"`).
// Only structured `codexErrorInfo: "unauthorized"` (or an untyped/other error
// matching Codex's exact anchored token-refresh failure message) produces an
// AuthError. Non-auth codes (rateLimitExceeded, usageLimitExceeded,
// contextWindowExceeded, sandboxError, httpConnectionFailed with 401, etc.)
// are ignored.
func ClassifyTurnError(e TurnError) *protocol.AuthError {
	raw := bytes.TrimSpace(e.CodexErrorInfo)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var code string
		if json.Unmarshal(raw, &code) != nil {
			// Structured object variant such as {"httpConnectionFailed":{"httpStatusCode":401}}
			// or {"responseStreamDisconnected":...} is NOT Codex's structured "unauthorized".
			return nil
		}
		code = strings.TrimSpace(code)
		if strings.EqualFold(code, "unauthorized") {
			if isCodexOAuthExpiredDetail(e.Message, e.AdditionalDetails) {
				return codexAuthError(protocol.AuthErrorOAuthExpired)
			}
			return codexAuthError(protocol.AuthErrorProviderAuth)
		}
		if code != "" && !strings.EqualFold(code, "other") {
			return nil
		}
	}
	if isCodexOAuthExpiredFallback(e.Message) || isCodexOAuthExpiredFallback(e.AdditionalDetails) {
		return codexAuthError(protocol.AuthErrorOAuthExpired)
	}
	return nil
}

// AuthErrorEvent implements agent.LogAuthErrors for Codex rollout JSONL logs.
// Only `event_msg` control records are inspected; `response_item` conversation
// content (user prompts, assistant replies, tool calls/outputs) never triggers.
func (*Agent) AuthErrorEvent(line []byte) agent.LogAuthEvent {
	if !bytes.Contains(line, []byte(`"event_msg"`)) {
		return agent.LogAuthEvent{}
	}
	var r struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			Type               string          `json:"type"`
			Message            string          `json:"message"`
			CodexErrorInfo     json.RawMessage `json:"codex_error_info"`
			CodexErrorInfoAlt  json.RawMessage `json:"codexErrorInfo"`
			AdditionalDetails  string          `json:"additional_details"`
			AdditionalDetails2 string          `json:"additionalDetails"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "event_msg" {
		return agent.LogAuthEvent{}
	}
	switch r.Payload.Type {
	case "error":
		info := r.Payload.CodexErrorInfo
		if len(bytes.TrimSpace(info)) == 0 {
			info = r.Payload.CodexErrorInfoAlt
		}
		details := r.Payload.AdditionalDetails
		if details == "" {
			details = r.Payload.AdditionalDetails2
		}
		if authErr := ClassifyTurnError(TurnError{
			Message:           r.Payload.Message,
			CodexErrorInfo:    info,
			AdditionalDetails: details,
		}); authErr != nil {
			return agent.LogAuthEvent{AuthError: authErr, At: r.Timestamp}
		}
	case "task_started", "task_complete":
		return agent.LogAuthEvent{Cleared: true, At: r.Timestamp}
	}
	return agent.LogAuthEvent{}
}

// ClassifyOneShotError inspects stdout/stderr from a failed `codex exec`
// invocation for Codex's exact anchored token-refresh failure message.
func ClassifyOneShotError(stdout, stderr []byte) *protocol.AuthError {
	if isCodexOAuthExpiredFallback(string(stderr)) || isCodexOAuthExpiredFallback(string(stdout)) {
		return codexAuthError(protocol.AuthErrorOAuthExpired)
	}
	return nil
}
