// Package claude is mako's driver for Claude Code: its options, its MCP
// configuration, its settings hooks and status line, its project logs and
// the subagents' logs beside them.
package claude

import (
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is Claude Code.
type Agent struct {
	agent.Flags
	agent.CommonHooks
}

// New is the Claude Code driver.
func New() *Agent {
	return &Agent{Flags: agent.Flags{
		PinFlag: "--session-id", ResumeFlags: []string{"--resume", "-r"}, ForkFlag: "--fork-session",
		PickerValues: []string{"--from-pr"}, PickerFlags: []string{"--continue", "-c"},
		Values:   strings.Fields("--model --effort --settings --setting-sources --system-prompt --system-prompt-file --append-system-prompt --append-system-prompt-file --permission-mode --permission-prompts --permission-prompt-tool --plugin-dir --plugin-url --agent --agents --name -n --debug-file --input-format --output-format --json-schema --max-budget-usd --max-turns --fallback-model --system-prompt-snapshot --remote-control-session-name-prefix"),
		Variadic: strings.Fields("--add-dir --allowedTools --allowed-tools --disallowedTools --disallowed-tools --tools --mcp-config --betas --file"),
	}}
}

func (*Agent) Info() agent.Info {
	return agent.Info{
		Name: "claude", Programs: []string{"claude"}, Command: []string{"claude"},
		Icon: "agent-claude", Color: color.NRGBA{0xd9, 0x77, 0x57, 0xff},
	}
}

// WithServers writes the servers as Claude Code's .mcp.json reads them, in
// dir and named for session, and names the file on the command line;
// cleanup removes it. It holds the servers' keys, so only the user reads it.
func (*Agent) WithServers(command []string, servers []agent.Server, dir, session string) ([]string, func(), error) {
	type server struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}
	cfg := struct {
		Servers map[string]server `json:"mcpServers"`
	}{map[string]server{}}
	for _, s := range servers {
		cfg.Servers[s.ID] = server{Type: "stdio", Command: s.Command, Args: s.ArgList(), Env: s.Env}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return command, func() {}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return command, func() {}, err
	}
	path := filepath.Join(dir, "mcp-"+session+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return command, func() {}, err
	}
	// The = form, as the flag takes any number of values otherwise and
	// would swallow the arguments after it.
	out := append([]string{command[0], "--mcp-config=" + path}, command[1:]...)
	return out, func() { os.Remove(path) }, nil
}
