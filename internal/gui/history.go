package gui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// The right panel's History tab lists the project's commits with the graph
// of their branches beside them, over what the commit chosen changed.

// historyRefresh is how often the shown history is read again.
const historyRefresh = 15 * time.Second

// graphRow is one commit's slice of the graph: the lane its dot is in, and
// the lines through the row, each from a lane at the row's top or middle
// to a lane at its middle or bottom.
type graphRow struct {
	Col   int
	Lanes int // the lanes the row spans
	Lines []graphLine
}

type graphLine struct {
	From, To int
	Lower    bool // from the middle to the bottom; else the top to the middle
}

// commitGraph lays commits out in lanes, children before their parents as
// git log lists them: each lane carries the commit it waits for, a commit
// takes the lane that waited for it, and hands it to its first parent.
func commitGraph(commits []protocol.GitCommit) []graphRow {
	rows := make([]graphRow, len(commits))
	var lanes []string
	free := func(ls []string, not int) int {
		for i, h := range ls {
			if h == "" && i != not {
				return i
			}
		}
		return len(ls)
	}
	put := func(ls []string, i int, h string) []string {
		for len(ls) <= i {
			ls = append(ls, "")
		}
		ls[i] = h
		return ls
	}
	for n, c := range commits {
		row := &rows[n]
		col := slices.Index(lanes, c.Hash)
		incoming := col >= 0
		if !incoming {
			col = free(lanes, -1)
			lanes = put(lanes, col, c.Hash)
		}
		row.Col = col
		next := slices.Clone(lanes)
		for i, h := range lanes {
			switch {
			case h == "":
			case h == c.Hash:
				if i != col || incoming {
					row.Lines = append(row.Lines, graphLine{From: i, To: col})
				}
				next[i] = "" // every child's line ends at the dot
			default:
				row.Lines = append(row.Lines, graphLine{From: i, To: i}, graphLine{From: i, To: i, Lower: true})
			}
		}
		for k, p := range c.Parents {
			j := slices.Index(next, p)
			if j < 0 {
				j = col
				if k > 0 || next[col] != "" {
					j = free(next, col)
				}
				next = put(next, j, p)
			}
			row.Lines = append(row.Lines, graphLine{From: col, To: j, Lower: true})
		}
		for len(next) > 0 && next[len(next)-1] == "" {
			next = next[:len(next)-1]
		}
		row.Lanes = max(len(lanes), len(next), col+1)
		lanes = next
	}
	return rows
}

// laneColors tell the graph's lanes apart, readable on light and dark.
var laneColors = []color.Color{
	hex(0x3B82F6), hex(0x22C55E), hex(0xF59E0B), hex(0xA855F7), hex(0xEC4899), hex(0x06B6D4),
}

const (
	laneWidth  = 12
	historyRow = 40
	maxLanes   = 8 // lanes beyond are left out
)

// graphCell paints one row of the graph.
type graphCell struct {
	row  graphRow
	head bool        // the commit checked out, drawn as a ring
	hole color.Color // the ring's middle: the page, as laid out
}

func (g *graphCell) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.hole = ggui.ResolveColor(tokSurface0, env)
	return c.Constrain(ggui.Sz(float64(min(g.row.Lanes, maxLanes))*laneWidth+4, historyRow))
}

func (g *graphCell) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x := func(lane int) float64 { return r.Origin.X + 2 + float64(lane)*laneWidth + laneWidth/2 }
	top, mid, bottom := r.Origin.Y, r.Origin.Y+r.Size.H/2, r.Origin.Y+r.Size.H
	for _, l := range g.row.Lines {
		if l.From >= maxLanes || l.To >= maxLanes {
			continue
		}
		lane := l.From
		a, b := ggui.Pt(x(l.From), top), ggui.Pt(x(l.To), mid)
		if l.Lower {
			lane = l.To
			a, b = ggui.Pt(x(l.From), mid), ggui.Pt(x(l.To), bottom)
		}
		dst.StrokeLine(a, b, 1.6, laneColors[lane%len(laneColors)])
	}
	if g.row.Col >= maxLanes {
		return
	}
	dot := ggui.Pt(x(g.row.Col), mid)
	dst.FillCircle(dot, 4, laneColors[g.row.Col%len(laneColors)])
	if g.head {
		dst.FillCircle(dot, 2, g.hole)
	}
}

// commitView is the commit chosen in the History tab, and the file of it
// whose diff is shown.
type commitView struct {
	Hash    string
	Info    protocol.GitCommitInfo
	Err     string
	Loading bool
	File    protocol.GitFile // Path "" while the commit's files are listed
	Diff    protocol.Diff
	DiffErr string
	Reading bool
}

func (m *model) openCommit(hash string) {
	root := ggui.Peek(m.GitHistory).Root
	if root.Path == "" {
		return
	}
	m.GitCommit.Set(commitView{Hash: hash, Loading: true})
	req := protocol.GitShow{NodeID: m.remoteID(root.Node), Root: root.Path, Hash: hash}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (protocol.GitCommitInfo, error) {
		return c.GitShow(ctx, m.key(), req)
	}, func(info protocol.GitCommitInfo, err error) {
		if ggui.Peek(m.GitCommit).Hash != hash {
			return
		}
		next := commitView{Hash: hash, Info: info}
		if err != nil {
			next.Err = nodeErr(err)
		}
		m.GitCommit.Set(next)
	})
}

func (m *model) openCommitFile(f protocol.GitFile) {
	cv := ggui.Peek(m.GitCommit)
	root := ggui.Peek(m.GitHistory).Root
	cv.File, cv.Diff, cv.DiffErr, cv.Reading = f, protocol.Diff{}, "", true
	m.GitCommit.Set(cv)
	req := protocol.GitDiff{NodeID: m.remoteID(root.Node), Root: root.Path, Path: f.Path, From: f.From, Commit: cv.Hash}
	fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (protocol.Diff, error) {
		return c.GitDiff(ctx, m.key(), req)
	}, func(d protocol.Diff, err error) {
		now := ggui.Peek(m.GitCommit)
		if now.Hash != cv.Hash || now.File.Path != f.Path {
			return
		}
		now.Diff, now.Reading = d, false
		if err != nil {
			now.DiffErr = nodeErr(err)
		}
		m.GitCommit.Set(now)
	})
}

// shortHash is a commit's hash as people quote it.
func shortHash(h string) string { return h[:min(len(h), 7)] }

// ago writes how long ago t was, briefly.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return T("now")
	case d < time.Hour:
		return Tf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return Tf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return Tf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return t.Local().Format("Jan 2")
	}
	return t.Local().Format("2006-01-02")
}

// refBadges are a commit's branches and tags, a few of them.
func refBadges(refs []string) []ggui.Widget {
	var out []ggui.Widget
	for i, ref := range refs {
		if i == 2 {
			out = append(out, muted(ggui.Text(fmt.Sprintf("+%d", len(refs)-i)).Size(10.5)))
			break
		}
		name, fg, lead := ref, color.Color(tokMuted), "git-branch"
		switch {
		case strings.HasPrefix(ref, "HEAD -> "):
			name, fg = strings.TrimPrefix(ref, "HEAD -> "), tokAccentBright
		case ref == "HEAD":
			fg = tokAccentBright
		case strings.HasPrefix(ref, "tag: "):
			name, lead = strings.TrimPrefix(ref, "tag: "), "tag"
		}
		out = append(out, ggui.Box(ggui.Row(
			icon(lead).Size(10).Color(fg),
			ggui.Box(ggui.Text(name).Size(10.5).Color(fg).NoWrap().Ellipsis()).MaxWidth(90),
		).Gap(3)).Pad(1, 5).Radius(4).Border(1, tokBorder))
	}
	return out
}

// Panel ---------------------------------------------------------------------------

func historyPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	pollWhileShown(m, root, historyTab, historyRefresh, m.loadHistory)
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to see its history.")).Size(13))).Pad(16)
		}
		return historyList(m)
	})
}

func historyList(m *model) ggui.Widget {
	type headed struct {
		Root   fileRoot
		Branch string
	}
	head := ggui.Map(m.GitHistory, func(v historyView) headed {
		return headed{v.Root, cmp.Or(v.History.Current, projectName(v.Root.Path))}
	})
	header := ggui.View(head, func(h headed) ggui.Widget {
		return panelHeader(
			icon("history").Size(15),
			ggui.Expanded(ggui.Text(h.Branch).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			iconButton("cloud-download", T("Fetch"), func() {
				m.gitRun(protocol.GitRun{Op: protocol.GitOpFetch}, T("Fetching…"), nil)
			}),
			iconButton("refresh-cw", T("Refresh"), func() { m.loadHistory(h.Root) }),
		)
	})
	// The list is rebuilt when the commits, or what stands in for them,
	// change; a refresh that finds the same commits leaves it be.
	type listed struct {
		Commits       []protocol.GitCommit
		Err           string
		Loading, More bool
	}
	shown := ggui.Map(m.GitHistory, func(v historyView) listed {
		return listed{v.History.Commits, v.Err, v.Loading && v.History.Commits == nil, v.History.More}
	}).WithEqual(func(a, b listed) bool {
		return a.Err == b.Err && a.Loading == b.Loading && a.More == b.More &&
			slices.EqualFunc(a.Commits, b.Commits, func(x, y protocol.GitCommit) bool {
				return x.Hash == y.Hash && slices.Equal(x.Refs, y.Refs)
			})
	})
	list := ggui.View(shown, func(v listed) ggui.Widget {
		cs := v.Commits
		var note string
		switch {
		case v.Err != "" && len(cs) == 0:
			note = v.Err
		case v.Loading:
			return ggui.Box(ggui.Center(ui.Spinner())).Height(80)
		case len(cs) == 0:
			note = T("No commits yet.")
		}
		if note != "" {
			return ggui.Box(muted(ggui.Text(note).Size(12.5))).Pad(14, 16)
		}
		graph := commitGraph(cs)
		rows := make([]ggui.Widget, 0, len(cs)+1)
		for i, c := range cs {
			rows = append(rows, commitRow(m, c, graph[i]))
		}
		if v.More {
			rows = append(rows, ggui.Box(muted(ggui.Text(Tf("Showing the newest %d commits.", len(cs))).Size(11.5))).Pad(8, 12))
		}
		return ggui.Column(rows...).Stretch()
	})
	open := ggui.Map(m.GitCommit, func(c commitView) bool { return c.Hash != "" })
	body := ggui.Column(gitStatusLine(m), ggui.Expanded(ggui.Scroll(ggui.Padding(list, 4, 0)).Bar(tokSurface3))).Stretch()
	return treeOverPane(header, body, open, 0.5, func() ggui.Widget { return commitPane(m) }, T("Commit divider"))
}

// commitRow is a commit in the list: its slice of the graph, its subject
// and refs, and who made it when.
func commitRow(m *model, c protocol.GitCommit, g graphRow) ggui.Widget {
	head := slices.ContainsFunc(c.Refs, func(r string) bool { return r == "HEAD" || strings.HasPrefix(r, "HEAD -> ") })
	top := append([]ggui.Widget{ggui.Expanded(ggui.Text(c.Subject).Size(12.5).NoWrap().Ellipsis())}, refBadges(c.Refs)...)
	text := ggui.Column(
		ggui.Row(top...).Gap(5).Align(ggui.AlignCenter),
		ggui.Text(c.Author+" · "+ago(c.Time)+" · "+shortHash(c.Hash)).Size(11).Color(tokExtraMuted).NoWrap().Ellipsis(),
	).Gap(2).Stretch()
	content := ggui.Row(&graphCell{row: g, head: head}, ggui.Expanded(text)).Gap(6).Align(ggui.AlignCenter)
	// Only the rows chosen and no longer chosen redraw as the choice moves.
	chosen := ggui.Map(m.GitCommit, func(cv commitView) bool { return cv.Hash == c.Hash })
	row := ggui.View(chosen, func(on bool) ggui.Widget {
		box := ggui.Box(content).Height(historyRow).Pad(0, 10, 0, 6)
		if on {
			box = box.Fill(tokSurface2)
		}
		return box
	})
	tap := ui.ButtonOf(row, func() { m.openCommit(c.Hash) }).Ghost().Pad(0).Radius(0).Name(c.Subject)
	return ui.ContextMenu(tap,
		ui.MenuItem(T("Copy hash"), func() { ggui.CurrentClipboard().Write(c.Hash) }),
		ui.MenuItem(T("Copy subject"), func() { ggui.CurrentClipboard().Write(c.Subject) }),
	).Name(Tf("%s actions", shortHash(c.Hash)))
}

// commitPane is what the chosen commit changed: its message and files, or
// the diff of one of them.
func commitPane(m *model) ggui.Widget {
	head := ggui.View(m.GitCommit, func(cv commitView) ggui.Widget {
		if cv.File.Path != "" {
			return diffHeader(cv.File, ggui.Box(), iconButton("arrow-left", T("Back to the commit"), func() {
				now := ggui.Peek(m.GitCommit)
				now.File, now.Diff, now.DiffErr = protocol.GitFile{}, protocol.Diff{}, ""
				m.GitCommit.Set(now)
			}))
		}
		return panelHeader(
			icon("git-commit-horizontal").Size(15),
			ggui.Expanded(ggui.Text(shortHash(cv.Hash)).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap()),
			iconButton("copy", T("Copy hash"), func() { ggui.CurrentClipboard().Write(cv.Hash) }),
			iconButton("x", T("Close commit"), func() { m.GitCommit.Set(commitView{}) }),
		)
	})
	content := ggui.View(m.GitCommit, func(cv commitView) ggui.Widget {
		switch {
		case cv.Loading:
			return ggui.Center(ui.Spinner())
		case cv.Err != "":
			return paneNote(cv.Err)
		case cv.File.Path != "":
			return diffBody(cv.Reading, cv.DiffErr, cv.Diff)
		}
		info := cv.Info
		rows := []ggui.Widget{
			ggui.Text(info.Commit.Subject).Size(13).Weight(ggui.WeightSemibold).Selectable(),
		}
		if info.Body != "" {
			rows = append(rows, ggui.Text(info.Body).Size(12).Color(tokMuted).Selectable())
		}
		who := info.Commit.Author
		if info.Email != "" {
			who += " <" + info.Email + ">"
		}
		rows = append(rows, ggui.Text(who+"\n"+info.Commit.Time.Local().Format("2006-01-02 15:04")).Size(11.5).Color(tokExtraMuted).Selectable())
		meta := ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).Pad(10, 12)
		files := make([]ggui.Widget, 0, len(info.Files))
		for _, f := range info.Files {
			row := ggui.Row(statusIcon(f.Status), ggui.Expanded(ggui.Text(f.Path).Size(12).NoWrap().Ellipsis()), fileCounts(f)).Gap(7)
			files = append(files, ui.ButtonOf(row, func() { m.openCommitFile(f) }).Ghost().Pad(4, 12).Radius(0).Name(f.Path))
		}
		if len(files) == 0 {
			files = append(files, paneNote(T("No files changed.")))
		}
		return ggui.Scroll(ggui.Column(meta, hline(), ggui.Column(files...).Stretch()).Stretch()).Bar(tokSurface3)
	})
	return lowerPane(head, content)
}
