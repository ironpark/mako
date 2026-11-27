package gui

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Following a chat: while it shows, the session's node says the moment its
// log grows, which reads it at once, and what of the agent's reply has
// streamed and is not yet logged, at most once a tick of its, which shows
// after the conversation until the log has it (protocol.WatchChat); a
// subagent's conversation too.

// chatFollow is what a chat keeps of following its conversation, on the UI
// goroutine. Once the log grows, what the node says next is held until
// the read of the log lands, so a block leaves the stream as the logged
// one shows.
type chatFollow struct {
	on      bool // the node is followed now
	waiting int  // reads out of a log that grew
	held    []*protocol.LiveBlock
	polled  time.Time // when the chat was last read every few seconds anyway
}

// followChat follows the conversation of session id, or of its subagent
// agent, into state, read again by read, which calls then once the read
// lands, until the function it returns is called, on the UI goroutine;
// nothing for a node too old to be followed.
func (m *model) followChat(id, agent string, state *chatState, read func(then func())) func() {
	info, ok := m.session(id)
	post := m.post // as the goroutine below finds it, read here
	if !ok || post == nil || !m.nodeHas(id, protocol.RevisionLiveChat) {
		return func() {}
	}
	req := &protocol.WatchChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, Agent: agent}
	ctx, cancel := context.WithCancel(context.Background())
	live := func(fn func()) {
		post(func() {
			if ctx.Err() == nil {
				fn()
			}
		})
	}
	go func() {
		for ctx.Err() == nil {
			if c, err := m.link.client(ctx); err == nil {
				_ = c.WatchChat(ctx, req, func(l *protocol.ChatLive) {
					live(func() { state.follow.on = true; tookLive(state, l, read) })
				})
			}
			live(func() { state.follow.on = false })
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second): // the stream broke: again
			}
		}
	}()
	return func() {
		cancel()
		state.follow = chatFollow{}
		state.live.Set(nil)
		state.suggestion.Set("")
		state.dismissedSuggestion.Set("")
	}
}

// readThen reads state by read, and calls then once a read lands that went
// out after it was asked for: another read if one is out.
func readThen(state *chatState, then func(), read func()) {
	state.afterRead = append(state.afterRead, then)
	state.reread = state.loading
	read()
}

// pollChat reports whether a chat's read every few seconds is due: always
// while it is not followed, else once in watchedPollInterval.
func pollChat(state *chatState) bool {
	if state.follow.on && time.Since(state.follow.polled) < watchedPollInterval {
		return false
	}
	state.follow.polled = time.Now()
	return true
}

// splitLiveBlocks separates reply streaming blocks from any inline ghost text
// suggestion block carried on the same ChatLive frame.
func splitLiveBlocks(blocks []*protocol.LiveBlock) (reply []*protocol.LiveBlock, suggestion string) {
	for _, b := range blocks {
		if b.GetKind() == protocol.LiveKindSuggestion {
			suggestion = strings.TrimSpace(b.GetText())
			continue
		}
		reply = append(reply, b)
	}
	return reply, suggestion
}

// tookLive shows what the node said of the chat: a log that grew is read,
// and the reply streaming waits for the read. Ghost text suggestions update
// immediately so the composer stays responsive without waiting on log reads.
func tookLive(state *chatState, l *protocol.ChatLive, read func(then func())) {
	reply, suggestion := splitLiveBlocks(l.GetBlocks())
	// A dismissal holds only while the agent offers the same suggestion.
	if suggestion != ggui.Peek(state.dismissedSuggestion) {
		state.dismissedSuggestion.Set("")
	}
	state.suggestion.Set(suggestion)
	f := &state.follow
	// A log that grows again while it is read is read again: the read out
	// may have gone before it grew, and its last message would wait for
	// the next poll.
	if l.GetLogged() {
		f.waiting++
		read(func() {
			// A follow begun again since has nothing out.
			if f.waiting > 0 {
				if f.waiting--; f.waiting == 0 {
					state.live.Set(f.held)
				}
			}
		})
	}
	if f.waiting > 0 {
		f.held = reply
		return
	}
	state.live.Set(reply)
}

// withLive is the turns logged, then those of the reply streaming: built
// apart, so a frame of the stream builds only its own.
func withLive(logged ggui.Readable[[]chatTurnItem], state *chatState) ggui.Readable[[]chatTurnItem] {
	live := ggui.Derived(func() []chatTurnItem { return liveTurns(state.live.Get()) })
	return ggui.Derived(func() []chatTurnItem {
		tail := live.Get()
		if len(tail) == 0 {
			return logged.Get()
		}
		return append(append([]chatTurnItem(nil), logged.Get()...), tail...)
	})
}

// liveTurns are the reply streaming as turns after the conversation's: its
// text as the agent's, the tool calls it began as calls waiting, with what
// their arguments say so far, and while it thinks, a line that says so.
func liveTurns(blocks []*protocol.LiveBlock) []chatTurnItem {
	var msgs []*protocol.ChatMessage
	for _, b := range blocks {
		key := "live:" + strconv.Itoa(int(b.GetIndex()))
		switch {
		case b.GetKind() == "text" && strings.TrimSpace(b.GetText()) != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "assistant", Text: b.GetText(), Id: key})
		case b.GetKind() == "tool" && b.GetTool() != "":
			msgs = append(msgs, &protocol.ChatMessage{Role: "tool", Tool: b.GetTool(), Detail: argsPreview(b.GetText()), Id: key, Pending: true})
		}
	}
	turns := chatTurns(msgs, nil)
	for i := range turns {
		turns[i].Key = "live:" + turns[i].Key
	}
	if n := len(blocks); n > 0 && blocks[n-1].GetKind() == "thinking" {
		fg, acts := splitLiveStatus(blocks[n-1].GetText())
		if fg != "" || len(acts) == 0 {
			turns = append(turns, chatTurnItem{
				Key:      "live:thinking",
				Messages: []*protocol.ChatMessage{{Role: "assistant"}},
				Thinking: true,
				Status:   fg,
			})
		}
	}
	return turns
}

// liveActivityItem is one structured active Swarm/Agent/Task row extracted from
// live status blocks (such as agy's bottom Swarm, Agent, and background Task rows).
type liveActivityItem struct {
	Kind     string // "swarm", "agent", or "task"
	Title    string
	Detail   string
	Duration string
}

// splitLiveStatus separates foreground spinner status lines (e.g. "Thinking…")
// from structured Swarm(...), Agent(...), and Task(...) live activity rows.
func splitLiveStatus(text string) (status string, activities []liveActivityItem) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	var fg []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if item, ok := parseLiveActivityItem(line); ok {
			activities = append(activities, item)
			continue
		}
		fg = append(fg, line)
	}
	return strings.Join(fg, " · "), activities
}

func parseLiveActivityItem(line string) (liveActivityItem, bool) {
	var kind, rest string
	switch {
	case strings.HasPrefix(line, "Swarm("):
		kind, rest = "swarm", line[len("Swarm("):]
	case strings.HasPrefix(line, "Agent("):
		kind, rest = "agent", line[len("Agent("):]
	case strings.HasPrefix(line, "Task("):
		kind, rest = "task", line[len("Task("):]
	default:
		return liveActivityItem{}, false
	}
	depth := 1
	closeIdx := -1
	for i, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeIdx = i
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx < 0 {
		return liveActivityItem{}, false
	}
	title := strings.TrimSpace(rest[:closeIdx])
	if title == "" {
		return liveActivityItem{}, false
	}
	if kind == "agent" {
		if trimmed, ok := strings.CutPrefix(title, "worker:"); ok {
			if t := strings.TrimSpace(trimmed); t != "" {
				title = t
			}
		}
	}
	tail := strings.TrimSpace(rest[closeIdx+1:])
	tail = strings.TrimSpace(strings.TrimPrefix(tail, "·"))
	var parts []string
	for part := range strings.SplitSeq(tail, "·") {
		if p := strings.TrimSpace(part); p != "" {
			parts = append(parts, p)
		}
	}
	var duration string
	if len(parts) > 0 && (looksLikeElapsed(parts[len(parts)-1]) || looksLikeClockTime(parts[len(parts)-1])) {
		duration = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "working":
			parts[i] = T("Working")
		case "running":
			parts[i] = T("Running")
		}
	}
	return liveActivityItem{
		Kind:     kind,
		Title:    title,
		Detail:   strings.Join(parts, " · "),
		Duration: duration,
	}, true
}

// looksLikeClockTime reports whether s is an HH:MM:SS timestamp such as "06:08:51".
func looksLikeClockTime(s string) bool {
	if len(s) != 8 || s[2] != ':' || s[5] != ':' {
		return false
	}
	for _, i := range []int{0, 1, 3, 4, 6, 7} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// looksLikeElapsed reports whether s looks like an agy duration token such as
// "45s", "6m44s", or "27h53m4s".
func looksLikeElapsed(s string) bool {
	if len(s) < 2 || len(s) > 16 {
		return false
	}
	hasDigit, hasUnit := false, false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case r == 'd' || r == 'h' || r == 'm' || r == 's':
			hasUnit = true
		default:
			return false
		}
	}
	last := s[len(s)-1]
	return hasDigit && hasUnit && (last == 's' || last == 'm' || last == 'h' || last == 'd')
}

// liveActivityItems extracts active Swarm/Agent rows from live blocks, dropping
// Agent rows already represented in runningAgents so they never appear twice.
func liveActivityItems(blocks []*protocol.LiveBlock, running []*protocol.SubAgent) []liveActivityItem {
	var all []liveActivityItem
	for _, b := range blocks {
		if b.GetKind() != "thinking" {
			continue
		}
		_, acts := splitLiveStatus(b.GetText())
		all = append(all, acts...)
	}
	if len(all) == 0 {
		return nil
	}
	var runningDescs []string
	for _, a := range running {
		if a.GetRunning() {
			if d := strings.ToLower(strings.TrimSpace(a.GetDescription())); d != "" {
				runningDescs = append(runningDescs, d)
			}
		}
	}
	var out []liveActivityItem
	for _, item := range all {
		if item.Kind == "agent" && len(runningDescs) > 0 {
			t := strings.ToLower(strings.TrimSpace(item.Title))
			if slices.ContainsFunc(runningDescs, func(d string) bool {
				return d == t || strings.Contains(d, t) || strings.Contains(t, d)
			}) {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

// liveActivityCard surfaces live Swarm and background Agent status rows over
// the composer (for agents like agy whose workers stay active while the main
// prompt is idle). Nothing shows when none are active or the session stopped.
func liveActivityCard(m *model, id string, state *chatState) ggui.Widget {
	items := ggui.Derived(func() []liveActivityItem {
		if s, ok := sessionIn(m.Sessions.Get(), id); !ok || s.GetState() != protocol.StateRunning {
			return nil
		}
		return liveActivityItems(state.live.Get(), state.result.Get().Chat.GetAgents())
	}).WithEqual(slices.Equal[[]liveActivityItem])
	return ggui.View(items, func(list []liveActivityItem) ggui.Widget {
		if len(list) == 0 {
			return widget.Box()
		}
		rows := make([]ggui.Widget, len(list))
		for i, item := range list {
			var cols []ggui.Widget
			cols = append(cols, ui.Spinner().Size(13))
			switch item.Kind {
			case "swarm":
				cols = append(cols, widget.Text(T("Swarm")).Size(11.5).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap())
			case "task":
				cols = append(cols, widget.Text(T("Command")).Size(11.5).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap())
			}
			if item.Kind == "task" {
				cols = append(cols, widget.Expanded(widget.Text(item.Title).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()))
				if item.Detail != "" {
					cols = append(cols, muted(widget.Text(item.Detail).Size(12).NoWrap()))
				}
			} else {
				cols = append(cols, widget.Text(item.Title).Size(12.5).Weight(ggui.WeightMedium).NoWrap())
				if item.Detail != "" {
					cols = append(cols, widget.Expanded(muted(widget.Text(item.Detail).Size(12).NoWrap().Ellipsis())))
				} else {
					cols = append(cols, widget.Spacer())
				}
			}
			if item.Duration != "" {
				cols = append(cols, muted(widget.Text(item.Duration).Size(11.5).NoWrap()))
			}
			rows[i] = widget.Row(cols...).Gap(8).Align(widget.AlignCenter)
		}
		return &lane{child: widget.Box(widget.Column(rows...).Gap(6).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(10).Pad(8, 12)}
	})
}

var (
	liveStepPrefixRe   = regexp.MustCompile(`^(\d+/\d+)\s+steps\s*·\s*(.+)$`)
	liveElapsedTrailRe = regexp.MustCompile(`^(.+?)\s+((?:\d+h\s*)?(?:\d+m\s*)?\d+s)$`)
)

// liveStatusLabel localizes well-known agent activity statuses (including
// optional "N/M steps · " prefixes and trailing elapsed timers like "1m 56s")
// while passing through any agent-specific status text verbatim.
func liveStatusLabel(status string) string {
	s := strings.TrimSpace(status)
	if s == "" {
		return T("Thinking…")
	}
	var prefix, suffix string
	if m := liveStepPrefixRe.FindStringSubmatch(s); m != nil {
		prefix = Tf("%s steps", m[1]) + " · "
		s = strings.TrimSpace(m[2])
	}
	if m := liveElapsedTrailRe.FindStringSubmatch(s); m != nil {
		suffix = " " + m[2]
		s = strings.TrimSpace(m[1])
	}
	return prefix + localizeCoreLiveStatus(s) + suffix
}

func localizeCoreLiveStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "", "Thinking…", "Thinking...":
		return T("Thinking…")
	case "Generating…", "Generating...":
		return T("Generating…")
	case "Reading file…", "Reading file...":
		return T("Reading file…")
	case "Reading files…", "Reading files...":
		return T("Reading files…")
	case "Editing file…", "Editing file...":
		return T("Editing file…")
	case "Editing files…", "Editing files...":
		return T("Editing files…")
	case "Writing file…", "Writing file...":
		return T("Writing file…")
	case "Running command…", "Running command...":
		return T("Running command…")
	case "Running commands…", "Running commands...":
		return T("Running commands…")
	case "Searching…", "Searching...":
		return T("Searching…")
	case "Searching files…", "Searching files...":
		return T("Searching files…")
	case "Listing directory…", "Listing directory...":
		return T("Listing directory…")
	case "Calling MCP tool…", "Calling MCP tool...":
		return T("Calling MCP tool…")
	case "Managing tasks…", "Managing tasks...":
		return T("Managing tasks…")
	case "Checking tasks…", "Checking tasks...":
		return T("Checking tasks…")
	case "Working…", "Working...":
		return T("Working…")
	case "Loading…", "Loading...":
		return T("Loading…")
	case "Starting…", "Starting...":
		return T("Starting…")
	default:
		return strings.TrimSpace(status)
	}
}

// thinkingRow says what the agent is currently doing as its reply streams.
func thinkingRow(status string) ggui.Widget {
	return widget.Row(ui.Spinner().Size(13), muted(widget.Text(liveStatusLabel(status)).Size(12.5))).Gap(8).Align(widget.AlignCenter)
}

// stringField finds the string fields of JSON that may stop short: each
// field's name, and its value so far.
var stringField = regexp.MustCompile(`"(\w+)"\s*:\s*"((?:[^"\\]|\\.)*)`)

// argsPreview is what a tool call's arguments, as far as they have
// streamed, say it does, as its row says once logged (agent.Detail).
func argsPreview(args string) string {
	fields := map[string]any{}
	for _, m := range stringField.FindAllStringSubmatch(args, -1) {
		raw := strings.TrimSuffix(m[2], `\`) // an escape cut in two
		if v, err := strconv.Unquote(`"` + raw + `"`); err == nil {
			fields[m[1]] = v
		} else {
			fields[m[1]] = raw
		}
	}
	return agent.Detail("", fields)
}
