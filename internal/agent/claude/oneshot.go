package claude

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// WithYolo adds the CLI flag that bypasses all permission checks.
func (*Agent) WithYolo(l *agent.Launch) {
	l.Command = agent.AppendFlagOnce(l.Command, "--dangerously-skip-permissions")
}

// YoloFlags lists the CLI flags that enable YOLO mode.
func (*Agent) YoloFlags() []string {
	return []string{"--dangerously-skip-permissions"}
}

// PrepareOneShot configures Claude Code for a single non-interactive print run.
func (*Agent) PrepareOneShot(l *agent.Launch, req agent.OneShotRequest, _, _ string) (agent.OneShotPrepared, error) {
	cmd := slices.Clone(l.Command)
	cmd = append(cmd, "--print", "--no-session-persistence")
	if !slices.Contains(cmd, "--dangerously-skip-permissions") && !slices.Contains(cmd, "--permission-prompts") {
		cmd = append(cmd, "--permission-prompts", "none")
	}
	prompt := req.Prompt
	if req.Format == "json" {
		cmd = append(cmd, "--output-format", "json")
		if schema := strings.TrimSpace(req.JSONSchema); schema != "" {
			cmd = append(cmd, "--json-schema", schema)
		} else {
			prompt = agent.JSONPromptSuffix(prompt, "json", "")
		}
		l.Command = cmd
		return agent.OneShotPrepared{
			Stdin: prompt,
			Parse: func(stdout, stderr []byte) (string, error) {
				var env struct {
					IsError          bool            `json:"is_error"`
					Result           string          `json:"result"`
					StructuredOutput json.RawMessage `json:"structured_output"`
					Errors           []string        `json:"errors"`
				}
				if json.Unmarshal(stdout, &env) == nil {
					if env.IsError {
						msg := strings.TrimSpace(env.Result)
						if msg == "" {
							msg = strings.Join(env.Errors, "; ")
						}
						if msg == "" {
							msg = strings.TrimSpace(string(stderr))
						}
						return "", errors.New(msg)
					}
					if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
						return agent.CompactJSON(env.StructuredOutput), nil
					}
					if strings.TrimSpace(env.Result) != "" {
						return agent.ExtractJSON(env.Result)
					}
				}
				return agent.ExtractJSON(string(stdout))
			},
		}, nil
	}
	cmd = append(cmd, "--output-format", "text")
	l.Command = cmd
	return agent.OneShotPrepared{
		Stdin: prompt,
		Parse: func(stdout, _ []byte) (string, error) {
			return strings.TrimSpace(string(stdout)), nil
		},
	}, nil
}
