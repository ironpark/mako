package agy

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// WithYolo adds the CLI flag that auto-approves tool permission requests.
func (*Agent) WithYolo(l *agent.Launch) {
	l.Command = agent.AppendFlagOnce(l.Command, "--dangerously-skip-permissions")
}

// YoloFlags lists the CLI flags that enable YOLO mode.
func (*Agent) YoloFlags() []string {
	return []string{"--dangerously-skip-permissions"}
}

// PermissionModes lists agy's startup execution modes (excluding YOLO).
func (*Agent) PermissionModes() []string {
	return []string{"auto", "acceptEdits", "plan"}
}

// WithPermission adds --mode for supported agy startup modes.
func (*Agent) WithPermission(l *agent.Launch, mode string) {
	switch mode {
	case "auto":
		l.Command = append(slices.Clone(l.Command), "--mode", "auto")
	case "acceptEdits":
		l.Command = append(slices.Clone(l.Command), "--mode", "accept-edits")
	case "plan":
		l.Command = append(slices.Clone(l.Command), "--mode", "plan")
	}
}

// HasYoloFlag reports whether cmd enables --dangerously-skip-permissions as a CLI flag.
func HasYoloFlag(cmd []string) bool {
	a := New()
	for i := 1; i < len(cmd); i++ {
		arg := cmd[i]
		if arg == "--" {
			return false
		}
		if arg == "--dangerously-skip-permissions" || arg == "--dangerously-skip-permissions=true" {
			return true
		}
		name, _, hasEq := strings.Cut(arg, "=")
		if !hasEq && (slices.Contains(a.Values, name) || slices.Contains(a.Inputs, name) || slices.Contains(a.ResumeFlags, name)) {
			i++
		}
	}
	return false
}

// PrepareOneShot configures agy for a single non-interactive print run.
func (*Agent) PrepareOneShot(l *agent.Launch, req agent.OneShotRequest, _, _ string) (agent.OneShotPrepared, error) {
	cmd := slices.Clone(l.Command)
	if req.NoTools && !slices.Contains(cmd, "--disable-slash-commands") {
		cmd = append(cmd, "--disable-slash-commands")
	}
	prompt := req.Prompt
	if req.Format == "json" {
		cmd = append(cmd, "--output-format", "json")
		if schema := strings.TrimSpace(req.JSONSchema); schema != "" {
			cmd = append(cmd, "--json-schema", schema)
		} else {
			prompt = agent.JSONPromptSuffix(prompt, "json", "")
		}
		cmd = append(cmd, "--print", prompt)
		l.Command = cmd
		return agent.OneShotPrepared{
			Parse: func(stdout, stderr []byte) (string, error) {
				var env struct {
					IsError          bool            `json:"is_error"`
					Error            string          `json:"error"`
					Result           json.RawMessage `json:"result"`
					Response         json.RawMessage `json:"response"`
					StructuredOutput json.RawMessage `json:"structured_output"`
				}
				if json.Unmarshal(stdout, &env) == nil {
					if env.IsError || strings.TrimSpace(env.Error) != "" {
						msg := strings.TrimSpace(env.Error)
						if msg == "" {
							msg = strings.TrimSpace(string(stderr))
						}
						if msg == "" {
							msg = "agy failed"
						}
						return "", errors.New(msg)
					}
					if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
						return agent.CompactJSON(env.StructuredOutput), nil
					}
					for _, inner := range []json.RawMessage{env.Result, env.Response} {
						if len(inner) == 0 || string(inner) == "null" {
							continue
						}
						var s string
						if json.Unmarshal(inner, &s) == nil {
							return agent.ExtractJSON(s)
						}
						return agent.CompactJSON(inner), nil
					}
				}
				return agent.ExtractJSON(string(stdout))
			},
		}, nil
	}
	cmd = append(cmd, "--output-format", "text", "--print", prompt)
	l.Command = cmd
	return agent.OneShotPrepared{
		Parse: func(stdout, _ []byte) (string, error) {
			return strings.TrimSpace(string(stdout)), nil
		},
	}, nil
}
