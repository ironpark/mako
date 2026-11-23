//go:build makotest

package testhooks

import (
	"os"
	"strings"
)

// Enabled says this build reads the test hooks.
const Enabled = true

// RegistryLoopback reads MAKO_PLUGIN_REGISTRY_TEST.
func RegistryLoopback() bool { return os.Getenv("MAKO_PLUGIN_REGISTRY_TEST") == "1" }

// NetTest reads MAKO_PLUGIN_NET_TEST.
func NetTest() map[string]string { return parseNetTest(os.Getenv("MAKO_PLUGIN_NET_TEST")) }

// parseNetTest reads MAKO_PLUGIN_NET_TEST's value: host=base URL pairs
// separated by commas.
func parseNetTest(v string) map[string]string {
	out := map[string]string{}
	for pair := range strings.SplitSeq(v, ",") {
		if host, base, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && host != "" && base != "" {
			out[host] = base
		}
	}
	return out
}
