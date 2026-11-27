package claude

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
	// LoginCommand is the host CLI command to re-authenticate Claude Code.
	LoginCommand = "claude auth login"

	loginExpiredSummary  = "Claude Code login expired on the host"
	loginExpiredGuidance = "Run `claude auth login` on the host machine running this session, with the same OS user and Claude configuration. This is separate from Mako node access. After signing in, review the conversation and resend your prompt if needed; Mako has not retried it."

	providerAuthSummary  = "Claude provider authentication failed on the host"
	providerAuthGuidance = "Check this session's provider credentials and configuration on the host machine. If you use Claude Code login, run `claude auth login` with the same OS user and Claude configuration. This is separate from Mako node access. After fixing it, review the conversation and resend your prompt if needed; Mako has not retried it."
)

// claudeOAuthExpiredRe is a narrow fallback matcher for Claude Code's exact
// OAuth refresh failure surface. Anchored at the start of trimmed text so
// MCP prefixes, arbitrary 401s, or quoted conversation text never match.
var claudeOAuthExpiredRe = regexp.MustCompile(`(?i)^Failed to authenticate: OAuth session expired and could not be refreshed(?:[.!]?(?:\s|$))`)

func isClaudeOAuthExpired(text string) bool {
	return claudeOAuthExpiredRe.MatchString(strings.TrimSpace(text))
}

func claudeAuthError(kind string) *protocol.AuthError {
	if kind == protocol.AuthErrorOAuthExpired {
		return &protocol.AuthError{
			Kind:     protocol.AuthErrorOAuthExpired,
			Provider: "claude",
			Summary:  loginExpiredSummary,
			Guidance: loginExpiredGuidance,
			Command:  LoginCommand,
		}
	}
	return &protocol.AuthError{
		Kind:     protocol.AuthErrorProviderAuth,
		Provider: "claude",
		Summary:  providerAuthSummary,
		Guidance: providerAuthGuidance,
		Command:  LoginCommand,
	}
}

// classifyStopFailure classifies a top-level Claude StopFailure hook payload.
// Only explicit `authentication_failed` errors (or an untyped StopFailure whose
// error detail begins with Claude's exact OAuth expiration error) are reported.
func classifyStopFailure(errCode, errType, errMsg string, rawDetails json.RawMessage, lastAssistant string) *protocol.AuthError {
	code := strings.TrimSpace(errCode)
	typ := strings.TrimSpace(errType)
	detail := extractErrorDetailText(rawDetails)

	if code == "authentication_failed" || typ == "authentication_failed" {
		if isClaudeOAuthExpired(detail) || isClaudeOAuthExpired(errMsg) || isClaudeOAuthExpired(lastAssistant) {
			return claudeAuthError(protocol.AuthErrorOAuthExpired)
		}
		return claudeAuthError(protocol.AuthErrorProviderAuth)
	}
	// Any other explicit error code (rate_limit, billing_error, overloaded,
	// server_error, unknown, oauth_org_not_allowed, bare HTTP 401, etc.) is
	// not classified as host OAuth/provider login expiry.
	if code != "" || typ != "" {
		return nil
	}
	if isClaudeOAuthExpired(detail) || isClaudeOAuthExpired(errMsg) {
		return claudeAuthError(protocol.AuthErrorOAuthExpired)
	}
	return nil
}

func extractErrorDetailText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	var obj struct {
		Message      string `json:"message"`
		ErrorMessage string `json:"error_message"`
		Detail       string `json:"detail"`
		RawResponse  string `json:"raw_response"`
	}
	if json.Unmarshal(trimmed, &obj) == nil {
		for _, cand := range []string{obj.Message, obj.ErrorMessage, obj.Detail, obj.RawResponse} {
			if strings.TrimSpace(cand) != "" {
				return cand
			}
		}
	}
	return ""
}

// AuthErrorEvent implements agent.LogAuthErrors for Claude Code's native JSONL
// transcripts. Only top-level error surfaces (`assistant` with
// `error: "authentication_failed"` and `result` with `is_error: true` matching
// OAuth expiry) produce an AuthError; user messages, tool results, subagent /
// sidechain records, MCP failures, and non-auth errors are ignored.
// A subsequent non-synthetic, non-error top-level assistant reply or successful
// result reports Cleared: true so stale auth errors clear automatically.
func (*Agent) AuthErrorEvent(line []byte) agent.LogAuthEvent {
	if !bytes.Contains(line, []byte(`"assistant"`)) && !bytes.Contains(line, []byte(`"result"`)) {
		return agent.LogAuthEvent{}
	}
	var r struct {
		Type             string          `json:"type"`
		Sidechain        bool            `json:"isSidechain"`
		Meta             bool            `json:"isMeta"`
		ParentToolUseID  string          `json:"parent_tool_use_id"`
		ParentToolUseID2 string          `json:"parentToolUseId"`
		Error            string          `json:"error"`
		IsAPIError       bool            `json:"isApiErrorMessage"`
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		Result           string          `json:"result"`
		Errors           []string        `json:"errors"`
		Timestamp        time.Time       `json:"timestamp"`
		Message          json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &r) != nil {
		return agent.LogAuthEvent{}
	}
	if r.Sidechain || r.Meta || r.ParentToolUseID != "" || r.ParentToolUseID2 != "" {
		return agent.LogAuthEvent{}
	}
	switch r.Type {
	case "assistant":
		if r.Error == "authentication_failed" {
			kind := protocol.AuthErrorProviderAuth
			if messageHasOAuthExpiredText(r.Message) {
				kind = protocol.AuthErrorOAuthExpired
			}
			return agent.LogAuthEvent{AuthError: claudeAuthError(kind), At: r.Timestamp}
		}
		if r.Error != "" || r.IsAPIError {
			return agent.LogAuthEvent{}
		}
		if isSuccessfulModelMessage(r.Message) {
			return agent.LogAuthEvent{Cleared: true, At: r.Timestamp}
		}
	case "result":
		if r.IsError {
			if r.Subtype == "success" {
				if isClaudeOAuthExpired(r.Result) {
					return agent.LogAuthEvent{AuthError: claudeAuthError(protocol.AuthErrorOAuthExpired), At: r.Timestamp}
				}
				return agent.LogAuthEvent{}
			}
			for _, e := range r.Errors {
				if isClaudeOAuthExpired(e) {
					return agent.LogAuthEvent{AuthError: claudeAuthError(protocol.AuthErrorOAuthExpired), At: r.Timestamp}
				}
			}
			if len(r.Errors) == 0 && isClaudeOAuthExpired(r.Result) {
				return agent.LogAuthEvent{AuthError: claudeAuthError(protocol.AuthErrorOAuthExpired), At: r.Timestamp}
			}
			return agent.LogAuthEvent{}
		}
		return agent.LogAuthEvent{Cleared: true, At: r.Timestamp}
	}
	return agent.LogAuthEvent{}
}

func messageHasOAuthExpiredText(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil || len(msg.Content) == 0 {
		return false
	}
	var plain string
	if json.Unmarshal(msg.Content, &plain) == nil {
		return isClaudeOAuthExpired(plain)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(msg.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && isClaudeOAuthExpired(b.Text) {
				return true
			}
		}
	}
	return false
}

func isSuccessfulModelMessage(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return false
	}
	// "<synthetic>" stands for a local error/status reply Claude Code wrote itself.
	if strings.HasPrefix(msg.Model, "<") {
		return false
	}
	trimmed := bytes.TrimSpace(msg.Content)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("[]")) && !bytes.Equal(trimmed, []byte(`""`))
}

// ClassifyOneShotError inspects stdout/stderr from a failed `claude --print`
// invocation and returns a safe, fixed AuthError when Claude reported an
// authentication failure or OAuth session expiration.
func ClassifyOneShotError(stdout, stderr []byte) *protocol.AuthError {
	var env struct {
		IsError bool     `json:"is_error"`
		Subtype string   `json:"subtype"`
		Error   string   `json:"error"`
		Result  string   `json:"result"`
		Errors  []string `json:"errors"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &env) == nil && env.IsError {
		if env.Error == "authentication_failed" {
			if isClaudeOAuthExpired(env.Result) {
				return claudeAuthError(protocol.AuthErrorOAuthExpired)
			}
			for _, e := range env.Errors {
				if isClaudeOAuthExpired(e) {
					return claudeAuthError(protocol.AuthErrorOAuthExpired)
				}
			}
			return claudeAuthError(protocol.AuthErrorProviderAuth)
		}
		if env.Error == "" {
			if isClaudeOAuthExpired(env.Result) {
				return claudeAuthError(protocol.AuthErrorOAuthExpired)
			}
			for _, e := range env.Errors {
				if isClaudeOAuthExpired(e) {
					return claudeAuthError(protocol.AuthErrorOAuthExpired)
				}
			}
		}
		return nil
	}
	if isClaudeOAuthExpired(string(stderr)) || isClaudeOAuthExpired(string(stdout)) {
		return claudeAuthError(protocol.AuthErrorOAuthExpired)
	}
	return nil
}
