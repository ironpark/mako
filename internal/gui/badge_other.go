//go:build !darwin

package gui

// setBadge would put label on the app's icon; only the Dock has one.
func setBadge(string) {}
