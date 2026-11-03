//go:build !windows

package gui

// platformLanguages are none beyond the locale variables, and on macOS
// AppleLanguages.
func platformLanguages() []string { return nil }
