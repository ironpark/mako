// Package opencode is mako's driver for OpenCode, and so for OmO, which
// runs as OpenCode's plugin: its options, the plugin mako has it load,
// which reports its events and writes its conversations down for mako,
// and the tools OpenCode and OmO have.
package opencode

import (
	"encoding/json"
	"image/color"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is OpenCode.
type Agent struct {
	agent.Flags
	agent.CommonHooks
}

// New is the OpenCode driver. OpenCode picks a new session's ID itself,
// once its first prompt is sent.
func New() *Agent {
	return &Agent{Flags: agent.Flags{
		ResumeFlags: []string{"--session", "-s"}, ForkFlag: "--fork",
		PickerFlags: []string{"--continue", "-c"},
		Inputs:      []string{"--prompt"},
		Values:      strings.Fields("--model -m --agent --port --hostname --mdns-domain --cors --replay-limit --log-level"),
	}}
}

func (*Agent) Info() agent.Info {
	return agent.Info{
		Name: "opencode", Programs: []string{"opencode"}, Command: []string{"opencode"},
		Icon: "agent-opencode", Color: color.NRGBA{0x9a, 0x9a, 0xa8, 0xff},
	}
}

// configEnv is the variable OpenCode reads a configuration from that it
// merges over the user's own, its lists added to theirs.
const configEnv = "OPENCODE_CONFIG_CONTENT"

// withConfig sets key of the configuration l gives OpenCode to value,
// keeping the rest, the process's own among it.
func withConfig(l *agent.Launch, key string, value any) error {
	doc := map[string]any{}
	content, ok := l.Env[configEnv]
	if !ok {
		content = os.Getenv(configEnv)
	}
	if content != "" {
		if err := json.Unmarshal([]byte(content), &doc); err != nil {
			return err // the user's, which mako will not replace
		}
	}
	switch v := value.(type) {
	case []any:
		was, _ := doc[key].([]any)
		value = append(was, v...)
	case map[string]any:
		was, _ := doc[key].(map[string]any)
		if was == nil {
			was = map[string]any{}
		}
		maps.Copy(was, v)
		value = was
	}
	doc[key] = value
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	l.SetEnv(configEnv, string(b))
	return nil
}

// WithServers gives OpenCode the servers in the configuration its
// environment carries.
func (*Agent) WithServers(l *agent.Launch, servers []agent.Server, _, _ string) (func(), error) {
	mcp := map[string]any{}
	for _, s := range servers {
		server := map[string]any{"type": "local", "command": append([]string{s.Command}, s.ArgList()...), "enabled": true}
		if len(s.Env) > 0 {
			server["environment"] = s.Env
		}
		mcp[s.ID] = server
	}
	return func() {}, withConfig(l, "mcp", mcp)
}

// fileURL is path as OpenCode takes a plugin's: a file URL.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
