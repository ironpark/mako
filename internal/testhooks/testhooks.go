// Package testhooks reads the environment variables with which an
// end-to-end test points a daemon at its own servers on loopback:
// MAKO_PLUGIN_REGISTRY_TEST=1 lets plugin registries and packages be on
// loopback, by http, and leaves the default registry out, and
// MAKO_PLUGIN_NET_TEST=host=base,... sends a granted host's plugin
// requests to another base URL. Both relax what keeps a node's requests
// on the internet, so only a build with the makotest tag reads them; any
// other build ignores them, whatever the environment holds (DEVELOP.md).
package testhooks

import "strings"

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
