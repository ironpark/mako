//go:build !windows

package agentspec

// shortPath has nothing to shorten outside Windows.
func shortPath(string) string { return "" }
