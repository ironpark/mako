//go:build makotest

package testhooks

import "os"

// Enabled says this build reads the test hooks.
const Enabled = true

// RegistryLoopback reads MAKO_PLUGIN_REGISTRY_TEST.
func RegistryLoopback() bool { return os.Getenv("MAKO_PLUGIN_REGISTRY_TEST") == "1" }

// NetTest reads MAKO_PLUGIN_NET_TEST.
func NetTest() map[string]string { return parseNetTest(os.Getenv("MAKO_PLUGIN_NET_TEST")) }
