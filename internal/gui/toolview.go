package gui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A tool call's fold shows what the call did, read from its arguments in
// the form protocol gives its kind, which the agent's driver put them in: a
// command and what it printed, an edit as a diff, a file read or written as
// code, a plan as a checklist. A tool of no kind shows its arguments field
// by field, and what it cannot read as they are.

// maxToolLines caps the lines of code or diff a tool's fold shows.
const maxToolLines = 400

// maxToolHeight caps a fold's code, diff or output, which scrolls inside.
const maxToolHeight = 360

// toolBody is a tool call's fold.
func toolBody(m *model, msg *protocol.ChatMessage) ggui.Widget {
	_, _, args, result, failed := toolParts(msg)
	kind := msg.GetKind()
	if failed && result == "" {
		result = T("The tool failed without output.")
	}
	var in map[string]any // nil for arguments that are not a JSON object
	_ = json.Unmarshal([]byte(args), &in)
	var rows []ggui.Widget
	if !msg.GetOutput() {
		rows = toolInputFor(m, msg.GetTool(), kind, args, in)
	}
	if result != "" {
		rows = append(rows, toolOutput(m, msg, in, result, failed))
	}
	if len(rows) == 0 {
		return widget.Padding(muted(widget.Text(T("No output")).Size(12)), 4, 6, 6, 6)
	}
	return widget.Padding(widget.Column(rows...).Gap(6).Stretch(), 4, 6, 6, 18)
}

// toolInput is what a call asked for, as its kind reads.
func toolInput(m *model, kind string, args string, in map[string]any) []ggui.Widget {
	return toolInputFor(m, "", kind, args, in)
}

// toolInputFor renders structured inputs for a tool call, supporting both
// canonical protocol kinds and agy swarm/task coordination tools.
func toolInputFor(m *model, tool, kind, args string, in map[string]any) []ggui.Widget {
	if in == nil {
		if args == "" {
			return nil
		}
		return []ggui.Widget{toolLabel(T("Arguments")), toolBlock(args, false)}
	}
	path := agent.Str(in, "path")
	switch kind {
	case protocol.ToolShell:
		cmd := agent.Str(in, "command")
		if cmd == "" {
			break
		}
		var rows []ggui.Widget
		if d := agent.Str(in, "description"); d != "" {
			rows = append(rows, muted(widget.Text(d).Size(12.5)))
		}
		return append(rows, commandBlock(cmd, agent.Str(in, "cwd")))
	case protocol.ToolPatch:
		return patchView(agent.Str(in, "patch"))
	case protocol.ToolEdit:
		return []ggui.Widget{pathLine(path, ""), editDiff(agent.Str(in, "old"), agent.Str(in, "new"))}
	case protocol.ToolMultiEdit:
		edits := agent.List(in, "edits")
		rows := []ggui.Widget{pathLine(path, Tf("%d edits", len(edits)))}
		for _, e := range edits {
			if e, ok := e.(map[string]any); ok {
				rows = append(rows, editDiff(agent.Str(e, "old"), agent.Str(e, "new")))
			}
		}
		return rows
	case protocol.ToolWrite:
		content := agent.Str(in, "content")
		return []ggui.Widget{pathLine(path, Tf("%d lines", textLines(content))), codeView(m, path, content)}
	case protocol.ToolRead:
		return []ggui.Widget{pathLine(path, readRange(in))}
	case protocol.ToolSearch:
		return []ggui.Widget{fieldChips(in, "pattern", "path", "glob", "type", "mode")}
	case protocol.ToolTodo, protocol.ToolPlan:
		var rows []ggui.Widget
		if e := agent.Str(in, "explanation"); e != "" {
			rows = append(rows, widget.Text(e).Size(12.5))
		}
		return append(rows, checklist(agent.List(in, "items")))
	case protocol.ToolExitPlan:
		return []ggui.Widget{scrolled(toolFrame(markdownText(m, agent.Str(in, "plan"))).Pad(8, 12))}
	case protocol.ToolFetch:
		return []ggui.Widget{fieldChips(in, "url"), proseBlock(agent.Str(in, "prompt"))}
	case protocol.ToolWebSearch:
		return []ggui.Widget{fieldChips(in, "query", "allowed_domains", "blocked_domains")}
	case protocol.ToolTask:
		var rows []ggui.Widget
		if d := agent.Str(in, "description"); d != "" {
			rows = append(rows, widget.Text(d).Size(13).Weight(ggui.WeightMedium))
		}
		if a := agent.Str(in, "agent"); a != "" {
			rows = append(rows, fieldChips(map[string]any{"agent": a}, "agent"))
		}
		return append(rows, proseBlock(agent.Str(in, "prompt")))
	case protocol.ToolQuestion:
		return toolQuestionRows(in)
	}
	if rows := swarmToolInput(tool, in); len(rows) > 0 {
		return rows
	}
	return fieldRows(in)
}

// toolOutput is what a call gave back, as its kind reads.
func toolOutput(m *model, msg *protocol.ChatMessage, in map[string]any, result string, failed bool) ggui.Widget {
	label := pick(failed, T("Error"), T("Output"))
	if failed {
		return widget.Column(toolLabel(label), scrolled(toolBlock(result, true))).Gap(4).Stretch()
	}
	switch msg.GetKind() {
	case protocol.ToolShell:
		head := []ggui.Widget{toolLabel(label)}
		code := -1
		if msg.ExitCode != nil {
			code = int(msg.GetExitCode())
			head = append(head, exitBadge(code))
		}
		block := ggui.Widget(muted(widget.Text(T("No output")).Size(12)))
		if strings.TrimSpace(result) != "" {
			block = scrolled(toolBlock(result, code > 0))
		}
		return widget.Column(widget.Row(head...).Gap(8).Align(widget.AlignCenter), block).Gap(4).Stretch()
	case protocol.ToolRead:
		first, _ := in["offset"].(float64)
		return widget.Column(toolLabel(Tf("From line %d", max(int(first), 1))), codeView(m, agent.Str(in, "path"), result)).Gap(4).Stretch()
	}
	return widget.Column(toolLabel(label), scrolled(toolBlock(result, false))).Gap(4).Stretch()
}

// toolGlyph is the icon a tool row shows for its kind, one of protocol's
// Tool kinds.
func toolGlyph(kind string) string {
	switch kind {
	case protocol.ToolCommand:
		return "terminal"
	case protocol.ToolSummary:
		return "history"
	case protocol.ToolShell:
		return "square-terminal"
	case protocol.ToolPatch, protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolRead:
		return "file-text"
	case protocol.ToolSearch:
		return "radar"
	case protocol.ToolFetch, protocol.ToolWebSearch:
		return "network"
	case protocol.ToolTask, protocol.ToolTodo, protocol.ToolPlan:
		return "list"
	}
	return "square-dot"
}

// readRange is the lines a Read asked for, "" for the whole file.
func readRange(in map[string]any) string {
	offset, _ := in["offset"].(float64)
	limit, _ := in["limit"].(float64)
	switch {
	case offset > 0 && limit > 0:
		return Tf("Lines %d–%d", int(offset), int(offset+limit-1))
	case offset > 0:
		return Tf("From line %d", int(offset))
	case limit > 0:
		return Tf("First %d lines", int(limit))
	}
	return ""
}

func textLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// pathLine is the file a tool works on, with its icon, and a note after.
func pathLine(path, note string) ggui.Widget {
	row := []ggui.Widget{
		icon(fileIconName(baseName(path))).Size(14).Color(skin.Muted),
		widget.Flex(widget.Text(path).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis().Selectable(), 1),
	}
	if note != "" {
		row = append(row, muted(widget.Text(note).Size(12).NoWrap()))
	}
	return widget.Row(row...).Gap(6).Align(widget.AlignCenter)
}

// commandBlock is a command as a terminal shows it, with where it runs.
func commandBlock(cmd, dir string) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 12}
	text := widget.RichText(widget.Span("$ ").Color(skin.AccentBright), widget.Span(strings.TrimRight(cmd, "\n"))).
		Style(mono).LineHeight(1.4).Selectable()
	rows := []ggui.Widget{text}
	if dir != "" {
		rows = append(rows, widget.Text(dir).Style(mono).Color(skin.ExtraMuted).NoWrap().Ellipsis())
	}
	return toolFrame(widget.Column(rows...).Gap(4).Stretch()).Pad(8, 10)
}

// exitBadge is a command's exit code, green for success.
func exitBadge(code int) ggui.Widget {
	c := pick(code == 0, skin.DiffAdded, skin.DotDown)
	return widget.Box(widget.Text(Tf("exit %d", code)).Size(11).Font(ggui.DefaultMonoFont()).Color(c).NoWrap()).
		Pad(1, 6).Border(1, c).Radius(4)
}

// codeView is a file's text, highlighted in its language, capped at
// maxToolLines.
func codeView(m *model, path, src string) ggui.Widget {
	lines := strings.Split(src, "\n")
	cut := len(lines) > maxToolLines
	if cut {
		src = strings.Join(lines[:maxToolLines], "\n")
	}
	l, ok := m.hl.languageFor(path, src)
	rows := []ggui.Widget{widget.Scroll(widget.Padding(codeText(m, src, l, ok, 12), 8, 10)).Horizontal().Bar(skin.Surface3)}
	if cut {
		rows = append(rows, widget.Box(muted(widget.Text(Tf("%d more lines", len(lines)-maxToolLines)).Size(11.5))).Pad(4, 10, 8, 10))
	}
	return scrolled(toolFrame(widget.Column(rows...).Stretch()))
}

// scrolled caps w at maxToolHeight, scrolling what is over; shorter, it
// is as tall as it is.
func scrolled(w ggui.Widget) ggui.Widget {
	return &capHeight{content: w, scroll: widget.Scroll(w).Bar(skin.Surface3), max: maxToolHeight}
}

// capHeight is content as tall as it is up to max, and scroll, which
// holds it, at max beyond. A Scroll alone takes all the height it may.
type capHeight struct {
	content, scroll ggui.Widget
	max             float64
}

func (c *capHeight) Layout(cs ggui.Constraints, env ggui.Env) ggui.Size {
	natural := c.content.Layout(ggui.Constraints{MaxW: cs.MaxW, MaxH: ggui.Unbounded}, env)
	h := min(natural.H, c.max, cs.MaxH)
	return c.scroll.Layout(ggui.Constraints{MinW: cs.MinW, MaxW: cs.MaxW, MinH: h, MaxH: h}, env)
}

func (c *capHeight) Paint(dst *ggui.Canvas, r ggui.Rect) { dst.Paint(c.scroll, r) }

// editDiff is an edit's change as diff lines, the words that differ marked.
func editDiff(old, cur string) ggui.Widget {
	return diffBlock(gitstat.TextDiff(old, cur))
}

// diffBlock draws diff lines in one column, their hunk headers as gaps:
// the numbers of a fragment's lines say nothing of the file's.
func diffBlock(lines []*protocol.DiffLine) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 12}
	shown := lines[:min(len(lines), maxToolLines)]
	words := changedWords(shown)
	var rows []ggui.Widget
	for i, l := range shown {
		switch l.GetOp() {
		case "@":
			if i > 0 {
				rows = append(rows, widget.Box(muted(widget.Text("⋯").Style(mono))).Pad(0, 10).Fill(skin.Surface2))
			}
			continue
		case "+", "-":
		default:
			l = &protocol.DiffLine{Op: " ", Text: l.GetText()}
		}
		row := widget.Box(diffText(l, words[i], diffMode{Wrap: true}, mono, l.GetOp()+" ")).Pad(1, 10)
		switch l.GetOp() {
		case "+":
			row = row.Fill(skin.DiffAddedBg)
		case "-":
			row = row.Fill(skin.DiffRemovedBg)
		}
		rows = append(rows, row)
	}
	if len(shown) < len(lines) {
		rows = append(rows, widget.Box(muted(widget.Text(T("The rest of the diff is not shown.")).Size(11.5))).Pad(4, 10))
	}
	if len(rows) == 0 {
		rows = append(rows, widget.Box(muted(widget.Text(T("No changes")).Size(12))).Pad(6, 10))
	}
	return scrolled(toolFrame(widget.Column(rows...).Stretch()).Pad(4, 0))
}

// patchFile is one file of an apply_patch patch.
type patchFile struct {
	Op, Path string
	Lines    []*protocol.DiffLine
}

// parsePatch reads the patch Codex's apply_patch takes: files begun with
// "*** Add File:", "*** Update File:" or "*** Delete File:", their hunks
// begun with "@@", their lines with " ", "-" or "+".
func parsePatch(patch string) []patchFile {
	var files []patchFile
	for l := range strings.SplitSeq(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "*** Begin Patch"), strings.HasPrefix(l, "*** End Patch"), strings.HasPrefix(l, "*** End of File"):
		case strings.HasPrefix(l, "*** Add File: "):
			files = append(files, patchFile{Op: "add", Path: strings.TrimPrefix(l, "*** Add File: ")})
		case strings.HasPrefix(l, "*** Update File: "):
			files = append(files, patchFile{Op: "update", Path: strings.TrimPrefix(l, "*** Update File: ")})
		case strings.HasPrefix(l, "*** Delete File: "):
			files = append(files, patchFile{Op: "delete", Path: strings.TrimPrefix(l, "*** Delete File: ")})
		case strings.HasPrefix(l, "*** Move to: ") && len(files) > 0:
			f := &files[len(files)-1]
			f.Path += " → " + strings.TrimPrefix(l, "*** Move to: ")
		case len(files) == 0:
		case strings.HasPrefix(l, "@@"):
			f := &files[len(files)-1]
			f.Lines = append(f.Lines, &protocol.DiffLine{Op: "@", Text: l})
		case l != "":
			f := &files[len(files)-1]
			op := l[:1]
			if op != "+" && op != "-" {
				op = " "
			}
			text := l
			if l[0] == '+' || l[0] == '-' || l[0] == ' ' {
				text = l[1:]
			}
			f.Lines = append(f.Lines, &protocol.DiffLine{Op: op, Text: text})
		}
	}
	return files
}

// patchView is a patch, a diff for each file it changes.
func patchView(patch string) []ggui.Widget {
	files := parsePatch(patch)
	if len(files) == 0 {
		return []ggui.Widget{toolLabel(T("Arguments")), scrolled(toolBlock(patch, false))}
	}
	var rows []ggui.Widget
	for _, f := range files {
		note := map[string]string{"add": T("New file"), "delete": T("Deleted"), "update": ""}[f.Op]
		rows = append(rows, pathLine(f.Path, note))
		if len(f.Lines) > 0 {
			rows = append(rows, diffBlock(f.Lines))
		}
	}
	return rows
}

// checklist is a plan's steps, {text, status}, each marked done, under way
// or not yet.
func checklist(items []any) ggui.Widget {
	var rows []ggui.Widget
	for _, it := range items {
		step, ok := it.(map[string]any)
		if !ok {
			continue
		}
		status := agent.Str(step, "status")
		glyph := icon("circle").Size(14).Color(skin.ExtraMuted)
		text := widget.Text(agent.Str(step, "text")).Size(12.5)
		switch status {
		case "completed", "done":
			glyph = icon("circle-check").Size(14).Color(skin.DiffAdded)
			text = text.Color(skin.Muted)
		case "skipped":
			glyph = icon("circle").Size(14).Color(skin.Muted)
			text = text.Color(skin.Muted)
		case "failed":
			glyph = icon("circle-x").Size(14).Color(skin.DiffRemoved)
		case "in_progress":
			glyph = icon("circle-dot").Size(14).Color(skin.AccentBright)
			text = text.Weight(ggui.WeightMedium)
		}
		rows = append(rows, widget.Row(glyph, widget.Expanded(text)).Gap(8).Align(widget.AlignStart))
	}
	if len(rows) == 0 {
		return muted(widget.Text(T("No steps")).Size(12))
	}
	return widget.Box(widget.Column(rows...).Gap(6).Stretch()).Pad(2, 2)
}

// fieldChips are the arguments keys name, each a label and its value on
// one line, in the order given.
func fieldChips(in map[string]any, keys ...string) ggui.Widget {
	var chips []ggui.Widget
	for _, k := range keys {
		v := scalar(in[k])
		if v == "" {
			continue
		}
		chips = append(chips, widget.Row(
			widget.Text(k).Size(11).Color(skin.ExtraMuted).NoWrap(),
			widget.Flex(widget.Text(v).Size(12).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis().Selectable(), 1),
		).Gap(6).Align(widget.AlignCenter))
	}
	if len(chips) == 0 {
		return widget.Box()
	}
	return widget.Column(chips...).Gap(4).Stretch()
}

// scalar is a value as one line: a string, number or boolean as itself, a
// list of strings joined; "" for anything else.
func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		var parts []string
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return ""
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// proseBlock is text the agent wrote for another, as a muted paragraph.
func proseBlock(s string) ggui.Widget {
	if strings.TrimSpace(s) == "" {
		return widget.Box()
	}
	return scrolled(toolFrame(widget.Text(strings.TrimSpace(s)).Size(12.5).LineHeight(1.45).Selectable()).Pad(8, 10))
}

// fieldRows are the arguments of a tool not known here: short values a
// line each, long text and structures in blocks under their keys.
func fieldRows(in map[string]any) []ggui.Widget {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var short []string
	var rows []ggui.Widget
	for _, k := range keys {
		v := scalar(in[k])
		switch {
		case v != "" && !strings.Contains(v, "\n") && len(v) <= 160:
			short = append(short, k)
		case v != "":
			rows = append(rows, toolLabel(k), scrolled(toolBlock(v, false)))
		default:
			b, _ := json.MarshalIndent(in[k], "", "  ")
			rows = append(rows, toolLabel(k), scrolled(toolBlock(string(b), false)))
		}
	}
	if len(short) > 0 {
		rows = append([]ggui.Widget{fieldChips(in, short...)}, rows...)
	}
	return rows
}

// toolRowGlyph is the icon for a tool row, giving agy swarm/task coordination
// tools dedicated icons before falling back to canonical tool kinds.
func toolRowGlyph(msg *protocol.ChatMessage) string {
	switch msg.GetTool() {
	case "read_board":
		return "messages-square"
	case "swarm_wait", "schedule":
		return "clock"
	case "post_message", "send_message":
		return "send-horizontal"
	case "swarm", "invoke_subagent":
		return "users"
	case "manage_task":
		return "list"
	}
	return toolGlyph(msg.GetKind())
}

// isSwarmSyncTool reports whether msg is a low-signal swarm/task coordination
// or polling call that should coalesce with adjacent coordination calls into a
// single compact collapsible group row.
func isSwarmSyncTool(msg *protocol.ChatMessage) bool {
	if msg == nil || msg.GetRole() != "tool" {
		return false
	}
	switch msg.GetTool() {
	case "read_board", "swarm_wait", "manage_task", "schedule":
		return true
	case "swarm":
		if msg.GetOutput() {
			return false
		}
		var in map[string]any
		if json.Unmarshal([]byte(msg.GetText()), &in) == nil {
			action := strings.ToLower(agent.Str(in, "action", "Action"))
			return action == "status" || action == "models"
		}
	}
	return false
}

// sameRawTool reports whether every message in msgs invokes the same raw tool.
func sameRawTool(msgs []*protocol.ChatMessage) bool {
	if len(msgs) <= 1 {
		return true
	}
	first := toolGroupName(msgs[0])
	for _, msg := range msgs[1:] {
		if toolGroupName(msg) != first {
			return false
		}
	}
	return true
}

// swarmSyncSummary builds a concise ordered breakdown of a coalesced swarm sync
// run, e.g. "Read board ×6 · Wait for board ×4 · Manage task · Schedule".
func swarmSyncSummary(msgs []*protocol.ChatMessage) string {
	var order []string
	counts := map[string]int{}
	for _, msg := range msgs {
		label, _ := toolDisplay(msg)
		if counts[label] == 0 {
			order = append(order, label)
		}
		counts[label]++
	}
	parts := make([]string, 0, len(order))
	for _, label := range order {
		if n := counts[label]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", label, n))
		} else {
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, " · ")
}

// toolDisplay returns the concise display label and one-line summary detail for
// a tool row. For agy swarm/task coordination tools it derives readable labels
// and summaries directly from the JSON arguments; other tools keep toolParts.
func toolDisplay(msg *protocol.ChatMessage) (label, detail string) {
	name, fallbackDetail, args, _, _ := toolParts(msg)
	rawTool := msg.GetTool()
	var in map[string]any
	if !msg.GetOutput() && args != "" {
		_ = json.Unmarshal([]byte(args), &in)
	}
	switch rawTool {
	case "read_board":
		return T("Read board"), cmp.Or(readBoardDetail(in), fallbackDetail)
	case "swarm_wait":
		return T("Wait for board"), cmp.Or(swarmWaitDetail(in), fallbackDetail)
	case "post_message":
		d := agent.OneLine(agent.Str(in, "text", "message", "content"), 100)
		return T("Post to board"), cmp.Or(d, fallbackDetail)
	case "swarm":
		return T("Swarm"), cmp.Or(swarmDetail(in), fallbackDetail)
	case "manage_task":
		return T("Manage task"), cmp.Or(manageTaskDetail(in), fallbackDetail)
	case "schedule":
		return T("Schedule"), cmp.Or(scheduleDetail(in), fallbackDetail)
	case "invoke_subagent":
		return T("Subagent"), cmp.Or(subagentToolDetail(in), fallbackDetail)
	case "send_message":
		return T("Send message"), cmp.Or(sendMessageDetail(in), fallbackDetail)
	}
	return name, fallbackDetail
}

func readBoardDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	if status, _ := in["status"].(bool); status {
		return T("roster status")
	}
	if seqs := seqList(in["seqs"]); seqs != "" {
		return seqs
	}
	if since, ok := intField(in, "since"); ok && since > 0 {
		return Tf("since #%d", since)
	}
	if limit, ok := intField(in, "limit"); ok && limit > 0 {
		return Tf("latest %d", limit)
	}
	return T("latest messages")
}

func swarmWaitDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	mentionsOnly := true
	if v, ok := in["mentions_only"].(bool); ok {
		mentionsOnly = v
	}
	since, hasSince := intField(in, "since")
	switch {
	case hasSince && since > 0 && !mentionsOnly:
		return Tf("since #%d · all messages", since)
	case hasSince && since > 0:
		return Tf("since #%d · mentions", since)
	case !mentionsOnly:
		return T("all messages")
	default:
		return T("mentions")
	}
}

func swarmDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	action := agent.Str(in, "action", "Action")
	name := agent.Str(in, "name", "Name")
	model := agent.Str(in, "model", "Model")
	title := agent.Str(in, "title", "Title")
	brief := agent.OneLine(agent.Str(in, "brief", "Brief"), 80)
	switch strings.ToLower(action) {
	case "add":
		head := "add"
		if name != "" {
			head += " " + name
		}
		if n, ok := intField(in, "n"); ok && n > 1 {
			head += fmt.Sprintf(" ×%d", n)
		} else if tasks := agent.List(in, "tasks"); len(tasks) > 0 {
			head += " (" + Tf("%d tasks", len(tasks)) + ")"
		}
		var parts []string
		parts = append(parts, head)
		if model != "" {
			parts = append(parts, model)
		}
		if brief != "" {
			parts = append(parts, brief)
		}
		return strings.Join(parts, " · ")
	case "kill":
		if name != "" {
			return "kill " + name
		}
		return "kill"
	case "model":
		switch {
		case name != "" && model != "":
			return fmt.Sprintf("model %s → %s", name, model)
		case model != "":
			return "model → " + model
		case name != "":
			return "model " + name
		default:
			return "model"
		}
	case "title":
		if title != "" {
			return agent.OneLine(title, 90)
		}
		return "title"
	case "status":
		return T("status")
	case "models":
		if all, _ := in["all"].(bool); all {
			return T("models (all)")
		}
		return T("models")
	default:
		return strings.TrimSpace(action + " " + name)
	}
}

func manageTaskDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	action := agent.Str(in, "Action", "action")
	taskID := agent.Str(in, "TaskId", "task_id", "taskId")
	input := agent.OneLine(agent.Str(in, "Input", "input"), 80)
	switch strings.ToLower(action) {
	case "list":
		return T("list tasks")
	case "kill":
		if taskID != "" {
			return "kill " + taskID
		}
		return "kill"
	case "status":
		if taskID != "" {
			return "status " + taskID
		}
		return T("status")
	case "send_input":
		switch {
		case taskID != "" && input != "":
			return fmt.Sprintf("%s: %s", taskID, input)
		case input != "":
			return input
		case taskID != "":
			return "send_input " + taskID
		}
	}
	return strings.TrimSpace(action + " " + taskID)
}

func scheduleDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	cron := agent.Str(in, "CronExpression", "cron_expression", "cron")
	secs, hasSecs := intField(in, "DurationSeconds")
	if !hasSecs {
		secs, hasSecs = intField(in, "duration_seconds")
	}
	prompt := agent.OneLine(agent.Str(in, "Prompt", "prompt"), 80)
	var when string
	switch {
	case cron != "":
		when = cron
	case hasSecs && secs > 0:
		when = formatShortDuration(secs)
	}
	switch {
	case when != "" && prompt != "":
		return when + " · " + prompt
	case when != "":
		return when
	default:
		return prompt
	}
}

func subagentToolDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	role := agent.Str(in, "agent", "Role", "TypeName", "role", "typeName")
	desc := agent.Str(in, "description", "toolSummary")
	prompt := agent.OneLine(agent.Str(in, "prompt", "Prompt"), 90)
	if role == "" || prompt == "" {
		if subs := agent.List(in, "Subagents"); len(subs) > 0 {
			if first, ok := subs[0].(map[string]any); ok {
				role = cmp.Or(role, agent.Str(first, "Role", "Name", "TypeName"))
				prompt = cmp.Or(prompt, agent.OneLine(agent.Str(first, "Prompt"), 90))
			}
		}
	}
	body := cmp.Or(desc, prompt)
	switch {
	case role != "" && body != "" && role != body:
		return role + " · " + body
	case body != "":
		return body
	default:
		return role
	}
}

func sendMessageDetail(in map[string]any) string {
	if in == nil {
		return ""
	}
	to := agent.Str(in, "to", "To", "target", "Target", "recipient", "Recipient", "conversation_id", "ConversationId")
	body := agent.OneLine(agent.Str(in, "text", "Text", "message", "Message", "content", "Content"), 90)
	switch {
	case to != "" && body != "":
		return fmt.Sprintf("→ %s: %s", to, body)
	case to != "":
		return "→ " + to
	default:
		return body
	}
}

// swarmToolInput formats the expanded input section for agy swarm/task tools.
func swarmToolInput(tool string, in map[string]any) []ggui.Widget {
	if in == nil {
		return nil
	}
	switch tool {
	case "read_board":
		var chips [][2]string
		if status, _ := in["status"].(bool); status {
			chips = append(chips, [2]string{T("Mode"), T("roster status")})
		}
		if seqs := seqList(in["seqs"]); seqs != "" {
			chips = append(chips, [2]string{T("Messages"), seqs})
		}
		if since, ok := intField(in, "since"); ok && since > 0 {
			chips = append(chips, [2]string{T("Since"), fmt.Sprintf("#%d", since)})
		}
		if limit, ok := intField(in, "limit"); ok && limit > 0 {
			chips = append(chips, [2]string{T("Limit"), strconv.Itoa(limit)})
		}
		if len(chips) == 0 {
			chips = append(chips, [2]string{T("Mode"), T("latest messages")})
		}
		return []ggui.Widget{labeledChips(chips)}
	case "swarm_wait":
		var chips [][2]string
		if since, ok := intField(in, "since"); ok && since > 0 {
			chips = append(chips, [2]string{T("Since"), fmt.Sprintf("#%d", since)})
		}
		mentionsOnly := true
		if v, ok := in["mentions_only"].(bool); ok {
			mentionsOnly = v
		}
		chips = append(chips, [2]string{T("Wake on"), pick(mentionsOnly, T("Mentions only"), T("All messages"))})
		return []ggui.Widget{labeledChips(chips)}
	case "post_message":
		if text := agent.Str(in, "text", "message", "content"); text != "" {
			return []ggui.Widget{proseBlock(text)}
		}
	case "swarm":
		var chips [][2]string
		if v := agent.Str(in, "action", "Action"); v != "" {
			chips = append(chips, [2]string{T("Action"), v})
		}
		if v := agent.Str(in, "name", "Name"); v != "" {
			chips = append(chips, [2]string{T("Target"), v})
		}
		if v := agent.Str(in, "title", "Title"); v != "" {
			chips = append(chips, [2]string{T("Swarm"), v})
		}
		if v := agent.Str(in, "model", "Model"); v != "" {
			chips = append(chips, [2]string{T("Model"), v})
		}
		if v := agent.Str(in, "workspace", "Workspace"); v != "" {
			chips = append(chips, [2]string{T("Workspace"), v})
		}
		var rows []ggui.Widget
		if len(chips) > 0 {
			rows = append(rows, labeledChips(chips))
		}
		if brief := agent.Str(in, "brief", "Brief"); brief != "" {
			rows = append(rows, proseBlock(brief))
		}
		return rows
	case "manage_task":
		var chips [][2]string
		if v := agent.Str(in, "Action", "action"); v != "" {
			chips = append(chips, [2]string{T("Action"), v})
		}
		if v := agent.Str(in, "TaskId", "task_id", "taskId"); v != "" {
			chips = append(chips, [2]string{T("Task"), v})
		}
		var rows []ggui.Widget
		if len(chips) > 0 {
			rows = append(rows, labeledChips(chips))
		}
		if input := agent.Str(in, "Input", "input"); input != "" {
			rows = append(rows, proseBlock(input))
		}
		return rows
	case "schedule":
		var chips [][2]string
		if v := agent.Str(in, "CronExpression", "cron_expression", "cron"); v != "" {
			chips = append(chips, [2]string{T("Cron"), v})
		}
		if secs, ok := intField(in, "DurationSeconds"); ok && secs > 0 {
			chips = append(chips, [2]string{T("Timer"), formatShortDuration(secs)})
		} else if secs, ok := intField(in, "duration_seconds"); ok && secs > 0 {
			chips = append(chips, [2]string{T("Timer"), formatShortDuration(secs)})
		}
		if v := agent.Str(in, "TimerCondition", "timer_condition"); v != "" {
			chips = append(chips, [2]string{T("Condition"), v})
		}
		var rows []ggui.Widget
		if len(chips) > 0 {
			rows = append(rows, labeledChips(chips))
		}
		if prompt := agent.Str(in, "Prompt", "prompt"); prompt != "" {
			rows = append(rows, proseBlock(prompt))
		}
		return rows
	case "invoke_subagent":
		var chips [][2]string
		if role := agent.Str(in, "Role", "TypeName", "agent"); role != "" {
			chips = append(chips, [2]string{T("Subagent"), role})
		}
		var rows []ggui.Widget
		if len(chips) > 0 {
			rows = append(rows, labeledChips(chips))
		}
		if prompt := agent.Str(in, "Prompt", "prompt"); prompt != "" {
			rows = append(rows, proseBlock(prompt))
		}
		return rows
	case "send_message":
		var rows []ggui.Widget
		if to := agent.Str(in, "to", "To", "target", "Target", "recipient", "Recipient", "conversation_id", "ConversationId"); to != "" {
			rows = append(rows, labeledChips([][2]string{{T("Recipient"), to}}))
		}
		if body := agent.Str(in, "text", "Text", "message", "Message", "content", "Content"); body != "" {
			rows = append(rows, proseBlock(body))
		}
		return rows
	}
	return nil
}

func labeledChips(pairs [][2]string) ggui.Widget {
	var rows []ggui.Widget
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		rows = append(rows, widget.Row(
			widget.Text(p[0]).Size(11).Color(skin.ExtraMuted).NoWrap(),
			widget.Flex(widget.Text(p[1]).Size(12).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis().Selectable(), 1),
		).Gap(6).Align(widget.AlignCenter))
	}
	if len(rows) == 0 {
		return widget.Box()
	}
	return widget.Column(rows...).Gap(4).Stretch()
}

func intField(in map[string]any, key string) (int, bool) {
	if in == nil {
		return 0, false
	}
	switch v := in[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

func seqList(v any) string {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	var parts []string
	for _, e := range list {
		if f, ok := e.(float64); ok {
			parts = append(parts, fmt.Sprintf("#%d", int(f)))
		}
	}
	return strings.Join(parts, ", ")
}

// toolQuestionRows renders every question a question tool asked, along with
// each question's options. Falls back to the legacy single-question fields
// when the canonical questions list is absent.
func toolQuestionRows(in map[string]any) []ggui.Widget {
	qs := agent.Questions(in)
	if len(qs) == 0 {
		rows := []ggui.Widget{proseBlock(agent.Str(in, "question"))}
		if options := agent.List(in, "options"); len(options) > 0 {
			rows = append(rows, fieldChips(map[string]any{"options": options}, "options"))
		}
		return rows
	}
	var rows []ggui.Widget
	for i, q := range qs {
		if len(qs) > 1 {
			title := fmt.Sprintf("%d/%d", i+1, len(qs))
			if h := strings.TrimSpace(q.GetHeader()); h != "" {
				title += " · " + h
			}
			rows = append(rows, toolLabel(title))
		} else if h := strings.TrimSpace(q.GetHeader()); h != "" {
			rows = append(rows, toolLabel(h))
		}
		rows = append(rows, proseBlock(q.GetQuestion()))
		if opts := q.GetOptions(); len(opts) > 0 {
			labels := make([]any, 0, len(opts))
			for _, o := range opts {
				if l := strings.TrimSpace(o.GetLabel()); l != "" {
					labels = append(labels, l)
				}
			}
			if len(labels) > 0 {
				rows = append(rows, fieldChips(map[string]any{"options": labels}, "options"))
			}
		}
	}
	return rows
}

func formatShortDuration(secs int) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		if rem := secs % 60; rem > 0 {
			return fmt.Sprintf("%dm%ds", secs/60, rem)
		}
		return fmt.Sprintf("%dm", secs/60)
	}
	h, rem := secs/3600, (secs%3600)/60
	if rem > 0 {
		return fmt.Sprintf("%dh%dm", h, rem)
	}
	return fmt.Sprintf("%dh", h)
}
