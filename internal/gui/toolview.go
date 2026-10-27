package gui

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// A tool call's fold shows what the call did, read from its arguments as
// each agent's tools write them: a command and what it printed, an edit
// as a diff, a file read or written as code, a plan as a checklist. A tool
// it does not know shows its arguments field by field, and what it cannot
// read as they are.

// maxToolLines caps the lines of code or diff a tool's fold shows.
const maxToolLines = 400

// maxToolHeight caps a fold's code, diff or output, which scrolls inside.
const maxToolHeight = 360

// toolKind is what a tool does, whatever its agent calls it.
type toolKind int

const (
	toolOther toolKind = iota
	toolShell
	toolPatch // Codex's apply_patch
	toolEdit
	toolMultiEdit
	toolWrite
	toolRead
	toolSearch
	toolTodo // Claude's TodoWrite
	toolPlan // Codex's update_plan
	toolFetch
	toolWebSearch
	toolTask
)

// toolKinds are the tools of Claude Code and Codex, by their names in
// lower case.
var toolKinds = map[string]toolKind{
	"bash": toolShell, "shell": toolShell, "exec_command": toolShell, "exec": toolShell,
	"local_shell": toolShell, "container.exec": toolShell, "write_stdin": toolShell,
	"apply_patch": toolPatch,
	"edit":        toolEdit, "str_replace_based_edit_tool": toolEdit, "str_replace": toolEdit,
	"multiedit": toolMultiEdit,
	"write":     toolWrite, "create": toolWrite, "notebookedit": toolWrite,
	"read": toolRead, "view": toolRead,
	"grep": toolSearch, "glob": toolSearch, "search": toolSearch, "ls": toolSearch, "list_dir": toolSearch,
	"todowrite":   toolTodo,
	"update_plan": toolPlan,
	"webfetch":    toolFetch, "fetch": toolFetch,
	"websearch": toolWebSearch, "web_search": toolWebSearch,
	"task": toolTask, "agent": toolTask,
}

func kindOf(name string) toolKind { return toolKinds[strings.ToLower(name)] }

// toolBody is a tool call's fold.
func toolBody(m *model, msg *protocol.ChatMessage) ggui.Widget {
	name, _, args, result, failed := toolParts(msg)
	kind := kindOf(name)
	if failed && result == "" {
		result = T("The tool failed without output.")
	}
	var in map[string]any // nil for arguments that are not a JSON object
	_ = json.Unmarshal([]byte(args), &in)
	var rows []ggui.Widget
	if !msg.GetOutput() {
		rows = toolInput(m, kind, args, in)
	}
	if result != "" {
		rows = append(rows, toolOutput(m, kind, in, result, failed))
	}
	if len(rows) == 0 {
		return ggui.Padding(muted(ggui.Text(T("No output")).Size(12)), 4, 6, 6, 6)
	}
	return ggui.Padding(ggui.Column(rows...).Gap(6).Stretch(), 4, 6, 6, 18)
}

// toolInput is what a call asked for, as its tool reads.
func toolInput(m *model, kind toolKind, args string, in map[string]any) []ggui.Widget {
	switch {
	case kind == toolPatch:
		// Codex writes the patch itself, or JSON holding it.
		return patchView(cmp.Or(str(in, "input", "patch"), args))
	case in == nil:
		if args == "" {
			return nil
		}
		return []ggui.Widget{toolLabel(T("Arguments")), toolBlock(args, false)}
	}
	path := str(in, "file_path", "path", "notebook_path")
	switch kind {
	case toolShell:
		cmd := shellCommand(in)
		if cmd == "" {
			break
		}
		var rows []ggui.Widget
		if d := str(in, "description"); d != "" {
			rows = append(rows, muted(ggui.Text(d).Size(12.5)))
		}
		return append(rows, commandBlock(cmd, str(in, "workdir", "cwd")))
	case toolEdit:
		return []ggui.Widget{pathLine(path, ""), editDiff(str(in, "old_string", "old_str"), str(in, "new_string", "new_str"))}
	case toolMultiEdit:
		edits := list(in, "edits")
		rows := []ggui.Widget{pathLine(path, Tf("%d edits", len(edits)))}
		for _, e := range edits {
			if e, ok := e.(map[string]any); ok {
				rows = append(rows, editDiff(str(e, "old_string"), str(e, "new_string")))
			}
		}
		return rows
	case toolWrite:
		content := str(in, "content", "file_text", "new_source")
		return []ggui.Widget{pathLine(path, Tf("%d lines", textLines(content))), codeView(m, path, content)}
	case toolRead:
		return []ggui.Widget{pathLine(path, readRange(in))}
	case toolSearch:
		return []ggui.Widget{fieldChips(in, "pattern", "query", "path", "glob", "type", "output_mode")}
	case toolTodo:
		return []ggui.Widget{checklist(list(in, "todos"), "content", "status")}
	case toolPlan:
		var rows []ggui.Widget
		if e := str(in, "explanation"); e != "" {
			rows = append(rows, ggui.Text(e).Size(12.5))
		}
		return append(rows, checklist(list(in, "plan"), "step", "status"))
	case toolFetch:
		return []ggui.Widget{fieldChips(in, "url"), proseBlock(str(in, "prompt"))}
	case toolWebSearch:
		return []ggui.Widget{fieldChips(in, "query", "allowed_domains", "blocked_domains")}
	case toolTask:
		var rows []ggui.Widget
		if d := str(in, "description"); d != "" {
			rows = append(rows, ggui.Text(d).Size(13).Weight(ggui.WeightMedium))
		}
		if a := str(in, "subagent_type"); a != "" {
			rows = append(rows, fieldChips(map[string]any{"agent": a}, "agent"))
		}
		return append(rows, proseBlock(str(in, "prompt")))
	}
	return fieldRows(in)
}

// toolOutput is what a call gave back, as its tool reads.
func toolOutput(m *model, kind toolKind, in map[string]any, result string, failed bool) ggui.Widget {
	label := pick(failed, T("Error"), T("Output"))
	if failed {
		return ggui.Column(toolLabel(label), scrolled(toolBlock(result, true))).Gap(4).Stretch()
	}
	switch kind {
	case toolShell:
		code, out := shellOutput(result)
		head := []ggui.Widget{toolLabel(label)}
		if code >= 0 {
			head = append(head, exitBadge(code))
		}
		block := ggui.Widget(muted(ggui.Text(T("No output")).Size(12)))
		if strings.TrimSpace(out) != "" {
			block = scrolled(toolBlock(out, code > 0))
		}
		return ggui.Column(ggui.Row(head...).Gap(8).Align(ggui.AlignCenter), block).Gap(4).Stretch()
	case toolRead:
		if text, first, ok := numberedLines(result); ok {
			return ggui.Column(toolLabel(Tf("From line %d", first)), codeView(m, str(in, "file_path", "path"), text)).Gap(4).Stretch()
		}
	}
	return ggui.Column(toolLabel(label), scrolled(toolBlock(stripReminders(result), false))).Gap(4).Stretch()
}

// toolGlyph is the icon a tool row shows for its kind: a slash command's
// output and a compaction's summary have their own.
func toolGlyph(name string) string {
	switch {
	case strings.HasPrefix(name, "/"):
		return "terminal"
	case name == "Summary":
		return "history"
	}
	switch kindOf(name) {
	case toolShell:
		return "square-terminal"
	case toolPatch, toolEdit, toolMultiEdit, toolWrite, toolRead:
		return "file-text"
	case toolSearch:
		return "radar"
	case toolFetch, toolWebSearch:
		return "network"
	case toolTask, toolTodo, toolPlan:
		return "list"
	}
	return "square-dot"
}

// str is the first of keys in holding a string, or "".
func str(in map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := in[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// list is in[key] as a list, or nil.
func list(in map[string]any, key string) []any {
	l, _ := in[key].([]any)
	return l
}

// shellCommand is the command a shell tool runs: Claude's command, Codex's
// cmd, or the script of an argv such as ["bash", "-lc", "…"].
func shellCommand(in map[string]any) string {
	if s := str(in, "command", "cmd"); s != "" {
		return s
	}
	argv := list(in, "command")
	if argv == nil {
		argv = list(in, "cmd")
	}
	var words []string
	for _, a := range argv {
		if s, ok := a.(string); ok {
			words = append(words, s)
		}
	}
	if len(words) == 3 && (words[1] == "-lc" || words[1] == "-c") {
		return words[2]
	}
	return strings.Join(words, " ")
}

// codexExit finds the exit code in the header Codex writes over a
// command's output.
var codexExit = regexp.MustCompile(`(?m)^(?:Process exited with code|Exit code:) (-?\d+)`)

// shellOutput is a command's output without the header Codex writes over
// it, and its exit code, -1 where it does not say.
func shellOutput(result string) (int, string) {
	code := -1
	head, out, ok := strings.Cut(result, "\nOutput:\n")
	if !ok {
		return code, result
	}
	if mm := codexExit.FindStringSubmatch(head); mm != nil {
		code, _ = strconv.Atoi(mm[1])
	}
	return code, out
}

// numberedLines is a file Claude's Read gave back, without the number it
// puts before each line ("   12→text"), and the first line's number. ok
// is false for output that is not numbered so. Lines past what a fold
// shows are left as they are.
func numberedLines(result string) (text string, first int, ok bool) {
	lines := strings.Split(strings.TrimRight(stripReminders(result), "\n"), "\n")
	for i, l := range lines[:min(len(lines), maxToolLines)] {
		num, rest, found := strings.Cut(l, "→")
		if !found {
			num, rest, found = strings.Cut(l, "\t")
		}
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if !found || err != nil {
			return "", 0, false
		}
		if i == 0 {
			first = n
		}
		lines[i] = rest
	}
	return strings.Join(lines, "\n"), first, true
}

// reminder is a note Claude Code adds to a tool's result for the model.
var reminder = regexp.MustCompile(`(?s)\s*<system-reminder>.*?</system-reminder>\s*`)

func stripReminders(s string) string { return reminder.ReplaceAllString(s, "\n") }

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
		icon(fileIconName(baseName(path))).Size(14).Color(tokMuted),
		ggui.Flex(ggui.Text(path).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis().Selectable(), 1),
	}
	if note != "" {
		row = append(row, muted(ggui.Text(note).Size(12).NoWrap()))
	}
	return ggui.Row(row...).Gap(6).Align(ggui.AlignCenter)
}

// commandBlock is a command as a terminal shows it, with where it runs.
func commandBlock(cmd, dir string) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 12}
	text := ggui.RichText(ggui.Span("$ ").Color(tokAccentBright), ggui.Span(strings.TrimRight(cmd, "\n"))).
		Style(mono).LineHeight(1.4).Selectable()
	rows := []ggui.Widget{text}
	if dir != "" {
		rows = append(rows, ggui.Text(dir).Style(mono).Color(tokExtraMuted).NoWrap().Ellipsis())
	}
	return toolFrame(ggui.Column(rows...).Gap(4).Stretch()).Pad(8, 10)
}

// exitBadge is a command's exit code, green for success.
func exitBadge(code int) ggui.Widget {
	c := pick(code == 0, tokDiffAdded, tokDotDown)
	return ggui.Box(ggui.Text(Tf("exit %d", code)).Size(11).Font(ggui.DefaultMonoFont()).Color(c).NoWrap()).
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
	rows := []ggui.Widget{ggui.Scroll(ggui.Padding(codeText(m, src, l, ok, 12), 8, 10)).Horizontal().Bar(tokSurface3)}
	if cut {
		rows = append(rows, ggui.Box(muted(ggui.Text(Tf("%d more lines", len(lines)-maxToolLines)).Size(11.5))).Pad(4, 10, 8, 10))
	}
	return scrolled(toolFrame(ggui.Column(rows...).Stretch()))
}

// scrolled caps w at maxToolHeight, scrolling what is over; shorter, it
// is as tall as it is.
func scrolled(w ggui.Widget) ggui.Widget {
	return &capHeight{content: w, scroll: ggui.Scroll(w).Bar(tokSurface3), max: maxToolHeight}
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
				rows = append(rows, ggui.Box(muted(ggui.Text("⋯").Style(mono))).Pad(0, 10).Fill(tokSurface2))
			}
			continue
		case "+", "-":
		default:
			l = &protocol.DiffLine{Op: " ", Text: l.GetText()}
		}
		row := ggui.Box(diffText(l, words[i], diffMode{Wrap: true}, mono, l.GetOp()+" ")).Pad(1, 10)
		switch l.GetOp() {
		case "+":
			row = row.Fill(tokDiffAddedBg)
		case "-":
			row = row.Fill(tokDiffRemovedBg)
		}
		rows = append(rows, row)
	}
	if len(shown) < len(lines) {
		rows = append(rows, ggui.Box(muted(ggui.Text(T("The rest of the diff is not shown.")).Size(11.5))).Pad(4, 10))
	}
	if len(rows) == 0 {
		rows = append(rows, ggui.Box(muted(ggui.Text(T("No changes")).Size(12))).Pad(6, 10))
	}
	return scrolled(toolFrame(ggui.Column(rows...).Stretch()).Pad(4, 0))
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
	for _, l := range strings.Split(patch, "\n") {
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

// checklist is a plan's steps, each marked done, under way or not yet.
func checklist(items []any, textKey, statusKey string) ggui.Widget {
	var rows []ggui.Widget
	for _, it := range items {
		step, ok := it.(map[string]any)
		if !ok {
			continue
		}
		status := str(step, statusKey)
		glyph := icon("circle").Size(14).Color(tokExtraMuted)
		text := ggui.Text(str(step, textKey)).Size(12.5)
		switch status {
		case "completed", "done":
			glyph = icon("circle-check").Size(14).Color(tokDiffAdded)
			text = text.Color(tokMuted)
		case "in_progress":
			glyph = icon("circle-dot").Size(14).Color(tokAccentBright)
			text = text.Weight(ggui.WeightMedium)
		}
		rows = append(rows, ggui.Row(glyph, ggui.Expanded(text)).Gap(8).Align(ggui.AlignStart))
	}
	if len(rows) == 0 {
		return muted(ggui.Text(T("No steps")).Size(12))
	}
	return ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).Pad(2, 2)
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
		chips = append(chips, ggui.Row(
			ggui.Text(k).Size(11).Color(tokExtraMuted).NoWrap(),
			ggui.Flex(ggui.Text(v).Size(12).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis().Selectable(), 1),
		).Gap(6).Align(ggui.AlignCenter))
	}
	if len(chips) == 0 {
		return ggui.Box()
	}
	return ggui.Column(chips...).Gap(4).Stretch()
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
		return ggui.Box()
	}
	return scrolled(toolFrame(ggui.Text(strings.TrimSpace(s)).Size(12.5).LineHeight(1.45).Selectable()).Pad(8, 10))
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
