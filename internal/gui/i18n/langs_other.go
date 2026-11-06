//go:build !windows

package i18n

// platformLanguages are none beyond the locale variables, and on macOS
// AppleLanguages.
func platformLanguages() []string { return nil }
