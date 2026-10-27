// Package config loads and persists sanbo's node configuration.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/mcp"
)

// Version is the build version, overridable at link time.
var Version = "0.1.0-dev"

// DefaultPort is the UDP port the daemon listens on for QUIC.
const DefaultPort = 4242

// ServiceType is the Bonjour/mDNS service advertised by every daemon.
const ServiceType = "_sanbo._udp"

// Config is the on-disk daemon configuration.
type Config struct {
	// NodeID uniquely identifies this daemon; generated once and persisted.
	NodeID string `json:"node_id"`
	// Name is the human-readable node name, defaulting to the hostname.
	Name string `json:"name"`
	// Listen is the QUIC listen address.
	Listen string `json:"listen"`
	// Domain is the mDNS domain to advertise and browse in.
	Domain string `json:"domain"`
	// Peers are statically configured peer addresses, always dialed.
	Peers []string `json:"peers"`
	// Discovery toggles the automatic peer discovery backends.
	Discovery Discovery `json:"discovery"`
	// PSK authenticates peers with each other. Empty disables the check.
	PSK string `json:"psk,omitempty"`
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
	// Disabled leaves the daemon to the older QUIC links alone.
	Disabled bool `json:"disabled,omitempty"`
	// Port is the UDP (QUIC) and TCP port; 0 is DefaultP2PPort, or any
	// free port when Listen's port is 0.
	Port int `json:"port,omitempty"`
	// Relay makes this daemon a relay for the mesh's other nodes. Turn it
	// on for a node with a public address.
	Relay bool `json:"relay,omitempty"`
	// Relays are the multiaddrs of the mesh's relay nodes, each ending in
	// its /p2p/ ID, which this daemon is reachable through from behind NAT.
	Relays []string `json:"relays,omitempty"`
}

// DefaultP2PPort is the port the daemon listens on for libp2p.
const DefaultP2PPort = 4243

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

// Dir returns the sanbo configuration directory, honoring SANBO_HOME.
func Dir() (string, error) {
	if custom := os.Getenv("SANBO_HOME"); custom != "" {
		return custom, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sanbo"), nil
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

// RunDir holds files that live as long as a session, as an agent's MCP
// configuration.
func RunDir() (string, error) { return subdir("run") }

func subdir(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Default builds a configuration for a fresh install.
func Default() *Config {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "sanbo-node"
	}
	return &Config{
		NodeID: newNodeID(),
		Name:   host,
		Listen: fmt.Sprintf(":%d", DefaultPort),
		Domain: "local.",
		Discovery: Discovery{
			MDNS:      true,
			Tailscale: true,
		},
		Agents: agentspec.Defaults(),
	}
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
		if err := Save(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
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
		panic(fmt.Sprintf("sanbo: no entropy for node id: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
