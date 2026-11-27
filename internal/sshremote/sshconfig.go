// Package sshremote manages OpenSSH connections to remote hosts, supporting
// both tunneled remote makod daemons and direct agentless SSH execution.
package sshremote

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"

	"github.com/ironpark/mako/internal/config"
)

// ConfigHost is a concrete Host alias discovered in ~/.ssh/config.
type ConfigHost struct {
	Alias        string
	HostName     string
	User         string
	Port         int
	IdentityFile string
	ProxyJump    string
}

// ToSSHHost converts a discovered ~/.ssh/config entry into a config.SSHHost.
// Because OpenSSH reads ~/.ssh/config natively, Host is set to the alias so
// all directives (including ProxyCommand, Certificates, Match, etc.) apply.
func (c ConfigHost) ToSSHHost() config.SSHHost {
	return config.SSHHost{
		Name:         c.Alias,
		Host:         c.Alias,
		User:         c.User,
		Port:         c.Port,
		IdentityFile: c.IdentityFile,
		ProxyJump:    c.ProxyJump,
	}
}

// Summary returns a short secondary label like "ubuntu@10.0.0.5:2222".
func (c ConfigHost) Summary() string {
	host := c.HostName
	if host == "" {
		host = c.Alias
	}
	if c.User != "" && !strings.Contains(host, "@") {
		host = c.User + "@" + host
	}
	if c.Port > 0 && c.Port != 22 {
		host += ":" + strconv.Itoa(c.Port)
	}
	return host
}

// FindConfigHost returns the Host alias in hosts that name selects. Like
// OpenSSH, it matches without regard to case, but an exact match wins.
func FindConfigHost(hosts []ConfigHost, name string) (ConfigHost, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ConfigHost{}, false
	}
	fold := -1
	for i, h := range hosts {
		if h.Alias == name {
			return h, true
		}
		if fold < 0 && strings.EqualFold(h.Alias, name) {
			fold = i
		}
	}
	if fold >= 0 {
		return hosts[fold], true
	}
	return ConfigHost{}, false
}

// DiscoverConfigHosts reads the current user's ~/.ssh/config (and any Include
// directives) and returns all concrete Host aliases in declaration order.
func DiscoverConfigHosts() []ConfigHost {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return DiscoverConfigHostsFrom(filepath.Join(home, ".ssh", "config"))
}

// DiscoverConfigHostsFrom reads path (an OpenSSH client config file) and
// returns all concrete Host aliases in declaration order.
func DiscoverConfigHostsFrom(path string) []ConfigHost {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseSSHConfig(f, filepath.Dir(path))
}

// ParseSSHConfig parses an OpenSSH client config reader and extracts concrete
// Host entries, resolving effective HostName, User, Port, IdentityFile and
// ProxyJump for each alias.
func ParseSSHConfig(r io.Reader, baseDir string) []ConfigHost {
	cfg, err := ssh_config.Decode(r)
	if err != nil || cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []ConfigHost
	collectConfigHosts(cfg, baseDir, seen, &out, 0)
	return out
}

func collectConfigHosts(cfg *ssh_config.Config, baseDir string, seen map[string]bool, out *[]ConfigHost, depth int) {
	if depth > 4 || cfg == nil {
		return
	}
	// First check any top-level or Host-scoped Include directives.
	for _, host := range cfg.Hosts {
		for _, node := range host.Nodes {
			var includeVal string
			switch n := node.(type) {
			case *ssh_config.Include:
				line := strings.TrimSpace(n.String())
				if idx := strings.IndexByte(line, '#'); idx >= 0 {
					line = strings.TrimSpace(line[:idx])
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 && strings.EqualFold(fields[0], "Include") {
					includeVal = strings.Join(fields[1:], " ")
				}
			case *ssh_config.KV:
				if strings.EqualFold(n.Key, "Include") {
					includeVal = n.Value
				}
			}
			if includeVal != "" {
				for _, pat := range strings.Fields(includeVal) {
					pat = expandIncludePath(pat, baseDir)
					matches, _ := filepath.Glob(pat)
					slices.Sort(matches)
					for _, m := range matches {
						if f, err := os.Open(m); err == nil {
							sub, err := ssh_config.Decode(f)
							_ = f.Close()
							if err == nil {
								collectConfigHosts(sub, filepath.Dir(m), seen, out, depth+1)
							}
						}
					}
				}
			}
		}
		for _, pat := range host.Patterns {
			alias := strings.TrimSpace(pat.String())
			if !isConcreteAlias(alias) || !host.Matches(alias) || seen[alias] {
				continue
			}
			seen[alias] = true
			entry := ConfigHost{
				Alias:        alias,
				HostName:     getConfigKey(cfg, alias, "HostName"),
				User:         getConfigKey(cfg, alias, "User"),
				IdentityFile: getConfigKey(cfg, alias, "IdentityFile"),
				ProxyJump:    getConfigKey(cfg, alias, "ProxyJump"),
			}
			if portStr := getConfigKey(cfg, alias, "Port"); portStr != "" {
				if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p <= 65535 {
					entry.Port = p
				}
			}
			*out = append(*out, entry)
		}
	}
}

func getConfigKey(cfg *ssh_config.Config, alias, key string) string {
	v, err := cfg.Get(alias, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func isConcreteAlias(alias string) bool {
	if alias == "" || strings.HasPrefix(alias, "!") || strings.HasPrefix(alias, "-") {
		return false
	}
	return !strings.ContainsAny(alias, "*? \t\r\n")
}

func expandIncludePath(pat, baseDir string) string {
	if strings.HasPrefix(pat, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, pat[2:])
		}
	}
	if !filepath.IsAbs(pat) && baseDir != "" {
		return filepath.Join(baseDir, pat)
	}
	return pat
}
