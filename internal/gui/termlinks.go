package gui

import (
	"context"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/gostty"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// Links in a terminal follow with ⌘-click, Ctrl-click off macOS. A program
// may mark one with OSC 8; otherwise what is under the pointer is read as
// a web address, a file:// URL or a path. A path is on the session's node,
// wherever the window is, and is followed there: an image to the preview,
// any other file to a tab of its own, at the line it names.

// termLink is a link on a row of the viewport.
type termLink struct {
	target     string // the URI, or the path as written
	row        int
	col0, col1 int // the cells it covers, col1 excluded
}

// linkMods reports whether mods are those that follow a link.
func linkMods(mods ggui.Mods) bool {
	if runtime.GOOS == "darwin" {
		return mods.Meta && !mods.Ctrl
	}
	return mods.Ctrl && !mods.Meta
}

// linkAt is the link at a cell of the viewport, if any.
func (t *termConn) linkAt(col, row int) (termLink, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || col < 0 || row < 0 || col >= int(t.cols) || row >= int(t.rows) {
		return termLink{}, false
	}
	vt := t.vt.Terminal()
	cols := int(t.cols)
	hyperlink := func(x int) string {
		uri, ok, err := vt.HyperlinkAt(gostty.PointTagViewport, uint16(x), uint32(row))
		if err != nil || !ok {
			return ""
		}
		return uri
	}
	if uri := hyperlink(col); uri != "" {
		l := termLink{target: uri, row: row, col0: col, col1: col + 1}
		for l.col0 > 0 && hyperlink(l.col0-1) == uri {
			l.col0--
		}
		for l.col1 < cols && hyperlink(l.col1) == uri {
			l.col1++
		}
		return l, true
	}
	// The row as painted, with the cell each rune starts at; a wide
	// rune's spacer is no rune of its own.
	if !t.hasFrame || len(t.cells) < (row+1)*cols {
		return termLink{}, false
	}
	var line []rune
	var at []int
	i := -1
	for x, c := range t.cells[row*cols : (row+1)*cols] {
		if c.Flags.Wide == gostty.CellWidthSpacerTail || c.Flags.Wide == gostty.CellWidthSpacerHead {
			continue
		}
		if x <= col {
			i = len(line)
		}
		line = append(line, max(c.Codepoint, ' '))
		at = append(at, x)
	}
	start, end, ok := linkIn(line, i)
	if !ok {
		return termLink{}, false
	}
	col1 := cols
	if end < len(at) {
		col1 = at[end]
	}
	return termLink{target: string(line[start:end]), row: row, col0: at[start], col1: col1}, true
}

// pwd is where the program says it is, by OSC 7, or "".
func (t *termConn) pwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ""
	}
	p, _ := t.vt.Terminal().GetPwd()
	if u, err := url.Parse(p); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return p
}

// linkIn finds the link in line around rune i, and returns where it
// starts and ends in line. A path in quotes is the whole of what they
// hold, spaces and all, as is one whose spaces are escaped, and an
// absolute one whose last name runs on past spaces to its extension, as a
// screenshot's does: /tmp/Screenshot 2026-10-27 at 4.41.22.png. Otherwise
// it is the word there, its punctuation trimmed, if that reads as a web
// address or a path.
func linkIn(line []rune, i int) (start, end int, ok bool) {
	if i < 0 || i >= len(line) {
		return 0, 0, false
	}
	if start, end, ok := quotedLink(line, i); ok {
		return start, end, true
	}
	if start, end, ok := spacedPath(line, i); ok {
		return start, end, true
	}
	if linkStop(line[i]) && !escapedSpace(line, i) {
		return 0, 0, false
	}
	start, end = i, i+1
	for start > 0 && (!linkStop(line[start-1]) || escapedSpace(line, start-1)) {
		start--
	}
	for end < len(line) && (!linkStop(line[end]) || escapedSpace(line, end)) {
		end++
	}
	word := string(line[start:end])
	// A scheme starts the link however the word starts.
	if k := strings.Index(word, "://"); k > 0 {
		s := k
		for s > 0 && isSchemeRune(rune(word[s-1])) {
			s--
		}
		start += len([]rune(word[:s]))
	}
	for end > start && strings.ContainsRune(".,;:!?'\"", line[end-1]) {
		end--
	}
	if i < start || i >= end {
		return 0, 0, false
	}
	return start, end, looksLinked(string(line[start:end]))
}

// escapedSpace reports whether line[i] is a space a backslash escapes.
func escapedSpace(line []rune, i int) bool {
	return line[i] == ' ' && i > 0 && line[i-1] == '\\'
}

// quotedLink is the path or address in the quotes around rune i.
func quotedLink(line []rune, i int) (start, end int, ok bool) {
	for _, q := range []rune{'\'', '"'} {
		open := -1
		for k, r := range line {
			if r != q {
				continue
			}
			if open < 0 {
				open = k
				continue
			}
			if open < i && i < k && looksLinked(string(line[open+1:k])) {
				return open + 1, k, true
			}
			open = -1
		}
	}
	return 0, 0, false
}

// extEnd ends a name with a file's extension.
var extEnd = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{0,7}$`)

// spacedPath is an absolute path around rune i whose last name has spaces
// in it, running from the slash that starts it to the first place a word
// ends it with an extension, a few words on at most.
func spacedPath(line []rune, i int) (start, end int, ok bool) {
	for s := i; s >= 0; s-- {
		if !pathStart(line, s) {
			continue
		}
		spaces := 0
		for e := s + 1; e <= len(line); e++ {
			if e < len(line) && !linkStop(line[e]) {
				continue
			}
			// A word ends at e: the path ends there if it has an
			// extension, and goes on past a space if it has not.
			if extEnd.MatchString(string(line[s:e])) {
				if spaces > 0 && s <= i && i < e {
					return s, e, true
				}
				break
			}
			if e == len(line) || line[e] != ' ' || spaces == 6 {
				break
			}
			spaces++
		}
	}
	return 0, 0, false
}

// pathStart reports whether an absolute path, / or ~/, starts at line[s].
func pathStart(line []rune, s int) bool {
	if s > 0 && !linkStop(line[s-1]) {
		return false
	}
	return line[s] == '/' || line[s] == '~' && s+1 < len(line) && line[s+1] == '/'
}

// linkStop reports whether r ends a link: space, quotes, brackets, and
// the lines and bullets terminal programs draw.
func linkStop(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("\"'`<>()[]{}|", r) ||
		r >= 0x2500 && r <= 0x25FF || // box drawing, blocks, shapes
		r >= 0x2190 && r <= 0x21FF || // arrows
		r >= 0x23E9 && r <= 0x23FF // ⏺ and its kin
}

// isURL reports whether s has a scheme, as a web address does.
func isURL(s string) bool { return strings.Index(s, "://") > 0 }

func isSchemeRune(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '-' || r == '.')
}

// fileName is a name with an extension: main.go, notes.md, a.c is not one
// (too short to tell from "e.g"), v1.2 is not either.
var fileName = regexp.MustCompile(`^[\p{L}\p{N}_@+-][\p{L}\p{N}_.@+-]*\.[A-Za-z][A-Za-z0-9]{0,7}$`)

// lineSuffix is a line and column after a path: :12 or :12:5.
var lineSuffix = regexp.MustCompile(`:(\d+)(?::\d+)?$`)

// looksLinked reports whether word reads as a web address or a path.
func looksLinked(word string) bool {
	if isURL(word) {
		return !strings.HasSuffix(word, "://")
	}
	p := lineSuffix.ReplaceAllString(word, "")
	switch {
	case p == "" || strings.Trim(p, "/.~") == "":
		return false
	case strings.HasPrefix(p, "/"), strings.HasPrefix(p, "~/"), strings.HasPrefix(p, "./"), strings.HasPrefix(p, "../"):
		return true
	case strings.Contains(p, "/"):
		return !strings.HasPrefix(p, "-") && !strings.Contains(p, "//")
	}
	dot := strings.LastIndex(p, ".")
	return fileName.MatchString(p) && (len(p)-dot-1 > 1 || dot > 1)
}

// linkPath is the path a link names, and the line it names, from 1, or 0.
func linkPath(target string) (string, int) {
	if strings.HasPrefix(target, "file://") {
		u, err := url.Parse(target)
		if err != nil {
			return "", 0
		}
		return u.Path, 0
	}
	target = strings.ReplaceAll(target, `\ `, " ")
	line := 0
	if m := lineSuffix.FindStringSubmatch(target); m != nil {
		line, _ = strconv.Atoi(m[1])
		target = target[:len(target)-len(m[0])]
	}
	return target, line
}

// followLink follows a link in session id's terminal, whose program is in
// cwd if it said so.
func (m *model) followLink(id, target, cwd string) {
	s, ok := m.session(id)
	if !ok {
		return
	}
	if isURL(target) && !strings.HasPrefix(target, "file://") {
		_ = ggruntime.OpenURL(target)
		return
	}
	p, line := linkPath(target)
	if p == "" {
		return
	}
	if !strings.HasPrefix(p, "~/") && !filepath.IsAbs(p) && !path.IsAbs(p) {
		base := cwd
		if base == "" {
			base = s.GetProject()
		}
		if base == "" {
			p = "~/" + p
		} else {
			p = resolvePath(base, p)
		}
	}
	node := s.GetNodeId()
	// A byte of it says the file is there, and where ~ is on its node.
	req := &protocol.ReadFile{NodeId: m.remoteID(node), Path: p, Max: 1}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		return c.ReadFile(ctx, m.key(), req)
	}, func(f *protocol.File, err error) {
		if err != nil {
			m.toaster.Push(ui.Toast(Tf("Could not open %s", baseName(p)), nodeErr(err)).Destructive())
			return
		}
		it := fileItem{Key: dirKey(node, f.GetPath()), Node: node, Path: f.GetPath(), Name: baseName(f.GetPath())}
		if isImage(it.Name) { // the preview is in the Files tab
			m.DetailsOpen.Set(true)
			m.DetailTab.Set(filesTab)
		}
		m.openItem(it, line)
	})
}
