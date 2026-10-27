package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
)

// servers is the source of an extension that adds MCP servers for the
// session, as mcp.json's mcpServers entries.
const servers = `// Written by mako: the MCP servers it gives this session.
const servers = %s;

export default function (pi) {
	for (const [name, config] of Object.entries(servers)) {
		if (pi.registerMcpServer) pi.registerMcpServer(name, config);
	}
}
`

// WithServers has pi load an extension adding the servers, written in dir
// and named for session; cleanup removes it. It holds the servers' keys,
// so only the user reads it.
func (p *Pi) WithServers(l *agent.Launch, list []agent.Server, dir, session string) (func(), error) {
	type server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}
	byName := map[string]server{}
	for _, s := range list {
		byName[s.ID] = server{Command: s.Command, Args: s.ArgList(), Env: s.Env}
	}
	b, err := json.Marshal(byName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "pi-mcp-"+session+".js")
	if err := os.WriteFile(path, fmt.Appendf(nil, servers, b), 0o600); err != nil {
		return nil, err
	}
	l.Command = withExtension(l.Command, path)
	return func() { os.Remove(path) }, nil
}
