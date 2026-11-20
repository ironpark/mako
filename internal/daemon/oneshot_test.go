//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestRunAgentPromptTextAndJSON(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
outfmt="text"
model=""
yolo="0"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-format) outfmt="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --dangerously-skip-permissions) yolo="1"; shift ;;
    *) shift ;;
  esac
done
prompt="$(cat)"
if [ "$outfmt" = "json" ]; then
  printf '{"is_error":false,"structured_output":{"model":"%s","yolo":"%s","ok":true}}' "$model" "$yolo"
else
  printf 'text:%s:%s:%s' "$model" "$yolo" "$prompt"
fi
`
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d := meshDaemonWith(t, "oneshot", func(cfg *config.Config) {
		on := true
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {
				Command: []string{claudeBin},
				Enabled: &on,
				Profiles: []agentspec.Profile{
					{Name: "fast", Model: "sonnet"},
				},
			},
		}
	})
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	yolo := true
	textRes, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		Agent:   "claude",
		Profile: "fast",
		Yolo:    &yolo,
		Project: workDir,
		Prompt:  "hello world",
		Format:  "text",
	})
	if err != nil {
		t.Fatalf("text RunAgentPrompt: %v", err)
	}
	if textRes.GetFormat() != "text" || textRes.GetOutput() != "text:sonnet:1:hello world" {
		t.Fatalf("text result = %+v", textRes)
	}

	jsonRes, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		Agent:   "claude",
		Model:   "opus",
		Project: workDir,
		Prompt:  "return json",
		Format:  "json",
	})
	if err != nil {
		t.Fatalf("json RunAgentPrompt: %v", err)
	}
	if jsonRes.GetFormat() != "json" {
		t.Fatalf("json format = %q", jsonRes.GetFormat())
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonRes.GetOutput()), &parsed); err != nil {
		t.Fatalf("unmarshal json output %q: %v", jsonRes.GetOutput(), err)
	}
	if parsed["model"] != "opus" || parsed["yolo"] != "0" || parsed["ok"] != true {
		t.Fatalf("parsed json = %+v", parsed)
	}
}

func TestRunAgentPromptValidation(t *testing.T) {
	d := meshDaemon(t, "oneshot-err")
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{Agent: "claude", Prompt: "  "}); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("empty prompt err = %v", err)
	}
	if _, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{Agent: "claude", Prompt: "hi", Format: "xml"}); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("bad format err = %v", err)
	}
	if _, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{Agent: "shell", Prompt: "hi"}); err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("unsupported agent err = %v", err)
	}
}

func TestRunAgentPromptAllowTemplateAndEnv(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
cat >/dev/null
printf 'proj=%s;mode=%s' "$AGENT_MAKO_PROJECT" "$AGENT_MAKO_MODE"
`
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d := meshDaemonWith(t, "oneshot-switch", func(cfg *config.Config) {
		on := true
		off := false
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {
				Command: []string{claudeBin},
				Enabled: &on,
				Profiles: []agentspec.Profile{
					{Name: "tmpl-ok", Model: "sonnet"},
					{Name: "no-tmpl", Model: "sonnet", AllowTemplate: &off},
				},
			},
		}
	})
	c := dialDaemon(t, d)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Disallowed profile is rejected.
	if _, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		Agent: "claude", Profile: "no-tmpl", Project: workDir, Prompt: "hi",
	}); err == nil || !strings.Contains(err.Error(), "not enabled for prompt templates") {
		t.Fatalf("expected template restriction error, got %v", err)
	}

	// Allowed profile runs with AGENT_MAKO_PROJECT and AGENT_MAKO_MODE=subagent.
	res, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		Agent: "claude", Profile: "tmpl-ok", Project: workDir, Prompt: "hi",
	})
	if err != nil {
		t.Fatalf("RunAgentPrompt: %v", err)
	}
	want := "proj=" + workDir + ";mode=subagent"
	if res.GetOutput() != want {
		t.Fatalf("output = %q, want %q", res.GetOutput(), want)
	}
}
