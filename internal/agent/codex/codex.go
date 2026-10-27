// Package codex is mako's driver for Codex: its options, its -c overrides
// for MCP servers and trusted hooks, and its rollout logs.
package codex

import (
	"encoding/json"
	"image/color"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is Codex.
type Agent struct {
	agent.Flags
	agent.CommonHooks
}

// New is the Codex driver.
func New() *Agent {
	return &Agent{Flags: agent.Flags{
		ResumeFlags: []string{"resume"}, ForkFlag: "--fork",
		PickerFlags: []string{"--last", "--all"},
		Inputs:      []string{"--image", "-i"},
		Values:      strings.Fields("--model -m --config -c --profile -p --sandbox -s --ask-for-approval -a --cd -C --add-dir --enable --disable --local-provider --remote --remote-auth-token-env"),
		IsolateFlag: "--no-daemon",
	}}
}

func (*Agent) Info() agent.Info {
	return agent.Info{
		Name: "codex", Programs: []string{"codex"}, Command: []string{"codex"},
		// Its standalone installer's.
		Dirs: []string{filepath.Join(".codex", "packages", "standalone", "current", "bin")},
		Icon: "agent-codex", Color: color.NRGBA{0x7b, 0x6c, 0xf0, 0xff},
	}
}

// WithServers gives the servers as -c overrides of Codex's mcp_servers
// table, each value TOML, which a JSON string or array of strings also is.
func (*Agent) WithServers(l *agent.Launch, servers []agent.Server, _, _ string) (func(), error) {
	flags := []string{l.Command[0]}
	for _, s := range servers {
		key := "mcp_servers." + tomlKey(s.ID)
		flags = append(flags, "-c", key+".command="+jsonString(s.Command))
		flags = append(flags, "-c", key+".args="+jsonString(s.ArgList()))
		if len(s.Env) > 0 {
			var kv []string
			for _, k := range slices.Sorted(maps.Keys(s.Env)) {
				kv = append(kv, jsonString(k)+"="+jsonString(s.Env[k]))
			}
			flags = append(flags, "-c", key+".env={"+strings.Join(kv, ",")+"}")
		}
	}
	l.Command = append(flags, l.Command[1:]...)
	return func() {}, nil
}

// tomlKey is id as a TOML key, bare when it can be.
func tomlKey(id string) string {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return jsonString(id)
		}
	}
	return id
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
