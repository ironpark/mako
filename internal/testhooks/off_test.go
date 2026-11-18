//go:build !makotest

package testhooks

import "testing"

// A build without the makotest tag ignores the hooks, set or not.
func TestHooksAreIgnoredWithoutTheTag(t *testing.T) {
	t.Setenv("MAKO_PLUGIN_REGISTRY_TEST", "1")
	t.Setenv("MAKO_PLUGIN_NET_TEST", "example.com=http://127.0.0.1:1")
	if Enabled || RegistryLoopback() || NetTest() != nil {
		t.Fatal("a build without the makotest tag reads the test hooks")
	}
}
