package agy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/pkg/fsutil"
)

// agy reads MCP servers from its global mcp_config.json, not from a CLI flag.
// Mako merges its servers there while keeping the user's own entries intact.
// For the built-in "mako" stdio bridge, session credentials (MAKO_SESSION and
// MAKO_AGENT_MCP_TOKEN) go into the launch environment rather than the shared
// file, so concurrent agy sessions authenticate independently without
// overwriting one another.

var mcpMu sync.Mutex

// mcpConfigFile is agy's global mcp_config.json, honoring HOME in env when set.
func mcpConfigFile(env map[string]string) (string, error) {
	if home := env["HOME"]; home != "" {
		return filepath.Join(home, ".gemini", "config", "mcp_config.json"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config", "mcp_config.json"), err
}

// WithServers merges servers into agy's mcp_config.json and passes per-session
// built-in MCP credentials through l.Env.
func (*Agent) WithServers(l *agent.Launch, servers []agent.Server, _, _ string) (func(), error) {
	var env map[string]string
	if l != nil {
		env = l.Env
	}
	path, err := mcpConfigFile(env)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]map[string]any, len(servers))
	hasLoopbackHTTP := false
	for _, s := range servers {
		if s.ID == "" {
			continue
		}
		if s.ID == "mako" && s.Command != "" {
			// Pass per-session credentials via the agy process environment so
			// concurrent agy sessions share the static bridge entry safely.
			if l != nil {
				for k, v := range s.Env {
					l.SetEnv(k, v)
				}
			}
			entries[s.ID] = map[string]any{
				"command":  s.Command,
				"args":     s.ArgList(),
				"disabled": false,
			}
			continue
		}
		switch {
		case s.URL != "":
			entry := map[string]any{
				"serverUrl": s.URL,
				"disabled":  false,
			}
			if s.BearerToken != "" {
				entry["headers"] = map[string]string{"Authorization": "Bearer " + s.BearerToken}
			}
			entries[s.ID] = entry
			if agent.IsLoopbackURL(s.URL) {
				hasLoopbackHTTP = true
			}
		case s.Command != "":
			entry := map[string]any{
				"command":  s.Command,
				"args":     s.ArgList(),
				"disabled": false,
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
			entries[s.ID] = entry
		}
	}
	if len(entries) == 0 {
		return func() {}, nil
	}
	if err := writeMCPConfig(path, entries); err != nil {
		return nil, err
	}
	if hasLoopbackHTTP {
		agent.EnsureLocalProxyBypass(l)
	}
	return func() {}, nil
}

// writeMCPConfig merges entries into the mcpServers object at path, preserving
// any user-configured servers and other top-level fields.
func writeMCPConfig(path string, entries map[string]map[string]any) error {
	mcpMu.Lock()
	defer mcpMu.Unlock()

	doc := map[string]json.RawMessage{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, &doc); err != nil {
				return err // the user's file, which mako will not overwrite
			}
		}
	case !os.IsNotExist(err):
		return err
	}

	mcpServers := map[string]json.RawMessage{}
	if raw := bytes.TrimSpace(doc["mcpServers"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &mcpServers); err != nil {
			return err
		}
	}

	changed := false
	for id, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		var was bytes.Buffer
		if prev, ok := mcpServers[id]; ok && json.Compact(&was, prev) == nil && bytes.Equal(was.Bytes(), encoded) {
			continue
		}
		mcpServers[id] = encoded
		changed = true
	}
	if !changed && len(body) > 0 {
		return nil
	}

	encodedServers, err := json.Marshal(mcpServers)
	if err != nil {
		return err
	}
	doc["mcpServers"] = encodedServers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.Write(path, append(out, '\n'))
}
