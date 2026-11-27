package sshremote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
)

// Feature flags reported on Node.Features for SSH hosts.
const (
	FeatureSSH       = "ssh"
	FeatureSSHDaemon = "ssh:daemon"
	FeatureSSHDirect = "ssh:direct"
)

// ProbeResult describes a remote SSH host discovered during connection probe.
type ProbeResult struct {
	OS          string
	Arch        string
	Hostname    string
	Home        string
	Shell       string
	MakodPath   string
	MakodSocket string
	Agents      []string
	Latency     time.Duration
}

// remotePathPrelude adds common user tool directories to PATH so non-interactive
// SSH commands find agents (claude, codex, agy, etc.) and makod.
const remotePathPrelude = `export PATH="$HOME/.local/bin:$HOME/.mako/bin:$HOME/go/bin:$HOME/.npm-global/bin:$HOME/.cargo/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"; ` +
	`for _d in "$HOME"/.nvm/versions/node/*/bin "$HOME"/.fnm/aliases/default/bin "$HOME"/.volta/bin; do [ -d "$_d" ] && PATH="$_d:$PATH"; done; export PATH`

// probeScript returns a single POSIX sh probe command that gathers OS, arch,
// hostname, home, shell, remote makod availability, and installed agents in one
// SSH roundtrip.
func probeScript(remoteBinary string) string {
	var b strings.Builder
	b.WriteString(remotePathPrelude)
	b.WriteString("; printf 'OS=%s\\n' \"$(uname -s 2>/dev/null)\"; ")
	b.WriteString("printf 'ARCH=%s\\n' \"$(uname -m 2>/dev/null)\"; ")
	b.WriteString("printf 'HOSTNAME=%s\\n' \"$(hostname 2>/dev/null)\"; ")
	b.WriteString("printf 'HOME=%s\\n' \"$HOME\"; ")
	b.WriteString("printf 'SHELL=%s\\n' \"$SHELL\"; ")
	if bin := strings.TrimSpace(remoteBinary); bin != "" {
		fmt.Fprintf(&b, "_mb=%s; case \"$_mb\" in '~') _mb=\"$HOME\";; '~/'*) _mb=\"$HOME/${_mb#~/}\";; esac; [ -x \"$_mb\" ] && printf 'MAKOD=%%s\\n' \"$_mb\"; ", ShellQuote(bin))
	} else {
		b.WriteString("_mb=\"$(command -v makod 2>/dev/null || true)\"; ")
		b.WriteString("[ -n \"$_mb\" ] || { [ -x \"$HOME/.mako/bin/makod\" ] && _mb=\"$HOME/.mako/bin/makod\"; }; ")
		b.WriteString("[ -n \"$_mb\" ] && printf 'MAKOD=%s\\n' \"$_mb\"; ")
	}
	// Check candidate agents.
	b.WriteString("_ag=''; for _a in claude codex agy pi omp omo opencode; do command -v \"$_a\" >/dev/null 2>&1 && _ag=\"${_ag}${_ag:+,}$_a\"; done; printf 'AGENTS=%s\\n' \"$_ag\"")
	return b.String()
}

// ParseProbeOutput parses key=value lines emitted by probeScript.
func ParseProbeOutput(raw string) ProbeResult {
	var res ProbeResult
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "OS":
			res.OS = normalizeOS(v)
		case "ARCH":
			res.Arch = normalizeArch(v)
		case "HOSTNAME":
			res.Hostname = v
		case "HOME":
			res.Home = v
		case "SHELL":
			res.Shell = v
		case "MAKOD":
			res.MakodPath = v
		case "MAKOD_SOCK":
			res.MakodSocket = v
		case "AGENTS":
			if v != "" {
				for _, name := range strings.Split(v, ",") {
					name = strings.TrimSpace(name)
					if name != "" && !slices.Contains(res.Agents, name) {
						res.Agents = append(res.Agents, name)
					}
				}
			}
		}
	}
	return res
}

func normalizeOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	case "freebsd":
		return "freebsd"
	case "openbsd":
		return "openbsd"
	case "netbsd":
		return "netbsd"
	default:
		if strings.HasPrefix(strings.ToLower(s), "mingw") || strings.HasPrefix(strings.ToLower(s), "msys") || strings.HasPrefix(strings.ToLower(s), "cygwin") {
			return "windows"
		}
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func normalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i686", "x86":
		return "386"
	case "armv7l", "armv6l", "arm":
		return "arm"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// Probe connects to h over OpenSSH and collects host metadata and installed agents.
func Probe(ctx context.Context, h config.SSHHost, controlPath string) (ProbeResult, error) {
	args, err := BaseArgs(h, Options{ControlPath: controlPath})
	if err != nil {
		return ProbeResult{}, err
	}
	args = append(args, "sh -c "+ShellQuote(probeScript(h.RemoteBinary)))
	start := time.Now()
	cmd := exec.CommandContext(ctx, SSHBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := safeexec.Run(cmd); err != nil {
		return ProbeResult{}, fmt.Errorf("%s", CleanSSHError(stderr.String(), err))
	}
	res := ParseProbeOutput(stdout.String())
	res.Latency = time.Since(start)
	if res.OS == "" && res.Hostname == "" && res.Home == "" {
		return ProbeResult{}, fmt.Errorf("unexpected empty probe output from %s", h.DisplayName())
	}
	return res, nil
}

// TestConnection tests connectivity to h without leaving a persistent
// ControlMaster behind, returning probe results and roundtrip latency.
func TestConnection(ctx context.Context, h config.SSHHost) (ProbeResult, error) {
	args, err := BaseArgs(h, Options{NoControlMaster: true})
	if err != nil {
		return ProbeResult{}, err
	}
	args = append(args, "sh -c "+ShellQuote(probeScript(h.RemoteBinary)))
	start := time.Now()
	cmd := exec.CommandContext(ctx, SSHBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := safeexec.Run(cmd); err != nil {
		return ProbeResult{}, fmt.Errorf("%s", CleanSSHError(stderr.String(), err))
	}
	res := ParseProbeOutput(stdout.String())
	res.Latency = time.Since(start)
	if res.OS == "" && res.Hostname == "" && res.Home == "" {
		return ProbeResult{}, fmt.Errorf("unexpected empty probe output from %s", h.DisplayName())
	}
	return res, nil
}

// AgentInfos turns a list of remote installed agent names into protocol.AgentInfo
// records (always including shell so users can open a remote terminal).
func AgentInfos(installed []string) []*protocol.AgentInfo {
	seen := map[string]bool{}
	var out []*protocol.AgentInfo

	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		info := &protocol.AgentInfo{Name: name}
		if e, ok := agents.Lookup(name); ok {
			info.Icon = e.Info.Icon
			c := e.Info.Color
			if c.A > 0 {
				info.Color = protocol.RGBA(c.R, c.G, c.B, c.A)
			}
			// In direct SSH mode we do not run remote hooks/log readers, so
			// capabilities are terminal-driven (screen rules still run locally).
		}
		out = append(out, info)
	}

	for _, name := range installed {
		add(name)
	}
	add(agentspec.ShellName)
	return out
}
