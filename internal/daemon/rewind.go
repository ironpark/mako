package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent/claude"
	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/ironpark/mako/internal/transport"
)

const rewindTimeout = 30 * time.Second

func (d *Daemon) handleRewindSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.RewindSession) (*protocol.SessionInfo, error) {
		return d.rewindSession(ctx, r)
	})
}

func (d *Daemon) rewindSession(ctx context.Context, req *protocol.RewindSession) (*protocol.SessionInfo, error) {
	sessionID := strings.TrimSpace(req.GetSessionId())
	if sessionID == "" {
		return nil, errors.New("rewind: missing session_id")
	}

	d.sessionMu.Lock()
	live, hasLive := d.sessions.get(sessionID)
	saved, hasSaved := d.saved[sessionID]
	d.sessionMu.Unlock()
	if !hasLive && !hasSaved {
		return nil, fmt.Errorf("unknown session %q", sessionID)
	}

	var (
		info           *protocol.SessionInfo
		transcriptPath string
		baseReq        *protocol.OpenSession
	)
	if hasLive {
		info = live.Info()
		transcriptPath = live.TranscriptPath()
	} else {
		info = proto.CloneOf(saved.Info)
		transcriptPath = saved.TranscriptPath
	}
	if transcriptPath == "" && hasSaved {
		transcriptPath = saved.TranscriptPath
	}
	if hasSaved && saved.Request != nil {
		baseReq = proto.CloneOf(saved.Request)
	} else {
		baseReq = &protocol.OpenSession{
			Agent:   info.GetAgent(),
			Profile: info.GetProfile(),
			Project: info.GetProject(),
			Cols:    info.GetCols(),
			Rows:    info.GetRows(),
		}
	}

	agentName := info.GetAgent()
	if agentName != "claude" && agentName != "codex" {
		return nil, fmt.Errorf("conversation rewind is not supported for %q sessions", agentName)
	}

	nextReq := proto.CloneOf(baseReq)
	nextReq.NodeId = ""
	nextReq.Prompt = ""
	nextReq.ParentSessionId = ""
	nextReq.Name = ""
	if nextReq.Project == "" {
		nextReq.Project = info.GetProject()
	}
	if nextReq.Agent == "" {
		nextReq.Agent = agentName
	}

	forkedName := ""
	if origName := strings.TrimSpace(info.GetName()); origName != "" {
		forkedName = origName + " (rewind)"
	}

	userTurnIdx := int(req.GetUserTurnIndex())
	if userTurnIdx < 0 {
		return nil, fmt.Errorf("rewind: invalid user_turn_index %d", userTurnIdx)
	}

	tctx, cancel := context.WithTimeout(ctx, rewindTimeout)
	defer cancel()

	var (
		forkedID   string
		forkedPath string
		zeroTurns  bool
		err        error
	)

	switch agentName {
	case "claude":
		forkedID, forkedPath, zeroTurns, err = d.rewindClaudeSession(tctx, info, nextReq, transcriptPath, req.GetMessageId(), userTurnIdx)
	case "codex":
		forkedID, forkedPath, zeroTurns, err = d.rewindCodexSession(tctx, sessionID, info, nextReq, userTurnIdx)
	}
	if err != nil {
		return nil, err
	}

	if zeroTurns {
		nextReq.Name = forkedName
		s, err := d.openSession(ctx, nextReq)
		if err != nil {
			return nil, err
		}
		return d.withHead(s.Info()), nil
	}

	s, err := d.openForkedSession(ctx, nextReq, forkedName, forkedID, forkedPath, saved.PinnedRepo, saved.PinnedRemote)
	if err != nil {
		return nil, err
	}
	return d.withHead(s.Info()), nil
}

func (d *Daemon) rewindClaudeSession(
	ctx context.Context,
	info *protocol.SessionInfo,
	nextReq *protocol.OpenSession,
	transcriptPath, messageID string,
	userTurnIndex int,
) (forkedID, forkedPath string, zeroTurns bool, err error) {
	if userTurnIndex == 0 && strings.TrimSpace(messageID) == "" {
		return "", "", true, nil
	}
	if transcriptPath == "" {
		if info.GetConversationId() == "" {
			return "", "", false, errors.New("claude rewind: session has no conversation to rewind")
		}
		callEnv := agentspec.IsolatedHomeEnvForAgent("claude", d.isolateHome(), d.agents())
		env := transcript.Env(os.Environ(), callEnv)
		transcriptPath, err = transcript.Locate(ctx, info, env)
		if err != nil {
			return "", "", false, fmt.Errorf("claude rewind: locate transcript: %w", err)
		}
		if transcriptPath == "" {
			return "", "", false, errors.New("claude rewind: conversation transcript not found")
		}
	}
	return claude.ForkTranscriptBefore(transcriptPath, messageID, userTurnIndex)
}

func (d *Daemon) rewindCodexSession(
	ctx context.Context,
	sessionID string,
	info *protocol.SessionInfo,
	nextReq *protocol.OpenSession,
	userTurnIndex int,
) (forkedID, forkedPath string, zeroTurns bool, err error) {
	if userTurnIndex == 0 {
		return "", "", true, nil
	}

	launch, err := agentspec.ResolveWith(nextReq.GetAgent(), nextReq.GetProfile(), nextReq.GetArgs(), nextReq.GetEnv(), agentspec.ResolveOptions{
		Model:  nextReq.GetModel(),
		Effort: nextReq.GetEffort(),
		Yolo:   nextReq.Yolo,
	}, d.agents())
	if err != nil {
		return "", "", false, fmt.Errorf("codex rewind: resolve launch: %w", err)
	}
	if d.isolateHome() {
		if err := agentspec.ApplyIsolatedHome("codex", &launch); err != nil {
			return "", "", false, fmt.Errorf("codex rewind: isolated home: %w", err)
		}
	}
	if len(launch.Command) == 0 {
		return "", "", false, errors.New("codex rewind: missing codex command")
	}
	if _, native := agents.Native("codex", launch.Command[0]); !native {
		return "", "", false, errors.New("codex rewind: requires native codex CLI")
	}
	split := codex.SplitRemoteLaunch(launch.Command)

	b := d.codexBridge(sessionID)
	threadID := info.GetConversationId()
	if b != nil && b.threadID() != "" {
		threadID = b.threadID()
	}
	if threadID == "" {
		return "", "", false, errors.New("codex rewind: session has no conversation to rewind")
	}

	opts := codex.RewindThreadOptions{
		ThreadID:       threadID,
		UserTurnIndex:  userTurnIndex,
		Cwd:            cmp.Or(split.Cwd, info.GetProject()),
		Model:          split.Model,
		ApprovalPolicy: split.ApprovalPolicy,
		Sandbox:        split.Sandbox,
	}

	var res codex.RewindThreadResult
	if client := b.currentClient(); client != nil {
		opts.ServerUserAgent = b.userAgent()
		res, err = codex.RewindThread(ctx, client, opts)
	} else {
		res, err = codex.RewindThreadStdio(ctx, split.Program, launch.Env, opts)
	}
	if err != nil {
		return "", "", false, err
	}
	return res.ThreadID, res.Path, res.ZeroTurns, nil
}

// openForkedSession starts a brand-new Mako session bound to a freshly forked
// conversation ID and transcript path.
func (d *Daemon) openForkedSession(
	ctx context.Context,
	req *protocol.OpenSession,
	name, conversationID, transcriptPath string,
	pinnedRepo, pinnedRemote *string,
) (*session.Session, error) {
	if conversationID == "" {
		return nil, errors.New("rewind: forked conversation ID is empty")
	}
	id := newSessionID()
	nodeID := d.config().NodeID
	seed := savedSession{
		Info: &protocol.SessionInfo{
			Id:             id,
			NodeId:         nodeID,
			Agent:          req.GetAgent(),
			Profile:        req.GetProfile(),
			Project:        req.GetProject(),
			ConversationId: conversationID,
			Name:           name,
			State:          protocol.StateStopped,
			Caps:           agents.Caps(req.GetAgent()),
		},
		Request:        proto.CloneOf(req),
		TranscriptPath: transcriptPath,
		PinnedRepo:     cloneStringPtr(pinnedRepo),
		PinnedRemote:   cloneStringPtr(pinnedRemote),
	}

	d.sessionMu.Lock()
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	d.saved[id] = seed
	d.sessionMu.Unlock()

	s, err := d.startSession(ctx, id, req)
	if err != nil {
		d.sessionMu.Lock()
		delete(d.saved, id)
		d.sessionMu.Unlock()
		return nil, err
	}
	return s, nil
}
