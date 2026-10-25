package gui

import (
	"path/filepath"
	"strings"

	"github.com/ironpark/ggui"
)

// Files and folders reach the window two ways: dragged from the desktop,
// which ggui delivers as a DropEvent, and dragged from the Files tab, as a
// pathDrag. On a terminal they are typed as paths; in the message box they
// are inserted; anywhere else a folder opens a new session in it.

// pathDrag is a file or folder of the Files tab being dragged.
type pathDrag struct{ Path string }

// dropFolder opens the New session dialog on a folder dropped where
// nothing else takes it, or on the folder a dropped file is in.
func (m *model) dropFolder(ev ggui.DropEvent) {
	for _, f := range ev.Files {
		if f.Path == "" {
			continue
		}
		dir := f.Path
		if !f.Dir {
			dir = filepath.Dir(dir)
		}
		m.openNew("", dir)
		return
	}
}

// shellQuote writes a path as a shell reads it back: as it is when it has
// nothing a shell would split or expand, else in single quotes.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:,@%~", r) || r > 0x7f)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellPaths is paths quoted for a shell, each followed by a space, as a
// terminal types the files dropped on it.
func shellPaths(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(shellQuote(p))
		b.WriteByte(' ')
	}
	return b.String()
}
