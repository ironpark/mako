package codex

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// WithYolo adds the CLI flag that bypasses approvals and sandboxing.
func (*Agent) WithYolo(l *agent.Launch) {
	if slices.Contains(l.Command, "--dangerously-bypass-approvals-and-sandbox") || slices.Contains(l.Command, "--yolo") {
		return
	}
	l.Command = append(l.Command, "--dangerously-bypass-approvals-and-sandbox")
}

// YoloFlags lists the CLI flags that enable YOLO mode.
func (*Agent) YoloFlags() []string {
	return []string{"--dangerously-bypass-approvals-and-sandbox", "--yolo"}
}

// PermissionModes lists Codex's startup permission modes (excluding YOLO).
func (*Agent) PermissionModes() []string {
	return []string{"readOnly", "acceptEdits", "auto"}
}

// WithPermission applies Codex's startup approval and sandbox flags for mode.
func (*Agent) WithPermission(l *agent.Launch, mode string) {
	switch mode {
	case "readOnly":
		l.Command = append(slices.Clone(l.Command), "-a", "on-request", "-s", "read-only")
	case "acceptEdits":
		l.Command = append(slices.Clone(l.Command), "-a", "on-request", "-s", "workspace-write")
	case "auto":
		l.Command = append(slices.Clone(l.Command), "--approve-for-me")
	}
}

// PrepareOneShot configures Codex for a single non-interactive exec run.
func (*Agent) PrepareOneShot(l *agent.Launch, req agent.OneShotRequest, _, tempDir string) (agent.OneShotPrepared, error) {
	ownDir := false
	if tempDir == "" {
		var err error
		tempDir, err = os.MkdirTemp("", "mako-codex-oneshot-*")
		if err != nil {
			return agent.OneShotPrepared{}, err
		}
		ownDir = true
	}
	outPath := filepath.Join(tempDir, "codex-out.txt")
	var schemaPath string
	prompt := req.Prompt
	cmd := append(slices.Clone(l.Command), "exec", "--ephemeral", "--skip-git-repo-check", "-o", outPath)
	if req.Format == "json" {
		if schema := strings.TrimSpace(req.JSONSchema); schema != "" {
			schemaPath = filepath.Join(tempDir, "codex-schema.json")
			if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
				if ownDir {
					_ = os.RemoveAll(tempDir)
				}
				return agent.OneShotPrepared{}, err
			}
			cmd = append(cmd, "--output-schema", schemaPath)
		} else {
			prompt = agent.JSONPromptSuffix(prompt, "json", "")
		}
	}
	cmd = append(cmd, "-")
	l.Command = cmd
	cleanup := func() {
		_ = os.Remove(outPath)
		if schemaPath != "" {
			_ = os.Remove(schemaPath)
		}
		if ownDir {
			_ = os.RemoveAll(tempDir)
		}
	}
	return agent.OneShotPrepared{
		Stdin:   prompt,
		Cleanup: cleanup,
		Parse: func(stdout, _ []byte) (string, error) {
			raw, err := os.ReadFile(outPath)
			text := strings.TrimSpace(string(raw))
			if err != nil || text == "" {
				text = strings.TrimSpace(string(stdout))
			}
			if req.Format == "json" {
				return agent.ExtractJSON(text)
			}
			return text, nil
		},
	}, nil
}
