package agentspec

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
)

// IsolatedHomeBase returns the base directory for isolated agent homes:
// $MAKO_HOME/home when MAKO_HOME is set, otherwise ~/.mako/home.
func IsolatedHomeBase() (string, error) {
	if custom := os.Getenv("MAKO_HOME"); custom != "" {
		return filepath.Join(custom, "home"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mako", "home"), nil
}

// IsolatedHomeDir returns the isolated home root for agent name under IsolatedHomeBase.
func IsolatedHomeDir(name string) (string, error) {
	base, err := IsolatedHomeBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// SupportsHomeIsolation reports whether agent name can have its settings and
// conversation data isolated into Mako's own home directory.
func SupportsHomeIsolation(name string) bool {
	entry, ok := agents.Lookup(name)
	if !ok {
		return false
	}
	logs, ok := entry.Driver.(agent.Logs)
	if !ok {
		return false
	}
	if logs.Place().RootEnv != "" {
		return true
	}
	return name == "agy" && runtime.GOOS != "windows"
}

// IsolatableAgents returns every built-in agent that supports home isolation,
// in sorted order.
func IsolatableAgents() []string {
	var out []string
	for _, name := range Names(nil) {
		if SupportsHomeIsolation(name) {
			out = append(out, name)
		}
	}
	return out
}

// AgentDataDir returns the agent's data directory when isolate is false (host
// default) or true (Mako isolated home).
func AgentDataDir(name string, isolate bool) (string, error) {
	entry, ok := agents.Lookup(name)
	if !ok {
		return "", fmt.Errorf("agent %q has no data directory", name)
	}
	logs, ok := entry.Driver.(agent.Logs)
	if !ok {
		return "", fmt.Errorf("agent %q has no data directory", name)
	}
	place := logs.Place()
	if !isolate {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, place.HomeDir), nil
	}
	if !SupportsHomeIsolation(name) {
		return "", fmt.Errorf("agent %q does not support home isolation", name)
	}
	iso, err := IsolatedHomeDir(name)
	if err != nil {
		return "", err
	}
	if name == "agy" {
		return filepath.Join(iso, place.HomeDir), nil
	}
	return iso, nil
}

// EnsureAgyIsolatedHome initializes isolatedDir as an isolated HOME for agy:
// creating .gemini/antigravity-cli and .gemini/config as independent directories
// while symlinking all other top-level entries from the real HOME so tools like
// git, ssh, and cloud CLIs continue to work normally.
func EnsureAgyIsolatedHome(isolatedDir string) error {
	for _, sub := range []string{
		isolatedDir,
		filepath.Join(isolatedDir, ".gemini", "antigravity-cli"),
		filepath.Join(isolatedDir, ".gemini", "config"),
	} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return err
		}
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	realHome = filepath.Clean(realHome)
	if filepath.Clean(isolatedDir) == realHome {
		return nil
	}

	// Clean up dangling symlinks in isolatedDir that pointed into realHome.
	if existing, err := os.ReadDir(isolatedDir); err == nil {
		prefix := realHome + string(filepath.Separator)
		for _, entry := range existing {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			linkPath := filepath.Join(isolatedDir, entry.Name())
			target, err := os.Readlink(linkPath)
			if err != nil {
				continue
			}
			cleanTarget := filepath.Clean(target)
			if strings.HasPrefix(cleanTarget, prefix) || cleanTarget == realHome {
				if _, err := os.Stat(linkPath); os.IsNotExist(err) {
					_ = os.Remove(linkPath)
				}
			}
		}
	}

	entries, err := os.ReadDir(realHome)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".gemini" || name == ".mako" {
			continue
		}
		src := filepath.Join(realHome, name)
		if filepath.Clean(src) == filepath.Clean(isolatedDir) {
			continue
		}
		dst := filepath.Join(isolatedDir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.Symlink(src, dst); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// IsolatedHomeEnv returns the environment variable overrides needed to isolate
// agent name's home directory, unless the relevant variable is already set in
// existing. It also ensures the target directory structure exists.
func IsolatedHomeEnv(name string, existing map[string]string) (map[string]string, error) {
	if !SupportsHomeIsolation(name) {
		return nil, nil
	}
	entry, _ := agents.Lookup(name)
	place := entry.Driver.(agent.Logs).Place()
	iso, err := IsolatedHomeDir(name)
	if err != nil {
		return nil, err
	}
	if name == "agy" {
		if existing["HOME"] != "" {
			return nil, nil
		}
		if err := EnsureAgyIsolatedHome(iso); err != nil {
			return nil, err
		}
		return map[string]string{"HOME": iso}, nil
	}
	key := place.RootEnv
	if key == "" || existing[key] != "" {
		return nil, nil
	}
	if err := os.MkdirAll(iso, 0o700); err != nil {
		return nil, err
	}
	return map[string]string{key: iso}, nil
}

// ApplyIsolatedHome injects the isolated home environment variable into l for
// agent name, unless already overridden in l.Env.
func ApplyIsolatedHome(name string, l *Launch) error {
	isoEnv, err := IsolatedHomeEnv(name, l.Env)
	if err != nil {
		return fmt.Errorf("agent %q isolated home: %w", name, err)
	}
	if len(isoEnv) == 0 {
		return nil
	}
	if l.Env == nil {
		l.Env = map[string]string{}
	}
	maps.Copy(l.Env, isoEnv)
	return nil
}

// IsolatedHomeEnvForAgent returns the environment overrides for agent name when
// global isolate is enabled (without clobbering explicit overrides in a.Env).
func IsolatedHomeEnvForAgent(name string, isolate bool, agents map[string]Agent) map[string]string {
	a, ok := Lookup(name, agents)
	var out map[string]string
	if ok {
		out = maps.Clone(a.Env)
	}
	if !isolate {
		return out
	}
	iso, err := IsolatedHomeEnv(name, out)
	if err != nil || len(iso) == 0 {
		return out
	}
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, iso)
	return out
}
