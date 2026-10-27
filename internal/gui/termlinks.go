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

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// Links in a terminal follow with ⌘-click, Ctrl-click off macOS. A program
// may mark one with OSC 8; otherwise what is under the pointer is read as
// a web address, a file:// URL or a path. A path is on the session's node,
// wherever the window is, and is followed there: an image to the preview,
// any other file to a tab of its own, at the line it names.

// termLink is a link in the viewport. A long one wraps onto the rows
// after the first, so it runs from a cell on one row to a cell on another.
type termLink struct {
	target     string // the URI, or the path as written
	row0, col0 int    // its first cell
	row1, col1 int    // the cell after its last, col1 up to the columns
}

// spans calls fn with the cells the link covers on each of its rows.
func (l termLink) spans(cols int, fn func(row, col0, col1 int)) {
	for row := l.row0; row <= l.row1; row++ {
		c0, c1 := 0, cols
		if row == l.row0 {
			c0 = l.col0
		}
		if row == l.row1 {
			c1 = l.col1
		}
		if c1 > c0 {
			fn(row, c0, c1)
		}
	}
}

// covers reports whether the link covers the cell at col, row.
func (l termLink) covers(col, row int) bool {
	return (row > l.row0 || row == l.row0 && col >= l.col0) && (row < l.row1 || row == l.row1 && col < l.col1)
}

// linkMods reports whether mods are those that follow a link.
func linkMods(mods ggui.Mods) bool {
	if runtime.GOOS == "darwin" {
		return mods.Meta && !mods.Ctrl
	}
	return mods.Ctrl && !mods.Meta
}

// wrapRows is how many rows a link is followed across, either way.
const wrapRows = 8

// linkAt is the link at a cell of the viewport, if any. A row that is full
// to its last column is read as running on into the next, as a long path
// or address does when the terminal wraps it.
func (t *termConn) linkAt(col, row int) (termLink, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || col < 0 || row < 0 || col >= int(t.cols) || row >= int(t.rows) {
		return termLink{}, false
	}
	vt := t.vt.Terminal()
	cols, rows := int(t.cols), int(t.rows)
	// Cells are counted across rows, i = row*cols + col.
	hyperlink := func(i int) string {
		uri, ok, err := vt.HyperlinkAt(gostty.PointTagViewport, uint16(i%cols), uint32(i/cols))
		if err != nil || !ok {
			return ""
		}
		return uri
	}
	here := row*cols + col
	if uri := hyperlink(here); uri != "" {
		lo, hi := max(0, here-wrapRows*cols), min(rows*cols, here+wrapRows*cols)
		i0, i1 := here, here+1
		for i0 > lo && hyperlink(i0-1) == uri {
			i0--
		}
		for i1 < hi && hyperlink(i1) == uri {
			i1++
		}
		return linkBetween(uri, i0, i1, cols), true
	}
	// The rows as painted, with the cell each rune starts at; a wide
	// rune's spacer is no rune of its own.
	if !t.hasFrame || len(t.cells) < rows*cols {
		return termLink{}, false
	}
	runsOn := func(y int) bool { // row y wraps into the next
		c := t.cells[(y+1)*cols-1]
		return c.Flags.Wide == gostty.CellWidthSpacerHead || c.Codepoint != 0 && c.Codepoint != ' '
	}
	r0, r1 := row, row
	for r0 > 0 && row-r0 < wrapRows && runsOn(r0-1) {
		r0--
	}
	for r1 < rows-1 && r1-row < wrapRows && runsOn(r1) {
		r1++
	}
	var line []rune
	var at []int
	i := -1
	for k, c := range t.cells[r0*cols : (r1+1)*cols] {
		if c.Flags.Wide == gostty.CellWidthSpacerTail || c.Flags.Wide == gostty.CellWidthSpacerHead {
			continue
		}
		k += r0 * cols
		if k <= here {
			i = len(line)
		}
		line = append(line, max(c.Codepoint, ' '))
		at = append(at, k)
	}
	start, end, ok := linkIn(line, i)
	if !ok {
		return termLink{}, false
	}
	i1 := (r1 + 1) * cols
	if end < len(at) {
		i1 = at[end]
	}
	return linkBetween(string(line[start:end]), at[start], i1, cols), true
}

// linkBetween is the link to target from cell i0 to before cell i1,
// counted across rows of cols cells.
func linkBetween(target string, i0, i1, cols int) termLink {
	// The cell after a row's last is that row's end, not the next's start.
	last := i1 - 1
	return termLink{target: target, row0: i0 / cols, col0: i0 % cols, row1: last / cols, col1: last%cols + 1}
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
		return c.ReadFile(ctx, req)
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
