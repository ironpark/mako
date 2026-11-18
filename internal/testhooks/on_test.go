//go:build makotest

package testhooks

import "testing"

// A build with the makotest tag reads the hooks.
func TestHooksAreReadWithTheTag(t *testing.T) {
	t.Setenv("MAKO_PLUGIN_REGISTRY_TEST", "1")
	t.Setenv("MAKO_PLUGIN_NET_TEST", " example.com=http://127.0.0.1:1 , bad, =x")
	if !RegistryLoopback() {
		t.Fatal("MAKO_PLUGIN_REGISTRY_TEST is not read")
	}
	if got := NetTest(); len(got) != 1 || got["example.com"] != "http://127.0.0.1:1" {
		t.Fatalf("MAKO_PLUGIN_NET_TEST read as %v", got)
	}
}
