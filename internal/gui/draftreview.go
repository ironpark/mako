package gui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// draftReviewComment is one queued local code-review comment collected from a
// diff tab so the user can review multiple files and send all feedback to an
// agent in a single structured batch.
type draftReviewComment struct {
	ID        string
	Root      fileRoot
	Path      string
	LineRef   string
	DiffBlock string
	Note      string
	Resolved  bool
	CreatedAt time.Time
}

func (m *model) addDraftReviewComment(root fileRoot, path string, lines []*protocol.DiffLine, note string) {
	m.draftReviewSeq++
	id := "rc-" + strconv.Itoa(m.draftReviewSeq)
	item := draftReviewComment{
		ID:        id,
		Root:      root,
		Path:      path,
		LineRef:   lineRef(path, lines),
		DiffBlock: formatDiffBlock(lines),
		Note:      strings.TrimSpace(note),
		CreatedAt: time.Now(),
	}
	m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
		return append(slices.Clone(list), item)
	})
}

func (m *model) removeDraftReviewComment(id string) {
	m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
		return slices.DeleteFunc(slices.Clone(list), func(c draftReviewComment) bool { return c.ID == id })
	})
}

func (m *model) toggleDraftReviewResolved(id string) {
	m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
		next := slices.Clone(list)
		for i := range next {
			if next[i].ID == id {
				next[i].Resolved = !next[i].Resolved
				break
			}
		}
		return next
	})
}

func (m *model) clearDraftReview(root fileRoot, resolvedOnly bool) {
	m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
		return slices.DeleteFunc(slices.Clone(list), func(c draftReviewComment) bool {
			if c.Root != root {
				return false
			}
			return !resolvedOnly || c.Resolved
		})
	})
}

func draftReviewForRoot(list []draftReviewComment, root fileRoot) []draftReviewComment {
	var out []draftReviewComment
	for _, c := range list {
		if c.Root == root {
			out = append(out, c)
		}
	}
	return out
}

func formatDiffBlock(lines []*protocol.DiffLine) string {
	var b strings.Builder
	for _, l := range lines {
		if l.GetOp() == "@" {
			continue
		}
		b.WriteString(l.GetOp())
		b.WriteString(l.GetText())
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatDraftReviewBatch builds a structured multi-file review prompt from all
// unresolved comments in comments.
func formatDraftReviewBatch(comments []draftReviewComment) string {
	var active []draftReviewComment
	for _, c := range comments {
		if !c.Resolved {
			active = append(active, c)
		}
	}
	if len(active) == 0 {
		return ""
	}
	files := map[string]bool{}
	for _, c := range active {
		files[c.Path] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Please address the following %d code review comments across %d file(s):\n", len(active), len(files))
	for i, c := range active {
		fmt.Fprintf(&b, "\n### %d. %s\n", i+1, c.LineRef)
		if c.DiffBlock != "" {
			b.WriteString("```diff\n")
			b.WriteString(c.DiffBlock)
			b.WriteString("\n```\n")
		}
		if c.Note != "" {
			b.WriteString(c.Note)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// draftReviewBar shows queued multi-file review comments at the bottom of a
// diff tab and lets the user inspect, resolve, or batch-send them to an agent.
func draftReviewBar(m *model, root fileRoot) ggui.Widget {
	expanded := ggui.State(false)
	return ggui.Reactive(func() ggui.Widget {
		comments := draftReviewForRoot(m.DraftReview.Get(), root)
		if len(comments) == 0 {
			return widget.Box()
		}
		files := map[string]bool{}
		unresolved := 0
		for _, c := range comments {
			files[c.Path] = true
			if !c.Resolved {
				unresolved++
			}
		}
		open := expanded.Get()
		agents := m.agentsFor(root)
		sendBatch := func(sessionID string) func() {
			return func() {
				text := formatDraftReviewBatch(comments)
				if text == "" {
					return
				}
				m.askAgent(sessionID, text)
				// Mark sent comments resolved so the user can track follow-up.
				m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
					next := slices.Clone(list)
					for i := range next {
						if next[i].Root == root && !next[i].Resolved {
							next[i].Resolved = true
						}
					}
					return next
				})
			}
		}

		var sendBtn ggui.Widget
		switch {
		case unresolved == 0:
			sendBtn = ui.Button(T("Send review"), func() {}).Outline().Disabled(true)
		case len(agents) == 0:
			sendBtn = ui.Button(T("Send review"), func() {}).Disabled(true).Name(T("No agent runs in this project"))
		case len(agents) == 1:
			sendBtn = ui.Button(Tf("Send review (%d) to %s", unresolved, sessionTitle(agents[0], ggui.Peek(m.Titles)[agents[0].GetId()])), sendBatch(agents[0].GetId()))
		default:
			items := make([]ggui.Widget, len(agents))
			for i, s := range agents {
				items[i] = ui.MenuItem(sessionTitle(s, ggui.Peek(m.Titles)[s.GetId()]), sendBatch(s.GetId()))
			}
			sendBtn = ui.MenuOf(widget.Text(Tf("Send review (%d)", unresolved)), items...).Width(240).Name(T("Send review"))
		}

		summary := Tf("%d review comments · %d files", len(comments), len(files))
		if unresolved < len(comments) {
			summary = Tf("%d open / %d total · %d files", unresolved, len(comments), len(files))
		}
		toggleBtn := ui.ButtonOf(widget.Row(
			icon(pick(open, "chevron-down", "chevron-up")).Size(13).Color(skin.Muted),
			icon("message-square").Size(13).Color(skin.AccentBright),
			widget.Text(summary).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
		).Gap(6).Align(widget.AlignCenter), func() { expanded.Set(!ggui.Peek(expanded)) }).Ghost().Pad(4, 8).Radius(6)

		head := widget.Box(widget.Row(
			toggleBtn,
			widget.Spacer(),
			ui.Button(T("Clear"), func() { m.clearDraftReview(root, false) }).Ghost().Pad(4, 8).Radius(6),
			sendBtn,
		).Gap(8).Align(widget.AlignCenter)).Pad(6, 12).Fill(skin.Surface2)

		rows := []ggui.Widget{hline(), head}
		if open {
			listRows := make([]ggui.Widget, 0, len(comments))
			for _, item := range comments {
				c := item
				checkIcon := pick(c.Resolved, "circle-check", "circle-dot")
				checkCol := pick(c.Resolved, skin.DotRunning, skin.AccentBright)
				noteText := c.Note
				if noteText == "" {
					noteText = T("(No note)")
				}
				listRows = append(listRows, widget.Box(widget.Row(
					ui.Tooltip(ui.ButtonOf(icon(checkIcon).Size(14).Color(checkCol), func() {
						m.toggleDraftReviewResolved(c.ID)
					}).Ghost().Pad(4).Radius(6).Name(pick(c.Resolved, T("Mark unresolved"), T("Mark resolved"))),
						pick(c.Resolved, T("Mark unresolved"), T("Mark resolved"))),
					widget.Expanded(widget.Column(
						widget.Text(c.LineRef).Size(12).Font(ggui.DefaultMonoFont()).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
						muted(widget.Text(noteText).Size(12).NoWrap().Ellipsis()),
					).Gap(2).Stretch()),
					iconButton("x", T("Remove comment"), func() { m.removeDraftReviewComment(c.ID) }),
				).Gap(8).Align(widget.AlignCenter)).Pad(6, 12))
			}
			rows = append(rows, hline(), widget.Box(widget.Scroll(widget.Column(listRows...).Stretch()).Bar(skin.Surface3)).MaxHeight(180).Fill(skin.Surface1))
		}
		return widget.Column(rows...).Stretch()
	})
}
