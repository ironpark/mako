package gui

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

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

// markedTurn is a user turn of marked prompts: each a card, the user's
// own to the right, as the user's turns go, and the rest to the left.
func markedTurn(m *model, id string, info *protocol.SessionInfo, msg *protocol.ChatMessage, marks []chatMark) ggui.Widget {
	state := m.chatState(id)
	sessions := ggui.Peek(m.Sessions)
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
		// message from an agent is not.
		open := state.toolOpen(chatMessageKey(msg)+"\x00mark"+strconv.Itoa(i), mk.Kind != markTemplate)
		header := foldHeader(open, glyph, widget.Text(title).Size(13).Weight(ggui.WeightMedium).NoWrap(), detail, title)
		body := widget.Padding(widget.Text(mk.Text).Size(13.5).Selectable(), 2, 10, 8, 10)
		card := widget.Box(widget.Column(header, reveal(open, func() ggui.Widget { return body })).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(12).Pad(4).MaxWidth(680)
		var w ggui.Widget = &withCopy{content: card, button: copyButton(m, mk.Text)}
		rows = append(rows, widget.Align(w).At(pick(mk.Kind == markTemplate, 1.0, 0.0), 0))
	}
	return widget.Column(rows...).Gap(8).Stretch()
}
