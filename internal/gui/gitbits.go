package gui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
)

// Shared formatting, polling, and git presentation helpers used across diff
// tabs, forge, file watching, and project panels.

// maxShownDiffLines caps the lines the pane draws of a diff.
const maxShownDiffLines = 2000

// gitRefresh is how often an open diff tab's work tree is polled when its node
// does not watch it.
const gitRefresh = 5 * time.Second

// lineCount writes a count of lines briefly: 2.7k from a thousand up.
func lineCount(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
}

// statusIcon is a change kind's box, in its colour.
func statusIcon(status string) ggui.Widget {
	switch status {
	case protocol.GitAdded:
		return iconColored("square-plus", skin.DiffAdded).Size(14)
	case protocol.GitDeleted:
		return iconColored("square-minus", skin.DiffRemoved).Size(14)
	case protocol.GitRenamed:
		return iconColored("square-arrow-right", skin.DiffModified).Size(14)
	case protocol.GitConflicted:
		return iconColored("shield-alert", skin.DotDown).Size(14)
	}
	return iconColored("square-dot", skin.DiffModified).Size(14)
}

// counts is a row's +added -removed.
func counts(added, removed int) ggui.Widget {
	return widget.Row(
		widget.Text("+"+lineCount(added)).Size(12).Color(skin.DiffAdded).NoWrap(),
		widget.Text("-"+lineCount(removed)).Size(12).Color(skin.DiffRemoved).NoWrap(),
	).Gap(5)
}

// fileCounts is a changed file's +added -removed, or that it is binary.
func fileCounts(f *protocol.GitFile) ggui.Widget {
	if f.GetBinary() {
		return muted(widget.Text(T("binary")).Size(12))
	}
	return counts(int(f.GetAdded()), int(f.GetRemoved()))
}

// diffHeader names a changed file over its diff: how it changed, where a
// rename moved it from, and its lines; lead and tail go before and after.
func diffHeader(f *protocol.GitFile, tail ggui.Widget, lead ...ggui.Widget) ggui.Widget {
	name := f.GetPath()
	if f.GetFrom() != "" {
		name = f.GetFrom() + " → " + f.GetPath()
	}
	return panelHeader(append(lead,
		statusIcon(f.GetStatus()),
		widget.Expanded(widget.Text(name).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		fileCounts(f),
		tail,
	)...)
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

// nodeErr says why a request to a node failed, in words for one whose
// daemon predates the request.
func nodeErr(err error) string {
	if protocol.HasCode(err, protocol.CodeUnsupported) {
		return T("The mako on this node is older and cannot do this. Update it there.")
	}
	return err.Error()
}

// lastLines is the end of a command's output, which says how an operation went.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// repoStamp is the stamps of the sessions on root's node in its
// repository, top being the top of its work tree once known: "" for none.
// A session is in it by its folder, or by the repository it is of, as a
// linked work tree's is, whose commits move the branches all share.
func repoStamp(sessions []*protocol.SessionInfo, root fileRoot, top string) string {
	repos := map[string]bool{}
	for _, s := range sessions {
		p := s.GetProject()
		if s.GetNodeId() == root.Node && s.GetRepo() != "" &&
			(pathscope.Within(p, root.Path) || top != "" && pathscope.Within(p, top) || pathscope.Within(root.Path, p)) {
			repos[s.GetRepo()] = true
		}
	}
	var stamps []string
	for _, s := range sessions {
		if s.GetNodeId() == root.Node && s.GetGitStamp() != "" && repos[s.GetRepo()] {
			stamps = append(stamps, s.GetId()+"="+s.GetGitStamp())
		}
	}
	slices.Sort(stamps)
	return strings.Join(stamps, ",")
}

// pollWhileShown loads a details tab's project when the window shows the
// tab's block, and again every while until it does not, unless rest says
// to rest this time, and at once whenever one of moved does, which says
// the project changed.
func pollWhileShown(m *model, root ggui.Readable[fileRoot], tab string, every time.Duration, load func(fileRoot), rest func(fileRoot) bool, moved ...ggui.Readable[string]) {
	ggui.Effect(func() ggui.Cleanup {
		r := root.Get()
		for _, mv := range moved {
			mv.Get()
		}
		if !m.blockShown(tab) {
			return nil // nothing shows it
		}
		load(r)
		return m.every(every, func() {
			if rest == nil || !rest(r) {
				load(r)
			}
		})
	})
}

// every runs fn on the UI goroutine each period until the cleanup it
// returns is called.
func (m *model) every(period time.Duration, fn func()) ggui.Cleanup {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.post(fn)
			}
		}
	}()
	return func() { close(stop) }
}

// findUiNodeSuffix walks a plugin UiNode tree for the first node whose ID ends
// with "/" + suffix.
func findUiNodeSuffix(n *protocol.UiNode, suffix string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	want := "/" + suffix
	if strings.HasSuffix(n.GetId(), want) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := findUiNodeSuffix(c, suffix); got != nil {
			return got
		}
	}
	return nil
}

