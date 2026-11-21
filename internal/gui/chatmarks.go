package gui

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A prompt Mako types into an agent's terminal for someone other than the
// user at the composer goes headed by a line in brackets that says what
// it is: a message another agent sent (internal/daemon/agent_delivery.go),
// a note about one, a plugin's message, an event of a block the agent
// shows, or the prompt a template made of the user's call. The agent
// reads the line; Chat shows each such prompt as a card under what the
// line says, its text after.

// markKind is what a marked prompt is.
type markKind int

const (
	markNone markKind = iota
	markMessage
	markNote
	markPlugin
	markEvent
	markTemplate
)

// chatMark is one marked prompt of a user turn: what it is, who or what
// it is from, and its text. A turn's text before any mark is one of kind
// markNone.
type chatMark struct {
	Kind    markKind
	From    string // a sender's name or a plugin's; a template's name
	Session string // the sender's session, or the one a note is about
	Number  string // the message's number
	Text    string
}

// templateMark heads the prompt template name made.
func templateMark(name string) string { return "[Mako template //" + name + "]" }

var (
	markLine = regexp.MustCompile(`^\[Mako (message|note|window event|template) ([^\]\n]*)\]$`)
	// A session as agentLabel names it: "id", "id (name)", "id (name, on
	// node)" or "id (on node)".
	markSession = regexp.MustCompile(`^(\S+)(?: \(([^)]*)\))?`)
	markNumber  = regexp.MustCompile(`^(\d+)`)
)

// chatMarks splits a user turn's text into its marked prompts; nil when
// it has none.
func chatMarks(text string) []chatMark {
	if !strings.Contains(text, "[Mako ") {
		return nil
	}
	lines := strings.Split(text, "\n")
	var marks []chatMark
	var body []string
	cur := chatMark{}
	flush := func() {
		cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
		if cur.Kind != markNone || cur.Text != "" {
			marks = append(marks, cur)
		}
		body = nil
	}
	found := false
	for _, line := range lines {
		head, ok := parseMark(line)
		if !ok {
			body = append(body, line)
			continue
		}
		found = true
		flush()
		cur = head
	}
	if !found {
		return nil
	}
	flush()
	return marks
}

// parseMark reads a mark's line.
func parseMark(line string) (chatMark, bool) {
	sub := markLine.FindStringSubmatch(strings.TrimRight(line, " \r"))
	if sub == nil {
		return chatMark{}, false
	}
	kind, rest := sub[1], sub[2]
	if kind == "template" {
		name, ok := strings.CutPrefix(rest, "//")
		if !ok || name == "" {
			return chatMark{}, false
		}
		return chatMark{Kind: markTemplate, From: name}, true
	}
	if kind == "window event" {
		return chatMark{Kind: markEvent, Number: markNumber.FindString(rest)}, true
	}
	number := markNumber.FindString(rest)
	rest = strings.TrimSpace(strings.TrimPrefix(rest, number))
	mark := chatMark{Kind: markMessage, Number: number}
	switch {
	case kind == "note":
		mark.Kind = markNote
		rest = strings.TrimPrefix(rest, "about session ")
	case strings.HasPrefix(rest, "from the plugin "):
		mark.Kind = markPlugin
		plugin, _, _ := strings.Cut(strings.TrimPrefix(rest, "from the plugin "), ":")
		mark.From = plugin
		return mark, true
	default:
		rest = strings.TrimPrefix(rest, "from session ")
	}
	if s := markSession.FindStringSubmatch(rest); s != nil {
		id, name := s[1], s[2]
		name, _, _ = strings.Cut(name, ",")
		if strings.HasPrefix(name, "on ") {
			name = ""
		}
		mark.Session, mark.From = strings.TrimSuffix(id, "."), name
	}
	return mark, true
}

// markPreviewMaxHeight caps a long agent/marked message on initial display
// (~20 lines of body prose), with a soft bottom fade and Show more / Collapse
// controls.
const (
	markPreviewMaxHeight = 420.0
	markPreviewFade      = 44.0
)

// markedTurn is a user turn of marked prompts: each a card, the user's
// own to the right, as the user's turns go, and the rest to the left.
func markedTurn(m *model, id string, info *protocol.SessionInfo, msg *protocol.ChatMessage, marks []chatMark) ggui.Widget {
	state := m.chatState(id)
	sessions := ggui.Peek(m.Sessions)
	var host mdview.Host = mdview.Base{}
	if m != nil && info != nil {
		host = pageHost{m: m, id: id, doc: docInfo{Node: info.GetNodeId(), Path: joinPath(info.GetProject(), "conversation.md")}}
	}
	rows := make([]ggui.Widget, 0, len(marks))
	for i, mk := range marks {
		if mk.Kind == markNone {
			bubble := widget.Box(widget.Text(mk.Text).Size(14).Selectable()).Fill(skin.Surface2).Radius(12).Pad(10, 14).MaxWidth(680)
			rows = append(rows, widget.Align(bubble).At(1, 0))
			continue
		}
		var glyph ggui.Widget
		var title, detail string
		switch mk.Kind {
		case markMessage, markNote:
			s, ok := sessionIn(sessions, mk.Session)
			title = cmp.Or(mk.From, s.GetName(), mk.Session)
			if ok {
				glyph = agentGlyph(s.GetAgent(), skin.Muted).Size(14)
			} else {
				glyph = icon("message-square").Size(14).Color(skin.Muted)
			}
			detail = pick(mk.Kind == markNote, Tf("Mako note %s", mk.Number), Tf("Mako message %s", mk.Number))
		case markPlugin:
			glyph, title, detail = icon("sparkles").Size(14).Color(skin.Muted), mk.From, T("Plugin message")
		case markEvent:
			glyph, title, detail = icon("info").Size(14).Color(skin.Muted), T("Window event"), mk.Number
		case markTemplate:
			glyph, title, detail = icon("file-text").Size(14).Color(skin.AccentBright), "//"+mk.From, T("Prompt template")
		}
		// A template's prompt is the user's, said at length: folded, as a
		// message from an agent is not. Long open messages preview up to
		// markPreviewMaxHeight (~20 lines) with expand and full-collapse actions.
		key := chatMessageKey(msg) + "\x00mark" + strconv.Itoa(i)
		open := state.toolOpen(key, mk.Kind != markTemplate)
		expanded := state.toolOpen(key+"\x00full", false)
		header := foldHeader(open, glyph, widget.Text(title).Size(13).Weight(ggui.WeightMedium).NoWrap(), detail, title)
		body := reveal(open, func() ggui.Widget {
			var rendered ggui.Widget
			if mk.Kind == markEvent {
				rendered = toolBlock(mk.Text, false)
			} else {
				rendered = mdview.Column(host, mk.Text)
			}
			padded := widget.Padding(rendered, 2, 10, 8, 10)
			return ggui.View(expanded, func(full bool) ggui.Widget {
				return &clampedMarkBody{
					content:     padded,
					expandBar:   markBodyBar(T("Show more"), "chevron-down", func() { expanded.Set(true) }, T("Collapse"), func() { open.Set(false) }),
					collapseBar: markBodyBar(T("Show less"), "chevron-up", func() { expanded.Set(false) }, T("Collapse"), func() { open.Set(false) }),
					maxH:        markPreviewMaxHeight,
					fadeH:       markPreviewFade,
					expanded:    full,
				}
			})
		})
		card := widget.Box(widget.Column(header, body).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(12).Pad(4).MaxWidth(680)
		var w ggui.Widget = &withCopy{content: card, button: copyButton(m, mk.Text)}
		rows = append(rows, widget.Align(w).At(pick(mk.Kind == markTemplate, 1.0, 0.0), 0))
	}
	return widget.Column(rows...).Gap(8).Stretch()
}

// markBodyBar sits under a long marked card's body, letting the reader expand
// or shorten the preview, or completely collapse the card, while leaving the
// bottom-right corner clear for the hover copy button.
func markBodyBar(primaryLabel, primaryIcon string, onPrimary func(), collapseLabel string, onCollapse func()) ggui.Widget {
	primary := ui.ButtonOf(widget.Row(
		icon(primaryIcon).Size(12).Color(skin.Muted),
		widget.Text(primaryLabel).Size(12).Color(skin.Muted).NoWrap(),
	).Gap(4).Align(widget.AlignCenter), onPrimary).Ghost().Pad(3, 8).Radius(6).Name(primaryLabel)
	collapse := ui.ButtonOf(widget.Text(collapseLabel).Size(12).Color(skin.ExtraMuted).NoWrap(), onCollapse).
		Ghost().Pad(3, 8).Radius(6).Name(collapseLabel)
	return widget.Box(widget.Column(
		hline(),
		widget.Padding(widget.Row(primary, collapse, widget.Spacer()).Gap(4).Align(widget.AlignCenter), 4, 6, 2, 6),
	).Stretch())
}

// clampedMarkBody shows content in full when it fits within maxH (or when
// expanded), and otherwise clips it to maxH with a soft bottom fade and shows
// expandBar below. Expanded tall bodies keep collapseBar below so they can be
// returned to preview or fully collapsed without scrolling back to the header.
type clampedMarkBody struct {
	content     ggui.Widget
	expandBar   ggui.Widget
	collapseBar ggui.Widget
	maxH        float64
	fadeH       float64
	expanded    bool

	contentSize ggui.Size
	barSize     ggui.Size
	clipped     bool
}

func (c *clampedMarkBody) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	bodyCS := ggui.Constraints{MinW: cs.MinW, MaxW: cs.MaxW, MaxH: ggui.Unbounded}
	c.contentSize = c.content.Layout(bodyCS, env)
	if c.contentSize.H <= c.maxH+1 {
		c.clipped = false
		c.barSize = ggui.Size{}
		return cs.Constrain(c.contentSize)
	}
	if !c.expanded {
		c.clipped = true
		c.barSize = c.expandBar.Layout(bodyCS, env)
		return cs.Constrain(ggui.Sz(max(c.contentSize.W, c.barSize.W), c.maxH+c.barSize.H))
	}
	c.clipped = false
	c.barSize = c.collapseBar.Layout(bodyCS, env)
	return cs.Constrain(ggui.Sz(max(c.contentSize.W, c.barSize.W), c.contentSize.H+c.barSize.H))
}

func (c *clampedMarkBody) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if c.clipped {
		clipH := min(c.maxH, r.Size.H)
		viewRect := ggui.Rct(r.Origin, ggui.Sz(r.Size.W, clipH))
		contentRect := ggui.Rct(r.Origin, ggui.Sz(r.Size.W, c.contentSize.H))
		dst.Clip(viewRect).FadeEdges(viewRect, false, 0, min(c.fadeH, clipH), func(dst *ggui.Canvas) {
			dst.Paint(c.content, contentRect)
		})
		barRect := ggui.Rct(ggui.Pt(r.Origin.X, r.Origin.Y+clipH), ggui.Sz(r.Size.W, c.barSize.H))
		dst.Paint(c.expandBar, barRect)
		return
	}
	contentRect := ggui.Rct(r.Origin, ggui.Sz(r.Size.W, c.contentSize.H))
	dst.Paint(c.content, contentRect)
	if c.barSize.H > 0 {
		barRect := ggui.Rct(ggui.Pt(r.Origin.X, r.Origin.Y+c.contentSize.H), ggui.Sz(r.Size.W, c.barSize.H))
		dst.Paint(c.collapseBar, barRect)
	}
}
