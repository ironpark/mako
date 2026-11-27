package agent

import (
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// localProxyBypassHosts are the loopback hosts always appended to NO_PROXY and
// no_proxy when Mako wires a local loopback HTTP/WebSocket endpoint into an
// agent session.
var localProxyBypassHosts = []string{"127.0.0.1", "localhost", "::1"}

// IsLoopbackURL reports whether rawURL is an HTTP(S) or WebSocket URL pointing
// at a loopback host (127.0.0.0/8, ::1, localhost, or *.localhost).
func IsLoopbackURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
	default:
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// HasLoopbackURL reports whether any server in servers exposes a loopback HTTP
// or WebSocket URL.
func HasLoopbackURL(servers []Server) bool {
	for _, s := range servers {
		if IsLoopbackURL(s.URL) {
			return true
		}
	}
	return false
}

// MergeLocalProxyBypass merges comma-separated NO_PROXY/no_proxy values while
// preserving existing entries without duplicates and ensuring 127.0.0.1,
// localhost, and ::1 are present.
func MergeLocalProxyBypass(values ...string) string {
	seen := make(map[string]bool, len(localProxyBypassHosts)+4)
	entries := make([]string, 0, len(localProxyBypassHosts)+4)
	add := func(raw string) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return
		}
		key := strings.ToLower(entry)
		if seen[key] {
			return
		}
		seen[key] = true
		entries = append(entries, entry)
	}
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			add(part)
		}
	}
	for _, host := range localProxyBypassHosts {
		add(host)
	}
	return strings.Join(entries, ",")
}

// EnsureLocalProxyBypass ensures both NO_PROXY and lowercase no_proxy in l.Env
// include 127.0.0.1, localhost, and ::1 while preserving existing entries from
// l.Env (or the ambient process environment when not overridden in l.Env).
//
// Both variables are written because Node/undici reads lowercase no_proxy first
// while Go and other tools inspect NO_PROXY.
func EnsureLocalProxyBypass(l *Launch) {
	EnsureLocalProxyBypassWith(l, os.LookupEnv)
}

// EnsureLocalProxyBypassWith is EnsureLocalProxyBypass with a custom ambient
// environment lookup function (used for testing).
func EnsureLocalProxyBypassWith(l *Launch, lookup func(string) (string, bool)) {
	if l == nil {
		return
	}
	var sources []string
	upperOverride, hasUpper := l.Env["NO_PROXY"]
	lowerOverride, hasLower := l.Env["no_proxy"]
	switch {
	case hasUpper || hasLower:
		if hasUpper {
			sources = append(sources, upperOverride)
		}
		if hasLower {
			sources = append(sources, lowerOverride)
		}
	case lookup != nil:
		if v, ok := lookup("NO_PROXY"); ok {
			sources = append(sources, v)
		}
		if v, ok := lookup("no_proxy"); ok {
			sources = append(sources, v)
		}
	}
	merged := MergeLocalProxyBypass(sources...)
	l.SetEnv("NO_PROXY", merged)
	l.SetEnv("no_proxy", merged)
}
