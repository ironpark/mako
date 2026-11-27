package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// SSHNodePrefix begins the node ID of every SSH remote host in the mesh.
const SSHNodePrefix = "ssh:"

// SSH connection modes.
const (
	SSHModeAuto   = "auto"   // use remote makod over SSH tunnel if installed, else direct SSH
	SSHModeDaemon = "daemon" // require remote makod over SSH tunnel
	SSHModeDirect = "direct" // always drive the remote host directly over SSH
)

// SSHHost configures a remote machine reached over OpenSSH.
type SSHHost struct {
	// ID uniquely identifies this SSH host; generated once when added.
	// Its node ID in the mesh is SSHNodePrefix + ID.
	ID string `json:"id"`
	// Name is the human-readable display name shown in the UI.
	Name string `json:"name"`
	// Host is the SSH target: hostname, IP, user@host, or ~/.ssh/config alias.
	Host string `json:"host"`
	// User overrides the SSH username when Host has no user@ prefix.
	User string `json:"user,omitempty"`
	// Port overrides the SSH port; 0 uses ~/.ssh/config or 22.
	Port int `json:"port,omitempty"`
	// IdentityFile is an optional private key path (e.g. ~/.ssh/id_ed25519).
	IdentityFile string `json:"identity_file,omitempty"`
	// ProxyJump is an optional jump/bastion host (-J).
	ProxyJump string `json:"proxy_jump,omitempty"`
	// DefaultDir is the default remote working directory (e.g. ~/projects).
	DefaultDir string `json:"default_dir,omitempty"`
	// Mode chooses between "auto" (default), "daemon", and "direct".
	Mode string `json:"mode,omitempty"`
	// RemoteBinary overrides the remote path to makod.
	RemoteBinary string `json:"remote_binary,omitempty"`
	// ExtraArgs are additional OpenSSH client flags (e.g. ["-o", "ForwardAgent=yes"]).
	ExtraArgs []string `json:"extra_args,omitempty"`
	// Disabled keeps the host saved without connecting to it automatically.
	Disabled bool `json:"disabled,omitempty"`
}

// NodeID returns the mesh node ID for this SSH host.
func (h SSHHost) NodeID() string {
	return SSHNodePrefix + h.ID
}

// DisplayName returns the host's label, falling back to Host.
func (h SSHHost) DisplayName() string {
	if s := strings.TrimSpace(h.Name); s != "" {
		return s
	}
	return strings.TrimSpace(h.Host)
}

// DisplayAddr returns a concise human-readable address such as "user@host:2222".
func (h SSHHost) DisplayAddr() string {
	target := strings.TrimSpace(h.Host)
	if user := strings.TrimSpace(h.User); user != "" && !strings.Contains(target, "@") {
		target = user + "@" + target
	}
	if h.Port > 0 && h.Port != 22 {
		return target + ":" + strconv.Itoa(h.Port)
	}
	return target
}

// EffectiveMode returns the normalized connection mode.
func (h SSHHost) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(h.Mode)) {
	case SSHModeDaemon:
		return SSHModeDaemon
	case SSHModeDirect:
		return SSHModeDirect
	default:
		return SSHModeAuto
	}
}

// Clone returns a deep copy of h.
func (h SSHHost) Clone() SSHHost {
	h.ExtraArgs = slices.Clone(h.ExtraArgs)
	return h
}

// Equal reports whether a and b have identical configuration fields.
func (a SSHHost) Equal(b SSHHost) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Host == b.Host &&
		a.User == b.User &&
		a.Port == b.Port &&
		a.IdentityFile == b.IdentityFile &&
		a.ProxyJump == b.ProxyJump &&
		a.DefaultDir == b.DefaultDir &&
		a.EffectiveMode() == b.EffectiveMode() &&
		a.RemoteBinary == b.RemoteBinary &&
		a.Disabled == b.Disabled &&
		slices.Equal(a.ExtraArgs, b.ExtraArgs)
}

// Validate checks that h is safe and well-formed to invoke with OpenSSH.
func (h SSHHost) Validate() error {
	host := strings.TrimSpace(h.Host)
	if host == "" {
		return errors.New("SSH host is required")
	}
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("SSH host %q must not start with '-'", host)
	}
	if hasControlOrSpace(host) {
		return fmt.Errorf("SSH host %q must not contain spaces or control characters", host)
	}
	if user := strings.TrimSpace(h.User); user != "" {
		if strings.HasPrefix(user, "-") || hasControlOrSpace(user) {
			return fmt.Errorf("SSH user %q is invalid", user)
		}
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("SSH port %d must be between 0 and 65535", h.Port)
	}
	if jump := strings.TrimSpace(h.ProxyJump); jump != "" {
		if strings.HasPrefix(jump, "-") || hasControlOrSpace(jump) {
			return fmt.Errorf("ProxyJump %q is invalid", jump)
		}
	}
	if strings.ContainsRune(h.IdentityFile, 0) || strings.ContainsAny(h.IdentityFile, "\r\n") {
		return errors.New("identity file path contains invalid characters")
	}
	if strings.ContainsRune(h.DefaultDir, 0) || strings.ContainsAny(h.DefaultDir, "\r\n") {
		return errors.New("default folder contains invalid characters")
	}
	if strings.ContainsRune(h.RemoteBinary, 0) || strings.ContainsAny(h.RemoteBinary, "\r\n") {
		return errors.New("remote binary path contains invalid characters")
	}
	switch strings.ToLower(strings.TrimSpace(h.Mode)) {
	case "", SSHModeAuto, SSHModeDaemon, SSHModeDirect:
	default:
		return fmt.Errorf("unknown SSH mode %q (want auto, daemon, or direct)", h.Mode)
	}
	for _, arg := range h.ExtraArgs {
		if strings.ContainsRune(arg, 0) || strings.ContainsAny(arg, "\r\n") {
			return errors.New("extra SSH argument contains invalid characters")
		}
	}
	return nil
}

func hasControlOrSpace(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// NewSSHHostID generates a random 8-byte hex identifier for a new SSH host.
func NewSSHHostID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("mako: no entropy for ssh host id: %v", err))
	}
	return hex.EncodeToString(buf[:])
}

// IsSSHNodeID reports whether nodeID refers to an SSH remote host.
func IsSSHNodeID(nodeID string) bool {
	return strings.HasPrefix(nodeID, SSHNodePrefix) && len(nodeID) > len(SSHNodePrefix)
}

// NormalizeSSHHosts trims fields, assigns missing IDs, deduplicates IDs, and
// drops empty entries while preserving order.
func NormalizeSSHHosts(hosts []SSHHost) []SSHHost {
	if len(hosts) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(hosts))
	out := make([]SSHHost, 0, len(hosts))
	for _, raw := range hosts {
		h := raw.Clone()
		h.ID = strings.TrimSpace(h.ID)
		h.Name = strings.TrimSpace(h.Name)
		h.Host = strings.TrimSpace(h.Host)
		h.User = strings.TrimSpace(h.User)
		h.IdentityFile = strings.TrimSpace(h.IdentityFile)
		h.ProxyJump = strings.TrimSpace(h.ProxyJump)
		h.DefaultDir = strings.TrimSpace(h.DefaultDir)
		h.RemoteBinary = strings.TrimSpace(h.RemoteBinary)
		h.Mode = strings.ToLower(strings.TrimSpace(h.Mode))
		if h.Mode == SSHModeAuto {
			h.Mode = ""
		}
		if h.Host == "" {
			continue
		}
		if h.Name == "" {
			h.Name = h.Host
		}
		if h.ID == "" || seen[h.ID] {
			for {
				id := NewSSHHostID()
				if !seen[id] {
					h.ID = id
					break
				}
			}
		}
		seen[h.ID] = true
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CloneSSHHosts returns a deep copy of hosts.
func CloneSSHHosts(hosts []SSHHost) []SSHHost {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]SSHHost, len(hosts))
	for i, h := range hosts {
		out[i] = h.Clone()
	}
	return out
}
