package gui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// Quick open (⌘P) finds a file of the project shown by the letters of its
// path, in order, and opens it. Search (⌘⇧F) finds a text in the project's
// files, in the right panel's Search tab, and opens a file at the line.
// Both ask the project's node, and leave out what .gitignore does.

// quickFiles is the project's files, as quick open last read them.
type quickFiles struct {
	Root    fileRoot
	Files   []string
	Loading bool
	Err     string
}

// openQuickOpen shows quick open, reading the project's files again.
func (m *model) openQuickOpen() {
	root := ggui.Untrack(m.filesRoot)
	if root.Path == "" {
		return
	}
	m.QuickQuery.Set("")
	cur := m.quickFiles
	if cur.Root != root {
		cur = quickFiles{Root: root}
	}
	cur.Loading = true
	m.quickFiles = cur
	m.refreshQuick()
	m.QuickOpen.Set(true)
	req := &protocol.Search{NodeId: m.remoteID(root.Node), Root: root.Path, Files: true}
	fetch(m, 25*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SearchResult, error) {
		return c.Search(ctx, m.key(), req)
	}, func(r *protocol.SearchResult, err error) {
		if m.quickFiles.Root != root {
			return
		}
		next := quickFiles{Root: root, Files: r.GetFiles()}
		if err != nil {
			next.Files, next.Err = m.quickFiles.Files, nodeErr(err)
		}
		m.quickFiles = next
		m.refreshQuick()
	})
}

// maxQuick is how many files quick open lists for a search.
const maxQuick = 80

// refreshQuick lists the files that match what is typed, best first.
func (m *model) refreshQuick() {
	if m.quick == nil {
		return
	}
	q := ggui.Peek(m.QuickQuery)
	qf := m.quickFiles
	var entries []ui.CommandEntry
	for _, p := range rankFiles(qf.Files, q, maxQuick) {
		it := fileItem{Key: dirKey(qf.Root.Node, joinPath(qf.Root.Path, p)), Node: qf.Root.Node, Path: joinPath(qf.Root.Path, p), Name: baseName(p)}
		label := baseName(p)
		if dir := strings.TrimSuffix(p, baseName(p)); dir != "" {
			label += "  ·  " + strings.TrimSuffix(dir, "/")
		}
		// The palette keeps entries holding the query; these are already
		// matched, by their letters in order.
		entries = append(entries, ui.CommandItem(label, func() { m.openFile(it) }).Keywords(q))
	}
	m.quick.Entries(entries...)
}

// rankFiles is the paths holding q's letters in order, best first: the
// letters together, at the starts of words, and in the file's name score
// higher, and shorter paths before longer.
func rankFiles(paths []string, q string, n int) []string {
	q = strings.ToLower(strings.ReplaceAll(q, " ", ""))
	if q == "" {
		return paths[:min(len(paths), n)]
	}
	type scored struct {
		path  string
		score int
	}
	var hits []scored
	for _, p := range paths {
		if s, ok := fuzzyScore(p, q); ok {
			hits = append(hits, scored{p, s})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return len(a.path) - len(b.path)
	})
	out := make([]string, 0, min(len(hits), n))
	for _, h := range hits[:min(len(hits), n)] {
		out = append(out, h.path)
	}
	return out
}

// fuzzyScore scores a path holding the lowercase letters q in order, and
// reports whether it does.
func fuzzyScore(path, q string) (int, bool) {
	lower := strings.ToLower(path)
	name := strings.LastIndexByte(path, '/') + 1
	score, at, prev := 0, 0, -2
	for _, r := range q {
		i := strings.IndexRune(lower[at:], r)
		if i < 0 {
			return 0, false
		}
		i += at
		switch {
		case i == prev+1:
			score += 6 // together
		case i == 0 || strings.ContainsRune("/_-. ", rune(path[i-1])) || unicode.IsUpper(rune(path[i])) && !unicode.IsUpper(rune(path[i-1])):
			score += 4 // a word's start
		}
		if i >= name {
			score += 2
		}
		prev = i
		at = i + len(string(r))
	}
	return score - len(path)/16, true
}

// quickOpenDialog is quick open over the window.
func quickOpenDialog(m *model) ggui.Widget {
	m.quick = ui.Command(m.QuickQuery).
		Placeholder(T("Go to a file by its name…")).Name(T("Go to file")).
		Hints().HintLabels(T("Navigate"), T("Open")).Empty(T("No files match")).Height(360)
	ggui.Effect(func() ggui.Cleanup {
		m.QuickQuery.Get()
		ggui.Untrack(func() struct{} { m.refreshQuick(); return struct{}{} })
		return nil
	})
	return ui.CommandDialog(m.QuickOpen, m.quick).Name(T("Go to file")).Width(580)
}

// searchView is what the Search tab last found.
type searchView struct {
	Root    fileRoot
	Query   string
	Result  *protocol.SearchResult
	Err     string
	Loading bool
}

// openSearch shows the Search tab, its field ready to type in.
func (m *model) openSearch() {
	m.DetailsOpen.Set(true)
	m.DetailTab.Set(searchTab)
	m.searchFocus = true
}

// runSearch searches the project shown for what the Search field holds.
func (m *model) runSearch() {
	root := ggui.Untrack(m.filesRoot)
	q := ggui.Peek(m.SearchQuery)
	if root.Path == "" || strings.TrimSpace(q) == "" {
		return
	}
	m.Search.Set(searchView{Root: root, Query: q, Loading: true})
	req := &protocol.Search{NodeId: m.remoteID(root.Node), Root: root.Path, Query: q}
	fetch(m, 25*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SearchResult, error) {
		return c.Search(ctx, m.key(), req)
	}, func(r *protocol.SearchResult, err error) {
		if cur := ggui.Peek(m.Search); cur.Root != root || cur.Query != q {
			return // another search since
		}
		next := searchView{Root: root, Query: q, Result: r}
		if err != nil {
			next.Err = nodeErr(err)
		}
		m.Search.Set(next)
	})
}

// searchPanel is the Search tab: a field, and the lines found, by file.
func searchPanel(m *model) ggui.Widget {
	field := ui.TextField(m.SearchQuery).Placeholder(T("Search the project's files")).Name(T("Search")).
		OnSubmit(func(string) { m.runSearch() })
	focus := &focusOnce{child: field, target: field.Input(), want: func() bool {
		if m.searchFocus {
			m.searchFocus = false
			return true
		}
		return false
	}}
	results := ggui.View(m.Search, func(v searchView) ggui.Widget {
		switch {
		case v.Loading:
			return ggui.Box(ggui.Center(ui.Spinner())).Height(80)
		case v.Err != "":
			return ggui.Box(muted(ggui.Text(v.Err).Size(12.5))).Pad(14, 16)
		case v.Query == "":
			return ggui.Box(muted(ggui.Text(T("Type a text and press Enter. A capital letter matches the case.")).Size(12.5))).Pad(14, 16)
		case len(v.Result.GetMatches()) == 0:
			return ggui.Box(muted(ggui.Text(T("Nothing found.")).Size(12.5))).Pad(14, 16)
		}
		return searchResults(m, v)
	})
	return ggui.Column(
		ggui.Box(ggui.Row(ggui.Expanded(focus), iconButton("search", T("Search"), m.runSearch)).Gap(6)).Pad(8, 10),
		hline(),
		ggui.Expanded(ggui.Scroll(results).Bar(tokSurface3)),
	).Stretch()
}

// searchResults lists the lines found under their files.
func searchResults(m *model, v searchView) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
	fold := strings.IndexFunc(v.Query, unicode.IsUpper) < 0
	var rows []ggui.Widget
	files := 0
	for i, match := range v.Result.GetMatches() {
		if i == 0 || v.Result.GetMatches()[i-1].GetPath() != match.GetPath() {
			files++
			n := 0
			for _, o := range v.Result.GetMatches()[i:] {
				if o.GetPath() != match.GetPath() {
					break
				}
				n++
			}
			rows = append(rows, ggui.Box(ggui.Row(
				icon(fileIconName(baseName(match.GetPath()))).Size(14),
				ggui.Expanded(ggui.Text(match.GetPath()).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
				muted(ggui.Text(fmt.Sprint(n)).Size(11.5)),
			).Gap(7)).Pad(6, 12, 2, 12))
		}
		it := fileItem{Key: dirKey(v.Root.Node, joinPath(v.Root.Path, match.GetPath())), Node: v.Root.Node, Path: joinPath(v.Root.Path, match.GetPath()), Name: baseName(match.GetPath())}
		line := match.GetLine()
		text := strings.TrimLeft(untab(match.GetText()), " ")
		row := ggui.Row(
			ggui.Box(ggui.Text(fmt.Sprint(match.GetLine())).Style(mono).Color(tokExtraMuted).NoWrap()).Width(36),
			ggui.Expanded(markQuery(text, v.Query, fold, mono)),
		).Gap(4)
		rows = append(rows, ui.ButtonOf(row, func() { m.openFileAt(it, int(line)) }).Ghost().Pad(2, 12, 2, 18).Radius(0).
			Name(fmt.Sprintf("%s:%d", match.GetPath(), match.GetLine())))
	}
	note := Tf("%d results in %d files", len(v.Result.GetMatches()), files)
	if v.Result.GetTruncated() {
		note = Tf("The first %d results, in %d files", len(v.Result.GetMatches()), files)
	}
	head := ggui.Box(muted(ggui.Text(note).Size(11.5))).Pad(8, 12, 2, 12)
	return ggui.Column(append([]ggui.Widget{head}, rows...)...).Stretch()
}

// markQuery is a found line, the query in it marked.
func markQuery(text, q string, fold bool, mono ggui.TextStyle) ggui.Widget {
	hay := text
	if fold {
		hay, q = strings.ToLower(text), strings.ToLower(q)
	}
	i := strings.Index(hay, q)
	if i < 0 || len(hay) != len(text) {
		return ggui.Text(text).Style(mono).NoWrap().Ellipsis()
	}
	return ggui.RichText(
		ggui.Span(text[:i]),
		ggui.Span(text[i:i+len(q)]).Color(tokAccentBright).Weight(ggui.WeightSemibold),
		ggui.Span(text[i+len(q):]),
	).Style(mono).NoWrap().Ellipsis()
}

// focusOnce focuses its child when want says to, as it is painted.
type focusOnce struct {
	child  ggui.Widget
	target ggui.KeyHandler
	want   func() bool
}

func (f *focusOnce) Layout(c ggui.Constraints, env ggui.Env) ggui.Size { return f.child.Layout(c, env) }

func (f *focusOnce) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(f.child, r)
	if f.want() {
		dst.RequestFocus(f.target)
	}
}
