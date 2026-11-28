package repotrust

import (
	"runtime"
	"strings"
)

// AllowCommand is the command that shows the file at path, on the node
// nodeID ("" for the node the command runs on), asks, and trusts it, as a
// hint prints it for the user to run: each word quoted for this system's
// shell where it needs to be, so that a path with a space or a quote in
// it pastes as one word. A caller prints it through protocol.Printable,
// as any path it prints.
func AllowCommand(nodeID, path string) string {
	return allowCommand(runtime.GOOS, nodeID, path)
}

func allowCommand(goos, nodeID, path string) string {
	words := []string{"mako"}
	if nodeID != "" {
		words = append(words, "-node", quote(goos, nodeID))
	}
	return strings.Join(append(words, "repo-trust", "allow", quote(goos, path)), " ")
}

// quote is s as one word of a command line of goos's shell: as it is when
// nothing in it means anything to the shell, else in single quotes for a
// POSIX shell, or in double quotes for Windows's, where no path has one.
func quote(goos, s string) string {
	plain := func(r rune) bool {
		return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' ||
			strings.ContainsRune("@%+=:,./_-", r) || goos == "windows" && r == '\\'
	}
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !plain(r) }) {
		return s
	}
	if goos == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
