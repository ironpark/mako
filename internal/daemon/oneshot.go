package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
	"github.com/ironpark/mako/internal/transport"
)

const (
	defaultOneShotTimeout = 3 * time.Minute
	maxOneShotPromptBytes = 128 << 10
	maxOneShotOutputBytes = 4 << 20
)

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.limit {
		avail := b.limit - b.buf.Len()
		if len(p) > avail {
			b.buf.Write(p[:avail])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (d *Daemon) handleRunAgentPrompt(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentPromptResult, func(req *protocol.RunAgentPrompt) (*protocol.AgentPromptResult, error) {
		return d.runAgentPrompt(ctx, req)
	})
}

type oneShotOptions struct {
	stripYolo bool
}

func (d *Daemon) runAgentPrompt(ctx context.Context, req *protocol.RunAgentPrompt) (*protocol.AgentPromptResult, error) {
	return d.runAgentPromptWith(ctx, req, oneShotOptions{})
}

func (d *Daemon) runAgentPromptWith(ctx context.Context, req *protocol.RunAgentPrompt, opts oneShotOptions) (*protocol.AgentPromptResult, error) {
	if strings.TrimSpace(req.GetPrompt()) == "" {
		return nil, errors.New("prompt is required")
	}
	if len(req.GetPrompt()) > maxOneShotPromptBytes {
		return nil, fmt.Errorf("prompt exceeds %d bytes", maxOneShotPromptBytes)
	}
	format := strings.ToLower(strings.TrimSpace(req.GetFormat()))
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "json" {
		return nil, fmt.Errorf("unsupported format %q (want \"text\" or \"json\")", format)
	}

	agentName := strings.TrimSpace(req.GetAgent())
	if agentName == "" {
		return nil, errors.New("agent is required")
	}
	configured := d.agents()
	if a, ok := agentspec.Lookup(agentName, configured); ok {
		if profName := strings.TrimSpace(req.GetProfile()); profName != "" {
			if p, ok := a.Profile(profName); ok && !p.TemplateAllowed() {
				return nil, fmt.Errorf("profile %q is not enabled for prompt templates", profName)
			}
		} else if !a.TemplateAllowed() {
			return nil, fmt.Errorf("agent %q default configuration is not enabled for prompt templates", agentName)
		}
	}
	launch, err := agentspec.ResolveWith(agentName, req.GetProfile(), nil, nil, agentspec.ResolveOptions{
		Model:  req.GetModel(),
		Effort: req.GetEffort(),
		Yolo:   req.Yolo,
	}, configured)
	if err != nil {
		return nil, err
	}
	if opts.stripYolo && len(launch.Command) > 1 {
		rest, _ := agentspec.LiftYoloArgs(agentName, launch.Command[1:])
		launch.Command = append(launch.Command[:1:1], rest...)
	}
	if d.isolateHome() {
		if err := agentspec.ApplyIsolatedHome(agentName, &launch); err != nil {
			return nil, err
		}
	}

	workDir := req.GetProject()
	if workDir == "" {
		workDir, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	if realDir, err := filepath.EvalSymlinks(workDir); runtime.GOOS == "windows" && err == nil && strings.EqualFold(realDir, workDir) {
		workDir = realDir
	}
	launch.SetEnv("AGENT_MAKO_PROJECT", workDir)
	launch.SetEnv("AGENT_MAKO_MODE", "subagent")
	if nodeID := d.config().NodeID; nodeID != "" {
		launch.SetEnv("AGENT_MAKO_NODE", nodeID)
	}
	if profName := strings.TrimSpace(req.GetProfile()); profName != "" {
		launch.SetEnv("AGENT_MAKO_PROFILE", profName)
	}
	if parentID := strings.TrimSpace(req.GetParentSessionId()); parentID != "" {
		launch.SetEnv("AGENT_MAKO_PARENT", parentID)
	}
	launch.Command = withProjectDirs(agentName, launch.Command, d.projectDirs(workDir))

	if len(launch.Command) == 0 {
		return nil, fmt.Errorf("agent %q has an empty command", agentName)
	}
	entry, ok := agents.Native(agentName, launch.Command[0])
	if !ok {
		return nil, fmt.Errorf("agent %q cannot run one-shot prompts through a custom wrapper", agentName)
	}
	osDriver, ok := entry.Driver.(agent.OneShot)
	if !ok {
		return nil, fmt.Errorf("agent %q does not support one-shot prompts", agentName)
	}

	runDir, err := config.RunDir(d.dir)
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp(runDir, "oneshot-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	prep, err := osDriver.PrepareOneShot(&launch.Launch, agent.OneShotRequest{
		Prompt:     req.GetPrompt(),
		Format:     format,
		JSONSchema: req.GetJsonSchema(),
	}, workDir, tempDir)
	if err != nil {
		return nil, err
	}
	if prep.Cleanup != nil {
		defer prep.Cleanup()
	}

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, defaultOneShotTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, launch.Command[0], launch.Command[1:]...)
	cmd.Dir = workDir
	cmd.Env = agent.BuildChildEnv(os.Environ(), launch.Env)
	if prep.Stdin != "" {
		cmd.Stdin = strings.NewReader(prep.Stdin)
	}
	stdout := &limitedBuffer{limit: maxOneShotOutputBytes}
	stderr := &limitedBuffer{limit: maxOneShotOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := safeexec.Run(cmd); err != nil {
		if runCtx.Err() != nil {
			return nil, runCtx.Err()
		}
		snippet := strings.TrimSpace(stderr.buf.String())
		if snippet == "" {
			snippet = strings.TrimSpace(stdout.buf.String())
		}
		if len(snippet) > 512 {
			snippet = snippet[:512] + "..."
		}
		if snippet != "" {
			return nil, fmt.Errorf("%s failed: %w: %s", agentName, err, snippet)
		}
		return nil, fmt.Errorf("%s failed: %w", agentName, err)
	}

	out, err := prep.Parse(stdout.buf.Bytes(), stderr.buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s output parse failed: %w", agentName, err)
	}
	return &protocol.AgentPromptResult{Output: out, Format: format}, nil
}
