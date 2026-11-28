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
	// SentTo is the session whose composer the comment was put in, until
	// that composer sends; Heading is the comment's heading there, which
	// the message sent must hold for the comment to be resolved.
	SentTo  string
	Heading string
}

func (m *model) addDraftReviewComment(root fileRoot, path string, lines []*protocol.DiffLine, note string) {
	if len(lines) == 0 {
		return
	}
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
// unresolved comments in comments, and the heading each has in it, by ID.
func formatDraftReviewBatch(comments []draftReviewComment) (string, map[string]string) {
	var active []draftReviewComment
	for _, c := range comments {
		if !c.Resolved {
			active = append(active, c)
		}
	}
	if len(active) == 0 {
		return "", nil
	}
	files := map[string]bool{}
	for _, c := range active {
		files[c.Path] = true
	}
	headings := make(map[string]string, len(active))
	var b strings.Builder
	fmt.Fprintf(&b, "Please address the following %d code review comments across %d file(s):\n", len(active), len(files))
	for i, c := range active {
		headings[c.ID] = fmt.Sprintf("### %d. %s", i+1, c.LineRef)
		b.WriteString("\n" + headings[c.ID] + "\n")
		if c.DiffBlock != "" {
			fence := codeFence(c.DiffBlock)
			b.WriteString(fence + "diff\n")
			b.WriteString(c.DiffBlock)
			b.WriteString("\n" + fence + "\n")
		}
		if c.Note != "" {
			b.WriteString(c.Note)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String()), headings
}

// codeFence is a Markdown fence that body, put inside it, cannot close:
// longer than any run of backticks in it, and three at least.
func codeFence(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// draftReviewSent resolves the comments put in session id's composer whose
// headings the message it sends, text, still has as lines of their own,
// once it sends it; the others are open again.
func (m *model) draftReviewSent(id, text string) {
	if !slices.ContainsFunc(ggui.Peek(m.DraftReview), func(c draftReviewComment) bool { return c.SentTo == id }) {
		return
	}
	lines := map[string]bool{}
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		lines[strings.TrimRight(line, " \t")] = true
	}
	m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
		next := slices.Clone(list)
		for i := range next {
			if next[i].SentTo != id {
				continue
			}
			if lines[strings.TrimRight(next[i].Heading, " \t")] {
				next[i].Resolved = true
			}
			next[i].SentTo, next[i].Heading = "", ""
		}
		return next
	})
}

// draftReviewBar shows queued multi-file review comments at the bottom of a
// diff tab and lets the user inspect, resolve, or batch-send them to an agent.
func draftReviewBar(m *model, root fileRoot) ggui.Widget {
	// The drawer's state is the model's: the diff tab builds the bar again
	// whenever its lines change or are picked.
	expanded := m.DraftReviewOpen
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
		// The agents to send to come and go with the sessions, named by
		// their titles.
		m.Sessions.Get()
		titles := m.Titles.Get()
		agents := m.agentsFor(root)
		sendBatch := func(sessionID string) func() {
			return func() {
				text, headings := formatDraftReviewBatch(comments)
				if text == "" {
					return
				}
				m.askAgent(sessionID, text)
				// Only the composer's sending resolves them: the draft may
				// be edited, or cleared, first.
				m.DraftReview.Update(func(list []draftReviewComment) []draftReviewComment {
					next := slices.Clone(list)
					for i := range next {
						if h, ok := headings[next[i].ID]; ok {
							next[i].SentTo, next[i].Heading = sessionID, h
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
			sendBtn = ui.Button(Tf("Send review (%d) to %s", unresolved, sessionTitle(agents[0], titles[agents[0].GetId()])), sendBatch(agents[0].GetId()))
		default:
			items := make([]ggui.Widget, len(agents))
			for i, s := range agents {
				items[i] = ui.MenuItem(sessionTitle(s, titles[s.GetId()]), sendBatch(s.GetId()))
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
		).Gap(6).Align(widget.AlignCenter), func() { expanded.Set(!ggui.Peek(expanded)) }).Ghost().Pad(4, 8).Radius(6).Name(summary)

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
