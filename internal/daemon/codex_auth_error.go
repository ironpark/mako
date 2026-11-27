package daemon

import (
	"encoding/json"

	"github.com/ironpark/mako/internal/agent/claude"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// observeCodexAuthNotification watches codex app-server notifications for
// structured provider authentication failures and subsequent recoveries on the
// active top-level thread. Runs after handleCodexNotification has updated
// bridge thread binding and turn status.
func (d *Daemon) observeCodexAuthNotification(s *session.Session, b *codexBridge, n codex.Notification, epoch uint64) {
	if s == nil || b == nil || !b.isCurrentEpoch(epoch) {
		return
	}
	switch n.Method {
	case "error":
		var p codex.ErrorNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() || p.WillRetry {
			return
		}
		if authErr := codex.ClassifyTurnError(p.Error); authErr != nil {
			s.ReportAuthError(authErr, b.seqFor(n.EmittedAtMs))
		}

	case "turn/completed":
		var p codex.TurnCompletedErrorNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		if p.Turn.Status == "completed" {
			s.ClearAuthError(b.seqFor(n.EmittedAtMs))
			return
		}
		if p.Turn.Status == "failed" && p.Turn.Error != nil {
			if authErr := codex.ClassifyTurnError(*p.Turn.Error); authErr != nil {
				s.ReportAuthError(authErr, b.seqFor(n.EmittedAtMs))
			}
		}

	case "turn/started":
		var p codex.TurnNotification
		if json.Unmarshal(n.Params, &p) != nil || p.ThreadID != b.threadID() {
			return
		}
		s.ClearAuthError(b.seqFor(n.EmittedAtMs))

	case "thread/started":
		var p codex.ThreadStartedNotification
		if json.Unmarshal(n.Params, &p) != nil || p.Thread.ID == "" || p.Thread.Ephemeral || p.Thread.IsSubagent() {
			return
		}
		if p.Thread.ID == b.threadID() {
			s.ClearAuthError(b.seqFor(n.EmittedAtMs))
		}

	case "account/login/completed":
		var p codex.AccountLoginCompletedNotification
		if json.Unmarshal(n.Params, &p) != nil || !p.Success {
			return
		}
		s.ClearAuthError(b.seqFor(n.EmittedAtMs))
	}
}

func classifyOneShotAuthError(agentName string, stdout, stderr []byte) *protocol.AuthError {
	switch agentName {
	case "claude":
		return claude.ClassifyOneShotError(stdout, stderr)
	case "codex":
		return codex.ClassifyOneShotError(stdout, stderr)
	default:
		return nil
	}
}
