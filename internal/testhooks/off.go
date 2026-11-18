//go:build !makotest

package testhooks

// Enabled says this build reads the test hooks: it does not.
const Enabled = false

// RegistryLoopback is false: this build reads no test hook.
func RegistryLoopback() bool { return false }

// NetTest is nil: this build reads no test hook.
func NetTest() map[string]string { return nil }
