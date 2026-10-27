package gui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
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

// refreshQuick lists the files that match what is typed again.
func (m *model) refreshQuick() {
	if m.quick != nil {
		m.quick.refresh()
	}
}

// quickRows are the files that match q, best first: the name and the
// folder it is in, their letters typed marked.
func (m *model) quickRows(q string) []pickRow {
	qf := m.quickFiles
	var rows []pickRow
	for _, f := range rankFiles(qf.Files, q, maxQuick) {
		p := f.Path
		it := fileItem{Key: dirKey(qf.Root.Node, joinPath(qf.Root.Path, p)), Node: qf.Root.Node, Path: joinPath(qf.Root.Path, p), Name: baseName(p)}
		dir := strings.TrimSuffix(strings.TrimSuffix(p, it.Name), "/")
		inDir, inName := splitHits(f.Hits, utf8.RuneCountInString(p)-utf8.RuneCountInString(it.Name))
		rows = append(rows, pickRow{Label: it.Name, Hits: inName, Detail: dir, DetailHits: inDir,
			Icon: icon(fileIconName(it.Name)).Size(15).Color(tokMuted), Run: func() { m.openFile(it) }})
	}
	return rows
}

// rankedFile is a path that matched, and the runes of it that did.
type rankedFile struct {
	Path string
	Hits []int
}

// rankFiles is the paths holding q's letters in order, best first: the
// letters together, at the starts of words, and in the file's name score
// higher, and shorter paths before longer.
func rankFiles(paths []string, q string, n int) []rankedFile {
	type scored struct {
		rankedFile
		score int
	}
	var hits []scored
	for _, p := range paths {
		score, at, ok := fuzzyMatch(p, q)
		if !ok {
			continue
		}
		name := utf8.RuneCountInString(p[:strings.LastIndexByte(p, '/')+1])
		for _, i := range at {
			if i >= name {
				score += 2 // in the file's name
			}
		}
		hits = append(hits, scored{rankedFile{p, at}, score - len(p)/16})
		if q == "" && len(hits) == n {
			break
		}
	}
	if q != "" {
		slices.SortStableFunc(hits, func(a, b scored) int {
			if a.score != b.score {
				return b.score - a.score
			}
			return len(a.Path) - len(b.Path)
		})
	}
	out := make([]rankedFile, 0, min(len(hits), n))
	for _, h := range hits[:min(len(hits), n)] {
		out = append(out, h.rankedFile)
	}
	return out
}

// quickOpenDialog is quick open over the window.
func quickOpenDialog(m *model) ggui.Widget {
	m.quick = newPicker(m.QuickOpen, m.QuickQuery, m.quickRows)
	m.quick.Placeholder = T("Go to a file by its name…")
	m.quick.Name = T("Go to file")
	m.quick.Empty = T("No files match")
	m.quick.Action = T("Open")
	return m.quick.dialog()
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
