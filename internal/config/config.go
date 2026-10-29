// Package config loads and persists mako's node configuration.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/mcp"
)

// Version is the build version, overridable at link time.
var Version = "0.1.0-dev"

// ServiceType is the Bonjour/mDNS service advertised by every daemon.
const ServiceType = "_mako._udp"

// Config is the on-disk daemon configuration.
type Config struct {
	// NodeID uniquely identifies this daemon; generated once and persisted.
	NodeID string `json:"node_id"`
	// Name is the human-readable node name, defaulting to the hostname.
	Name string `json:"name"`
	// Domain is the mDNS domain to advertise and browse in.
	Domain string `json:"domain"`
	// Peers are statically configured peer addresses, always dialed.
	Peers []string `json:"peers"`
	// Discovery toggles the automatic peer discovery backends.
	Discovery Discovery `json:"discovery"`
	// SSHKey is an SSH public key whose owner vouches for this node: its
	// private half in the SSH agent, or beside it without a passphrase.
	// Nodes that let the key in let this node in, as do the other nodes
	// it vouches for. Left out, it is DefaultSSHKey; empty for none.
	SSHKey string `json:"ssh_key"`
	// P2P is how the daemon links to its peers over libp2p.
	P2P P2P `json:"p2p"`
	// Agents are how this node launches each agent by name: its command,
	// default flags and environment, and its profiles. A built-in left out
	// launches with its default command.
	Agents map[string]agentspec.Agent `json:"agents,omitempty"`
	// ContextServers configure the MCP servers this node's claude and
	// codex sessions get, by server ID: those of the Zed extensions
	// installed, which are on unless turned off here, and the user's own.
	ContextServers map[string]mcp.Server `json:"context_servers,omitempty"`
}

// P2P configures the daemon's libp2p host, which its peers reach it on.
type P2P struct {
	// Disabled leaves the daemon without peers.
	Disabled bool `json:"disabled,omitempty"`
	// Listen is the address libp2p listens on, as host:port: QUIC on the
	// UDP port and TCP on the TCP one. An empty host is every interface,
	// and port 0 any free port; empty Listen is every interface on
	// DefaultP2PPort.
	Listen string `json:"listen,omitempty"`
	// Relay makes this daemon a relay for the mesh's other nodes. Turn it
	// on for a node with a public address.
	Relay bool `json:"relay,omitempty"`
	// Relays are the multiaddrs of the mesh's relay nodes, each ending in
	// its /p2p/ ID, which this daemon is reachable through from behind NAT.
	Relays []string `json:"relays,omitempty"`
}

// DefaultP2PPort is the port the daemon listens on for libp2p.
const DefaultP2PPort = 4243

// HostPort is where libp2p listens: Listen's host and port, or every
// interface on DefaultP2PPort when it is empty.
func (p P2P) HostPort() (string, int, error) {
	if p.Listen == "" {
		return "", DefaultP2PPort, nil
	}
	host, port, err := net.SplitHostPort(p.Listen)
	if err != nil {
		return "", 0, fmt.Errorf("p2p.listen %q: %w", p.Listen, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", 0, fmt.Errorf("p2p.listen %q: the port is not a port", p.Listen)
	}
	return host, n, nil
}

// WithP2PPort is addr, a host with or without its port, with
// DefaultP2PPort where it has none.
func WithP2PPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return net.JoinHostPort(addr, strconv.Itoa(DefaultP2PPort))
	}
	return addr
}

// Discovery selects which peer discovery backends run.
type Discovery struct {
	// MDNS enables Bonjour advertise/browse on the local link.
	MDNS bool `json:"mdns"`
	// Tailscale enables MagicDNS-based peer probing.
	Tailscale bool `json:"tailscale"`
	// TailnetHosts are MagicDNS names to probe when Tailscale discovery is on.
	// Empty means "ask the local tailscale CLI for the peer list".
	TailnetHosts []string `json:"tailnet_hosts,omitempty"`
}

// Dir returns the mako configuration directory, honoring MAKO_HOME.
func Dir() (string, error) {
	if custom := os.Getenv("MAKO_HOME"); custom != "" {
		return custom, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "mako")
	// The project was called sanbo before; its configuration, identity and
	// saved state move over the first time.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		_ = os.Rename(filepath.Join(base, "sanbo"), dir)
	}
	return dir, nil
}

// Path returns the path of the config file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ExtensionsDir is where the Zed extensions are installed, and
// ExtensionsWorkDir where each keeps what it downloads and installs.
func ExtensionsDir() (string, error)     { return subdir("extensions") }
func ExtensionsWorkDir() (string, error) { return subdir("extension-work") }

func subdir(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// maxSocketPath is the longest socket path every platform takes (macOS's
// sun_path holds 104 bytes), with room for a socket's name.
const maxSocketPath = 100 - len("/daemon.sock")

// RunDir is where the daemon whose configuration directory is dir keeps
// what lives as long as it runs: its sockets and its sessions' MCP
// configurations. It is run in dir, or where that is too long for a
// socket's path, a folder of this user's named after dir in the temporary
// directory. Only this user may enter it. Empty dir is Dir.
func RunDir(dir string) (string, error) {
	if dir == "" {
		var err error
		if dir, err = Dir(); err != nil {
			return "", err
		}
	}
	run := filepath.Join(dir, "run")
	if len(run) > maxSocketPath {
		sum := sha256.Sum256([]byte(dir))
		name := fmt.Sprintf("mako-%d-%s", os.Getuid(), hex.EncodeToString(sum[:6]))
		run = filepath.Join(os.TempDir(), name)
		if len(run) > maxSocketPath && runtime.GOOS != "windows" {
			run = filepath.Join("/tmp", name)
		}
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		return "", err
	}
	// One that someone else made first is refused here.
	if err := os.Chmod(run, 0o700); err != nil {
		return "", err
	}
	return run, nil
}

// SocketPath is the socket the daemon whose configuration directory is
// dir listens on for its front-ends, in its RunDir.
func SocketPath(dir string) (string, error) {
	run, err := RunDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(run, "daemon.sock"), nil
}

// Default builds a configuration for a fresh install.
func Default() *Config {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "mako-node"
	}
	return &Config{
		NodeID: newNodeID(),
		Name:   host,
		Domain: "local.",
		Discovery: Discovery{
			MDNS:      true,
			Tailscale: true,
		},
		Agents: agentspec.Defaults(),
	}
}

// defaultSSHKeys are the public keys ssh-keygen makes by default, in the
// order ssh tries them.
var defaultSSHKeys = []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"}

// DefaultSSHKey is the first of this user's default SSH public keys in
// ~/.ssh, written from ~, or empty when there is none.
func DefaultSSHKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range defaultSSHKeys {
		if _, err := os.Stat(filepath.Join(home, ".ssh", name)); err == nil {
			return "~/.ssh/" + name
		}
	}
	return ""
}

// Load reads the config file, creating it with defaults when absent.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := Default()
		cfg.SSHKey = DefaultSSHKey()
		if err := Save(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	cfg.SSHKey = DefaultSSHKey()
	if err := json.Unmarshal(body, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.NodeID == "" {
		cfg.NodeID = newNodeID()
	}
	return cfg, nil
}

// Save writes the config file, creating its directory as needed.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func newNodeID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("mako: no entropy for node id: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
