package agy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"

	"github.com/ironpark/mako/internal/detect"
)

var (
	spinnerLineRe       = regexp.MustCompile(`^\s*[\x{2801}-\x{28FF}]+(?:\s|$)`)
	promptFooterRe      = regexp.MustCompile(`^\s*\? for shortcuts\b`)
	numberedListRe      = regexp.MustCompile(`^(\d+[.)]\s+)(.*)$`)
	expandHintRe        = regexp.MustCompile(`^(?:\(ctrl\+o to (?:expand|collapse)\)|(?:…|\.\.\.)\s*(?:\+\d+\s+(?:more\s+)?lines?|\d+\s+more\s+actions?\b.*))$`)
	markdownLinkRe      = regexp.MustCompile(`!?\[([^\]]*)\]\([^)\s]+(?:\s+"[^"]*")?\)`)
	fileLinkRe          = regexp.MustCompile(`\[[^\]]*\]\(file://([^)#\s]+)(?:#[^)\s]*)?\)`)
	tableSeparatorRe    = regexp.MustCompile(`^\|?(?:\s*:?-{3,}:?\s*\|)+\s*:?-{3,}:?\s*\|?$`)
	rawHTMLTagRe        = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9_.:-]*(?:\s[^>\n]*)?/?>`)
	htmlBreakRe         = regexp.MustCompile(`(?i)<br\s*/?>`)
	dockOverflowRe      = regexp.MustCompile(`^\s*[↓↑]\s+\d+\s+more\s*$`)
	thoughtHeaderRe     = regexp.MustCompile(`^\s*(?:│\s*)?[▸▾○●]\s+Thought\s+for\b`)
	taskDockRe          = regexp.MustCompile(`^\s*(?:[●○✓✔✗✘◆]|[\x{2801}-\x{28FF}]+)\s+\[(\d{2}:\d{2}:\d{2})\]\s+(.+?)\s+(running|done|completed|failed|exited|stopped|killed)\s*$`)
	stepChecklistItemRe = regexp.MustCompile(`^\s*(?:⎿\s+)?[✓✔●○✗✘]\s+\S`)
	submitAllFooterRe   = regexp.MustCompile(`(?i)^\s*(?:.*·\s*)?enter\s+submit\s+all(?:\s*·|\s*$)`)
)

// ExtractLive extracts the currently streaming assistant response text from an
// agy PTY screen snapshot while the session is actively working.
//
// It returns "" when the screen is idle, showing a dialog or slash menu, has no
// active working indicator, shows only chrome/spinners/tool output, or matches
// lastLogged (the assistant text already written to transcript_full.jsonl).
func (*Agent) ExtractLive(screen, lastLogged string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return ""
	}
	lines := rawLines[:end]

	// Gate 1: Reject idle, overlay, dialog, permission, trust, and slash-menu screens.
	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.State == detect.Idle || detected.Hold {
		return ""
	}
	if hasIdleOrDialogFooter(lines) {
		return ""
	}
	// Require positive working evidence near the bottom (esc to cancel footer
	// or a braille spinner) so splash screens or non-agy output never leak.
	if detected.State != detect.Working && !hasWorkingIndicator(lines) {
		return ""
	}

	cleaned := extractBodyText(lines, false)
	if cleaned == "" {
		return ""
	}

	// Suppress text already persisted to transcript_full.jsonl.
	if lastLogged != "" && matchesLogged(cleaned, lastLogged) {
		return ""
	}
	return cleaned
}

// ExtractBaseline extracts the last completed assistant text currently shown
// above the prompt composer (including idle screens), excluding dialogs.
// screenLivePoller snapshots this while idle so a newly submitted turn cannot
// briefly re-emit the previous turn's response before agy paints the new turn.
func (*Agent) ExtractBaseline(screen string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return ""
	}
	lines := rawLines[:end]

	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.Hold || hasDialogFooter(lines) {
		return ""
	}
	return extractBodyText(lines, true)
}

// ExtractTurn counts the rendered user prompt turns in the conversation body
// above the composer box and returns the latest user prompt text.
func (*Agent) ExtractTurn(screen string) (int, string) {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return 0, ""
	}
	lines := rawLines[:end]
	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.Hold || hasDialogFooter(lines) {
		return 0, ""
	}
	bodyEnd, cols := stripBottomChrome(lines, true)
	if bodyEnd == 0 {
		return 0, ""
	}
	body := lines[:bodyEnd]

	inFence := false
	turns := 0
	lastIdx := -1
	for i, line := range body {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isUserPromptLine(line) {
			// Skip contiguous multi-line "> " lines belonging to the same prompt.
			if i > 0 && isUserPromptLine(body[i-1]) {
				continue
			}
			turns++
			lastIdx = i
		}
	}
	if lastIdx < 0 {
		return 0, ""
	}

	var parts []string
	parts = append(parts, strings.TrimSpace(body[lastIdx][2:]))
	j := lastIdx + 1
	for j < len(body) && strings.HasPrefix(body[j], "> ") {
		parts = append(parts, strings.TrimSpace(body[j][2:]))
		j++
	}
	wrapMin := 50
	if cols > 0 {
		wrapMin = max(40, cols-20)
	}
	prevLine := body[j-1]
	for j < len(body) {
		curr := body[j]
		if strings.TrimSpace(curr) == "" ||
			isToolHeader(curr) || isThoughtHeader(curr) || isToolDetail(curr) || isSpinnerLine(curr) ||
			isHorizontalRule(curr) || isTitledDivider(curr) || isBoxStart(curr) || isAnnouncementLine(curr) || isInterruptedBanner(curr) {
			break
		}
		prevLen := displayWidth(strings.TrimRight(prevLine, " \t"))
		if prevLen >= wrapMin && strings.HasPrefix(curr, "  ") && !startsMarkdownBlock(strings.TrimSpace(curr)) {
			parts = append(parts, strings.TrimSpace(curr))
			prevLine = curr
			j++
			continue
		}
		break
	}
	return turns, strings.TrimSpace(strings.Join(parts, " "))
}

// ExtractStatus extracts agy's live activity label from its bottom braille
// spinner line (e.g. "Reading file…", "Running command…", "Generating…",
// "Thinking…") while working, plus any active Swarm/Agent status rows shown
// in the bottom chrome (even while the coordinator prompt is idle). Returns ""
// when showing a dialog/slash menu or when neither spinner nor active
// Swarm/Agent work is visible.
func (*Agent) ExtractStatus(screen string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return ""
	}
	lines := rawLines[:end]

	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.Hold || detected.ID == "slash_menu" || hasDialogFooter(lines) {
		return ""
	}

	activity := extractLiveActivity(lines)
	var spinner string
	if detected.State != detect.Idle && !hasIdleOrDialogFooter(lines) && (detected.State == detect.Working || hasWorkingIndicator(lines)) {
		// Strip footer (and stacked statusLine) + prompt composer box, then scan
		// upward across the bottom chrome region for the active braille spinner.
		spinnerEnd := end
		footerFloor := max(0, spinnerEnd-16)
		for i := spinnerEnd - 1; i >= footerFloor; i-- {
			if isEscCancelLine(lines[i]) {
				spinnerEnd = i
				break
			}
		}
		spinnerEnd = trimTrailingBlanks(lines[:spinnerEnd])
		if spinnerEnd > 0 {
			scanFloor := max(0, spinnerEnd-14)
			for i := spinnerEnd - 1; i >= scanFloor; i-- {
				if !isSpinnerLine(lines[i]) || isLiveActivityHeader(lines[i]) || isTaskDockLine(lines[i]) {
					continue
				}
				if label := normalizeStatusLabel(lines[i]); label != "" {
					spinner = label
					break
				}
			}
		}
	}

	if spinner == "" {
		return strings.Join(activity, "\n")
	}
	if len(activity) == 0 {
		return spinner
	}
	return spinner + "\n" + strings.Join(activity, "\n")
}

// ExtractActivity extracts only active background Swarm/Agent/Task status rows
// from agy's bottom chrome (never foreground braille spinners), stripped of raw
// keybinding hints like "alt+s board · alt+x dismiss". Returns "" on dialogs,
// overlays, or when no background work is active.
func (*Agent) ExtractActivity(screen string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return ""
	}
	lines := rawLines[:end]

	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.Hold || detected.ID == "slash_menu" || hasDialogFooter(lines) {
		return ""
	}
	return strings.Join(extractLiveActivity(lines), "\n")
}

// extractLiveActivity scans agy's bottom chrome (below the composer box and
// contiguous chrome immediately above it) for active Swarm(...) and Agent(...)
// rows, stripping keyboard shortcut hints and ignoring finished ("Done") rows.
func extractLiveActivity(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	boxTop, boxBottom := findBottomComposerBox(lines)
	var chrome []string
	if boxTop >= 0 {
		// Collect contiguous chrome lines immediately above the composer box.
		aboveStart := boxTop
		limit := max(0, boxTop-16)
		for aboveStart > limit && (isBottomChromeLine(lines[aboveStart-1]) || isActivityContinuationCandidate(lines, aboveStart-1)) {
			aboveStart--
		}
		chrome = append(chrome, lines[aboveStart:boxTop]...)
		if boxBottom+1 < len(lines) {
			chrome = append(chrome, lines[boxBottom+1:]...)
		}
	} else {
		start := max(0, len(lines)-16)
		chrome = lines[start:]
	}

	type rawEntry struct {
		kind, name, detail string
	}
	var entries []rawEntry
	for i := 0; i < len(chrome); i++ {
		if name, started, status, ok := parseTaskDockLine(chrome[i]); ok {
			detail := status
			if started != "" {
				if detail != "" {
					detail += " · " + started
				} else {
					detail = started
				}
			}
			entries = append(entries, rawEntry{kind: "Task", name: name, detail: detail})
			continue
		}
		kind, name, rest, ok := parseLiveActivityHeader(chrome[i])
		if !ok {
			continue
		}
		if rest == "" && i+1 < len(chrome) && isStandaloneActivityContinuation(chrome[i+1]) {
			rest = strings.TrimSpace(chrome[i+1])
			i++
		}
		detail := cleanActivityDetail(rest)
		entries = append(entries, rawEntry{kind: kind, name: name, detail: detail})
	}
	if len(entries) == 0 {
		return nil
	}

	hasActiveAgent := false
	for _, e := range entries {
		if e.kind == "Agent" && !isInactiveActivityDetail(e.detail) {
			hasActiveAgent = true
			break
		}
	}

	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		if isInactiveActivityDetail(e.detail) {
			continue
		}
		// A Swarm line with no detail and no active workers is ambiguous; skip.
		if e.kind == "Swarm" && e.detail == "" && !hasActiveAgent {
			continue
		}
		line := e.kind + "(" + e.name + ")"
		if e.detail != "" {
			line += " · " + e.detail
		}
		if utf8.RuneCountInString(line) > 200 {
			runes := []rune(line)
			line = strings.TrimSpace(string(runes[:200])) + "…"
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

// findBottomComposerBox locates the bottommost prompt composer box (bounded by
// equal-width horizontal rules around a ">" line) within the bottom 24 lines.
func findBottomComposerBox(lines []string) (top, bottom int) {
	floor := max(0, len(lines)-24)
	for b := len(lines) - 1; b >= floor; b-- {
		if !isHorizontalRule(lines[b]) {
			continue
		}
		limit := max(0, b-12)
		for t := b - 1; t >= limit; t-- {
			if isHorizontalRule(lines[t]) {
				if rulesMatchWidth(lines[t], lines[b]) && looksLikeComposerBody(lines[t+1 : b]) {
					return t, b
				}
				break
			}
			if isToolHeader(lines[t]) {
				break
			}
		}
	}
	return -1, -1
}

func rulesMatchWidth(a, b string) bool {
	wa := utf8.RuneCountInString(strings.TrimSpace(a))
	wb := utf8.RuneCountInString(strings.TrimSpace(b))
	diff := wa - wb
	if diff < 0 {
		diff = -diff
	}
	return diff <= 2
}

// isActivityContinuationCandidate reports whether lines[i] is a wrapped
// continuation line directly beneath a Swarm/Agent header line.
func isActivityContinuationCandidate(lines []string, i int) bool {
	if i <= 0 || !isStandaloneActivityContinuation(lines[i]) {
		return false
	}
	_, _, rest, ok := parseLiveActivityHeader(lines[i-1])
	return ok && rest == ""
}

func isStandaloneActivityContinuation(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || isHorizontalRule(t) || isComposerLine(t) || isSpinnerLine(t) ||
		isEscCancelLine(t) || promptFooterRe.MatchString(t) || isTipLine(t) ||
		isLiveActivityHeader(t) || startsWithToolGlyph(t) || isToolDetail(t) {
		return false
	}
	lower := strings.ToLower(t)
	return strings.Contains(t, "·") ||
		strings.Contains(lower, "alt+s board") ||
		strings.Contains(lower, "alt+x dismiss")
}

func isLiveActivityHeader(line string) bool {
	_, _, _, ok := parseLiveActivityHeader(line)
	return ok
}

// parseLiveActivityHeader parses a Swarm(...) or Agent(...) chrome line,
// handling nested parentheses such as "Agent(worker: Swarm Worker (ui-schema))".
func parseLiveActivityHeader(line string) (kind, name, rest string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" {
		return "", "", "", false
	}
	t = strings.TrimLeft(t, "─━═ \t")
	hasMarker := false
	switch {
	case startsWithToolGlyph(t):
		// Strip one leading tool glyph + space(s).
		_, sz := utf8.DecodeRuneInString(t)
		t = strings.TrimSpace(t[sz:])
		hasMarker = true
	case isSpinnerLine(t):
		t = strings.TrimLeftFunc(t, func(r rune) bool {
			return (r >= 0x2801 && r <= 0x28FF) || unicode.IsSpace(r)
		})
		hasMarker = true
	}
	switch {
	case strings.HasPrefix(t, "Swarm("):
		kind = "Swarm"
		t = t[len("Swarm("):]
	case hasMarker && strings.HasPrefix(t, "Agent("):
		kind = "Agent"
		t = t[len("Agent("):]
	default:
		return "", "", "", false
	}
	// Find matching closing ')' accounting for nested parens.
	depth := 1
	closeIdx := -1
	for i, r := range t {
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
		return "", "", "", false
	}
	name = strings.TrimSpace(t[:closeIdx])
	if name == "" {
		return "", "", "", false
	}
	rest = strings.TrimSpace(t[closeIdx+1:])
	return kind, name, rest, true
}

func cleanActivityDetail(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	var kept []string
	for part := range strings.SplitSeq(rest, "·") {
		seg := strings.Join(strings.Fields(strings.TrimSpace(part)), " ")
		if seg == "" || isShortcutChromeSegment(seg) {
			continue
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, " · ")
}

func isShortcutChromeSegment(seg string) bool {
	lower := strings.ToLower(strings.TrimSpace(seg))
	if lower == "" {
		return true
	}
	for _, prefix := range []string{"alt+", "ctrl+", "shift+", "esc to ", "? for shortcuts"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return strings.Contains(lower, "alt+s board") || strings.Contains(lower, "alt+x dismiss")
}

func isInactiveActivityDetail(detail string) bool {
	first := strings.ToLower(strings.TrimSpace(detail))
	if first == "" {
		return false
	}
	if i := strings.IndexAny(first, " ·,:-"); i >= 0 {
		first = first[:i]
	}
	switch first {
	case "done", "completed", "finished", "dismissed", "stopped", "killed", "cancelled", "canceled", "failed", "exited":
		return true
	}
	return false
}

func parseTaskDockLine(line string) (cmd, started, status string, ok bool) {
	m := taskDockRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", false
	}
	started = m[1]
	cmd = strings.Join(strings.Fields(strings.TrimSpace(m[2])), " ")
	status = strings.ToLower(strings.TrimSpace(m[3]))
	if cmd == "" {
		return "", "", "", false
	}
	return cmd, started, status, true
}

func isTaskDockLine(line string) bool {
	_, _, _, ok := parseTaskDockLine(line)
	return ok
}

func isDockOverflowLine(line string) bool {
	return dockOverflowRe.MatchString(line)
}

func normalizeStatusLabel(line string) string {
	t := strings.TrimSpace(line)
	// Strip leading braille spinner rune(s) and separating whitespace.
	t = strings.TrimLeftFunc(t, func(r rune) bool {
		return (r >= 0x2801 && r <= 0x28FF) || unicode.IsSpace(r)
	})
	// If a partial TUI redraw left two spinner statuses concatenated on one
	// logical line (e.g. "1/3 steps · Foo...⠋  1/3 steps · Bar..."), keep the
	// latest segment after the rightmost braille spinner.
	if lastSpinner := strings.LastIndexFunc(t, func(r rune) bool {
		return r >= 0x2801 && r <= 0x28FF
	}); lastSpinner >= 0 {
		_, sz := utf8.DecodeRuneInString(t[lastSpinner:])
		if tail := strings.TrimSpace(t[lastSpinner+sz:]); tail != "" {
			t = tail
		}
	}
	// Strip optional leading completion mark ("✓ 1/3 steps · ...").
	t = strings.TrimSpace(strings.TrimPrefix(t, "✓"))
	if t == "" {
		return ""
	}
	if strings.HasSuffix(t, "...") {
		t = strings.TrimSuffix(t, "...") + "…"
	}
	if utf8.RuneCountInString(t) > 120 {
		runes := []rune(t)
		t = strings.TrimSpace(string(runes[:120])) + "…"
	}
	return t
}

func extractBodyText(lines []string, includeIdleFooter bool) string {
	// Step 1: Strip bottom chrome (footer, stacked custom statusLine, prompt
	// composer box, and working spinner) and infer terminal width from the
	// prompt box rule.
	bodyEnd, cols := stripBottomChrome(lines, includeIdleFooter)
	if bodyEnd == 0 {
		return ""
	}
	body := lines[:bodyEnd]

	// Step 2: Anchor below the latest user prompt turn marker.
	turnStart, ok := findCurrentTurnStart(body, cols)
	if !ok || turnStart >= len(body) {
		return ""
	}
	turn := body[turnStart:]

	// Step 3: Skip tool call blocks, tool outputs, and boxes in the current turn
	// so only newly streamed assistant prose after the last tool step remains.
	afterTool := skipThroughLastTool(turn)
	if afterTool >= len(turn) {
		return ""
	}
	cand := trimEdgeChrome(turn[afterTool:])
	if len(cand) == 0 {
		return ""
	}

	// Step 4: Dedent common margin and rejoin software-wrapped prose lines
	// while keeping paragraphs, lists, headings, blockquotes, and code fences.
	return formatExtractedLines(cand, cols)
}

func trimTrailingBlanks(lines []string) int {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}

// hasIdleOrDialogFooter inspects the bottom 10 lines directly so a stacked
// custom statusLine below agy's default footer cannot hide an idle or dialog
// prompt from detect.Bottom(2).
func hasIdleOrDialogFooter(lines []string) bool {
	start := max(0, len(lines)-10)
	for i := start; i < len(lines); i++ {
		if promptFooterRe.MatchString(lines[i]) {
			return true
		}
	}
	return hasDialogFooter(lines)
}

func hasOverlayFooter(lines []string) bool {
	start := max(0, len(lines)-10)
	for i := start; i < len(lines); i++ {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "keyboard:") &&
			(strings.Contains(lower, "esc cancel") ||
				strings.Contains(lower, "esc go back") ||
				strings.Contains(lower, "esc close") ||
				strings.Contains(lower, "esc back") ||
				strings.Contains(lower, "esc exit") ||
				strings.Contains(lower, "tab switch tab")) {
			return true
		}
	}
	return false
}

func hasDialogFooter(lines []string) bool {
	if hasOverlayFooter(lines) {
		return true
	}
	start := max(0, len(lines)-10)
	for i := start; i < len(lines); i++ {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "navigate") &&
			(strings.Contains(lower, "tab amend") ||
				strings.Contains(lower, "esc skip") ||
				strings.Contains(lower, "enter confirm") ||
				strings.Contains(lower, "tab complete")) {
			return true
		}
		if strings.Contains(lower, "esc skip") &&
			(strings.Contains(lower, "enter select") ||
				strings.Contains(lower, "space toggle") ||
				strings.Contains(lower, "write-in")) {
			return true
		}
		if submitAllFooterRe.MatchString(lines[i]) {
			return true
		}
		if strings.Contains(lower, "requesting permission for:") ||
			strings.Contains(lower, "do you trust the contents of this project?") {
			return true
		}
	}
	return false
}

func hasWorkingIndicator(lines []string) bool {
	start := max(0, len(lines)-15)
	for i := start; i < len(lines); i++ {
		if isEscCancelLine(lines[i]) || isSpinnerLine(lines[i]) {
			return true
		}
	}
	return false
}

func isEscCancelLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if strings.HasPrefix(lower, "esc to cancel") {
		return true
	}
	// Handle "─── esc to cancel" variant matched by esc_cancel_footer_working.
	trimmed := strings.TrimLeft(lower, "─━═ \t")
	return len(trimmed) < len(lower) && strings.HasPrefix(trimmed, "esc to cancel")
}

func isSpinnerLine(line string) bool {
	return spinnerLineRe.MatchString(line)
}

func isHorizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	count := 0
	for _, r := range t {
		if r == '─' || r == '━' || r == '═' {
			count++
		} else {
			return false
		}
	}
	return count >= 4
}

func isTurnDivider(line string) bool {
	t := strings.TrimSpace(line)
	if !isHorizontalRule(t) {
		return false
	}
	return utf8.RuneCountInString(t) >= 10
}

// stripBottomChrome removes the footer (plus any stacked custom statusLine
// beneath it), the composer prompt box, and trailing spinners/rules.
// It returns the exclusive end index of the conversation body and the inferred
// terminal column width (0 if unknown).
func stripBottomChrome(lines []string, includeIdleFooter bool) (end int, cols int) {
	end = len(lines)

	// 1. Strip default footer and any custom statusLine below it.
	footerFloor := max(0, end-16)
	for i := end - 1; i >= footerFloor; i-- {
		if isEscCancelLine(lines[i]) || (includeIdleFooter && promptFooterRe.MatchString(lines[i])) {
			end = i
			break
		}
	}
	end = trimTrailingBlanks(lines[:end])

	// 2. Strip the bottommost prompt composer box AND any below-composer dock
	//    (background tasks, Swarm/Agent workers, overflow rows, or partial TUI
	//    redraws) in one step so below-composer chrome can never leak as prose.
	if boxTop, boxBottom := findBottomComposerBox(lines[:end]); boxTop >= 0 {
		if w := utf8.RuneCountInString(strings.TrimSpace(lines[boxTop])); w >= 20 && w > cols {
			cols = w
		}
		if w := utf8.RuneCountInString(strings.TrimSpace(lines[boxBottom])); w >= 20 && w > cols {
			cols = w
		}
		end = boxTop
	} else if end >= 2 && isComposerLine(lines[end-1]) && isHorizontalRule(lines[end-2]) {
		if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-2])); w >= 20 && w > cols {
			cols = w
		}
		end -= 2
	}

	// 3. Iteratively strip stacked chrome immediately above the composer box:
	//    live step checklists, Swarm/Agent status bars, artifact Tip banners,
	//    working spinners, and separator rules.
	for end > 0 {
		prev := end
		if stripped := stripTrailingStepChecklist(lines, end); stripped < end {
			end = stripped
		}
		for end > 0 && (isBottomChromeLine(lines[end-1]) || isActivityContinuationCandidate(lines, end-1)) {
			if isHorizontalRule(lines[end-1]) {
				if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-1])); w >= 20 && w > cols {
					cols = w
				}
			}
			end--
		}
		if end == prev {
			break
		}
	}
	return end, cols
}

func isTipLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	t = strings.TrimLeft(t, "└╰⎿├│┃ \t")
	return strings.HasPrefix(t, "Tip:") ||
		strings.HasPrefix(t, "To disable tips, toggle Show Tips in /settings")
}

func isTitledDivider(line string) bool {
	t := strings.TrimSpace(line)
	if utf8.RuneCountInString(t) < 10 {
		return false
	}
	left := 0
	for _, r := range t {
		if r == '─' || r == '━' || r == '═' {
			left++
		} else {
			break
		}
	}
	if left < 3 {
		return false
	}
	right := 0
	for i := len(t); i > 0; {
		r, sz := utf8.DecodeLastRuneInString(t[:i])
		if r == '─' || r == '━' || r == '═' {
			right++
			i -= sz
		} else {
			break
		}
	}
	if right < 3 {
		return false
	}
	// Require inner title text (letters/digits) and reject table grid lines ("───┼───").
	hasLabel := false
	for _, r := range t {
		switch r {
		case '┼', '╪', '╂', '┿', '┬', '┴', '│', '┃':
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasLabel = true
		}
	}
	return hasLabel
}

func isSwarmChromeLine(line string) bool {
	if isLiveActivityHeader(line) {
		return true
	}
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	rest := strings.TrimLeft(t, "─━═ \t")
	if strings.Contains(rest, "Swarm(") && (startsWithToolGlyph(rest) || isSpinnerLine(rest) || strings.HasPrefix(rest, "Swarm(")) {
		return true
	}
	lower := strings.ToLower(t)
	return strings.Contains(lower, "alt+s board") || strings.Contains(lower, "alt+x dismiss")
}

func isBottomChromeLine(line string) bool {
	return strings.TrimSpace(line) == "" ||
		isSpinnerLine(line) ||
		isHorizontalRule(line) ||
		isTitledDivider(line) ||
		isTipLine(line) ||
		isSwarmChromeLine(line) ||
		isTaskDockLine(line) ||
		isDockOverflowLine(line) ||
		isExpandHint(line)
}

// stripTrailingStepChecklist strips an agy step checklist block immediately
// beneath a working spinner (e.g. "⣟  1/3 steps · ...\n  ⎿  ✓ ...\n     ● ...\n     ○ ...").
func stripTrailingStepChecklist(lines []string, end int) int {
	j := end
	for j > 0 && strings.TrimSpace(lines[j-1]) == "" {
		j--
	}
	items := 0
	hasDetail := false
	for j > 0 && items < 16 {
		l := lines[j-1]
		if !stepChecklistItemRe.MatchString(l) || leadingSpaces(l) < 2 {
			break
		}
		if isToolDetail(l) {
			hasDetail = true
		}
		items++
		j--
	}
	if items > 0 && hasDetail && j > 0 && isSpinnerLine(lines[j-1]) {
		return j - 1
	}
	return end
}

func looksLikeComposerBody(lines []string) bool {
	if len(lines) == 0 {
		return true
	}
	for _, l := range lines {
		if isComposerLine(l) {
			return true
		}
	}
	// Allow blank composer box.
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

func isComposerLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == ">" || strings.HasPrefix(t, "> ") || t == "❯" || strings.HasPrefix(t, "❯ ")
}

func isUserPromptLine(line string) bool {
	return strings.HasPrefix(line, "> ") && strings.TrimSpace(line[2:]) != ""
}

func isFenceLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

func hasSplashBanner(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, "Antigravity CLI") || strings.Contains(l, "▄▀▀") || strings.Contains(l, "▀▀▄") {
			return true
		}
	}
	return false
}

// findCurrentTurnStart locates the first line after the latest user prompt
// echo. Returns ok=false if only the splash banner is present.
func findCurrentTurnStart(lines []string, cols int) (start int, ok bool) {
	inFence := false
	lastDivided := -1
	lastCol0 := -1
	for i, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isUserPromptLine(line) {
			lastCol0 = i
			if i > 0 && isTurnDivider(lines[i-1]) {
				lastDivided = i
			} else if i > 1 && strings.TrimSpace(lines[i-1]) == "" && isTurnDivider(lines[i-2]) {
				lastDivided = i
			}
		}
	}

	promptIdx := lastDivided
	if promptIdx < 0 {
		promptIdx = lastCol0
	}
	if promptIdx < 0 {
		if hasSplashBanner(lines) {
			return 0, false
		}
		return 0, true
	}

	start = promptIdx + 1
	// Multi-line prompts prefixed with "> ".
	for start < len(lines) && strings.HasPrefix(lines[start], "> ") {
		promptIdx = start
		start++
	}

	// Wrapped continuation of a long single-line user prompt.
	wrapMin := 50
	if cols > 0 {
		wrapMin = max(40, cols-20)
	}
	prevLine := lines[promptIdx]
	for start < len(lines) {
		curr := lines[start]
		if strings.TrimSpace(curr) == "" ||
			isToolHeader(curr) || isThoughtHeader(curr) || isToolDetail(curr) || isSpinnerLine(curr) ||
			isHorizontalRule(curr) || isTitledDivider(curr) || isBoxStart(curr) || isAnnouncementLine(curr) || isInterruptedBanner(curr) {
			break
		}
		prevLen := displayWidth(strings.TrimRight(prevLine, " \t"))
		if prevLen >= wrapMin && strings.HasPrefix(curr, "  ") && !startsMarkdownBlock(strings.TrimSpace(curr)) {
			prevLine = curr
			start++
			continue
		}
		break
	}
	return start, true
}

func isToolHeader(line string) bool {
	if leadingSpaces(line) > 1 {
		// Indented lines are usually markdown content unless explicitly marked
		// as an agy collapsible tool call.
		return strings.Contains(line, "(ctrl+o to expand)") && startsWithToolGlyph(strings.TrimSpace(line))
	}
	return startsWithToolGlyph(strings.TrimSpace(line))
}

func startsWithToolGlyph(trimmed string) bool {
	for _, prefix := range []string{"● ", "○ ", "✓ ", "✔ ", "✗ ", "✘ ", "◆ ", "∴ "} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func isToolDetail(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "⎿")
}

func isBoxStart(line string) bool {
	t := strings.TrimSpace(line)
	// Exclude rendered markdown tables (e.g. "┌──┬──┐" / "╭──┬──╮"), which
	// include column tee junctions ('┬') and belong to assistant prose.
	return (strings.HasPrefix(t, "╭") || strings.HasPrefix(t, "┌")) &&
		strings.ContainsRune(t, '─') && !strings.ContainsRune(t, '┬')
}

func isBoxEnd(line string) bool {
	t := strings.TrimSpace(line)
	return (strings.HasPrefix(t, "╰") || strings.HasPrefix(t, "└")) && strings.ContainsRune(t, '─')
}

func isBoxLine(line string) bool {
	t := strings.TrimSpace(line)
	for _, prefix := range []string{"│", "┃", "├", "╭", "┌", "╰", "└"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func isAnnouncementLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "┃")
}

func isExpandHint(line string) bool {
	return expandHintRe.MatchString(strings.TrimSpace(line))
}

func isThoughtHeader(line string) bool {
	return thoughtHeaderRe.MatchString(line)
}

func isInterruptedBanner(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "Interrupted ·")
}

func isQuestionEchoLine(line string) bool {
	return strings.HasPrefix(line, "? ") && strings.TrimSpace(line[2:]) != ""
}

// skipThroughLastTool scans a turn's lines and returns the index immediately
// after the final tool call / tool output / thinking block / bordered box.
// Any assistant text preceding a tool call belonged to an earlier planner step.
func skipThroughLastTool(lines []string) int {
	inFence := false
	afterLast := 0
	for i := 0; i < len(lines); {
		line := lines[i]
		if isFenceLine(line) {
			inFence = !inFence
			i++
			continue
		}
		if inFence {
			i++
			continue
		}
		switch {
		case isThoughtHeader(line):
			i = consumeThoughtBlock(lines, i)
			afterLast = i
		case isToolHeader(line):
			i++
			i = consumeToolBody(lines, i)
			afterLast = i
		case isToolDetail(line):
			i = consumeDetailBlock(lines, i)
			afterLast = i
		case isQuestionEchoLine(line):
			i = consumeQuestionEcho(lines, i)
			afterLast = i
		case isBoxStart(line):
			i = consumeBox(lines, i)
			afterLast = i
		case isAnnouncementLine(line) || isTipLine(line) || isSwarmChromeLine(line) ||
			isTaskDockLine(line) || isDockOverflowLine(line) || isExpandHint(line) ||
			isTitledDivider(line) || isInterruptedBanner(line):
			i++
			afterLast = i
		default:
			i++
		}
	}
	return afterLast
}

// consumeThoughtBlock skips an agy collapsible thinking header
// ("▸ Thought for 10s, 384 tokens", "▾ Thought for ...", or "│ ▾ Thought for ...")
// along with any immediately contiguous indented CoT preview lines or "│"-framed
// expanded thought lines. Assistant prose after a blank line is preserved.
func consumeThoughtBlock(lines []string, i int) int {
	i++ // consume the Thought header itself
	for i < len(lines) {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			break
		}
		if isToolHeader(l) || isThoughtHeader(l) || isToolDetail(l) || isHorizontalRule(l) || isTitledDivider(l) {
			break
		}
		if isBoxLine(l) || leadingSpaces(l) >= 2 || isExpandHint(l) {
			i++
			continue
		}
		break
	}
	return i
}

// consumeQuestionEcho skips an AskQuestion history prompt line ("? <question>")
// and any immediately wrapped continuation lines without a blank separator.
func consumeQuestionEcho(lines []string, i int) int {
	i++
	for i < len(lines) {
		l := lines[i]
		if strings.TrimSpace(l) == "" || isToolHeader(l) || isThoughtHeader(l) ||
			isQuestionEchoLine(l) || isHorizontalRule(l) || isTitledDivider(l) || isBoxStart(l) {
			break
		}
		if leadingSpaces(l) == 0 {
			i++
			continue
		}
		break
	}
	return i
}

func consumeToolBody(lines []string, i int) int {
	for i < len(lines) {
		// Allow a single blank line only when followed by attached tool output.
		if strings.TrimSpace(lines[i]) == "" {
			if i+1 < len(lines) && (isToolDetail(lines[i+1]) || isBoxStart(lines[i+1]) || isBoxLine(lines[i+1]) || isExpandHint(lines[i+1])) {
				i++
				continue
			}
			break
		}
		switch {
		case isToolDetail(lines[i]):
			i = consumeDetailBlock(lines, i)
		case isBoxStart(lines[i]):
			i = consumeBox(lines, i)
		case isBoxLine(lines[i]) || isExpandHint(lines[i]) || isHorizontalRule(lines[i]):
			i++
		default:
			return i
		}
	}
	return i
}

func consumeDetailBlock(lines []string, i int) int {
	i++ // consume the "⎿" line itself
	for i < len(lines) {
		l := lines[i]
		if strings.TrimSpace(l) == "" || isToolHeader(l) || isToolDetail(l) {
			break
		}
		if leadingSpaces(l) >= 4 || isBoxLine(l) || isExpandHint(l) {
			i++
			continue
		}
		break
	}
	return i
}

func consumeBox(lines []string, i int) int {
	i++ // consume top border
	for i < len(lines) {
		end := isBoxEnd(lines[i])
		if isBoxLine(lines[i]) || end {
			i++
			if end {
				break
			}
			continue
		}
		break
	}
	return i
}

func trimEdgeChrome(lines []string) []string {
	start := 0
	for start < len(lines) {
		if isBottomChromeLine(lines[start]) {
			start++
			continue
		}
		break
	}
	end := len(lines)
	for end > start {
		if isBottomChromeLine(lines[end-1]) {
			end--
			continue
		}
		break
	}
	return lines[start:end]
}

func leadingSpaces(line string) int {
	n := 0
	for _, r := range line {
		if r == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}

func formatExtractedLines(lines []string, cols int) string {
	// Trim trailing whitespace and measure minimum left margin.
	trimmed := make([]string, len(lines))
	minIndent := -1
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		trimmed[i] = l
		if l == "" {
			continue
		}
		ind := leadingSpaces(l)
		if minIndent < 0 || ind < minIndent {
			minIndent = ind
		}
	}
	if minIndent < 0 {
		return ""
	}
	if minIndent > 0 {
		for i, l := range trimmed {
			if len(l) >= minIndent {
				trimmed[i] = l[minIndent:]
			}
		}
	}

	// Adjust effective column width after dedenting common margin.
	effCols := cols
	if effCols > minIndent {
		effCols -= minIndent
	}

	converted := convertBoxTables(trimmed)
	out := joinWrappedLines(converted, effCols)
	out = escapeRawHTMLInLines(out)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// convertBoxTables converts Unicode box-drawing tables rendered by agy's TUI
// (e.g. ┌──┬──┐ / │ ... │ / ├──┼──┤ / └──┴──┘ or rounded ╭──┬──╮) into GFM pipe
// tables so Goldmark/mdview renders them as structured tables instead of a
// flattened paragraph. Multi-line wrapped cells and mid-stream tables without
// a closing bottom border are handled cleanly.
func convertBoxTables(lines []string) []string {
	var out []string
	inFence := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if isFenceLine(line) {
			inFence = !inFence
			out = append(out, line)
			i++
			continue
		}
		if inFence {
			out = append(out, line)
			i++
			continue
		}
		if isBoxTableStart(lines, i) {
			end := consumeBoxTable(lines, i)
			if gfm := renderBoxTableAsGFM(lines[i:end]); len(gfm) > 0 {
				if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
					out = append(out, "")
				}
				out = append(out, gfm...)
				if end < len(lines) && strings.TrimSpace(lines[end]) != "" {
					out = append(out, "")
				}
				i = end
				continue
			}
		}
		if wantCols, ok := isBorderlessTableStart(lines, i); ok {
			end := consumeBorderlessTable(lines, i, wantCols)
			if gfm := renderBorderlessTableAsGFM(lines[i:end], wantCols); len(gfm) > 0 {
				if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
					out = append(out, "")
				}
				out = append(out, gfm...)
				if end < len(lines) && strings.TrimSpace(lines[end]) != "" {
					out = append(out, "")
				}
				i = end
				continue
			}
		}
		out = append(out, line)
		i++
	}
	return out
}

func isBoxTableTop(line string) bool {
	t := strings.TrimSpace(line)
	return (strings.HasPrefix(t, "┌") || strings.HasPrefix(t, "╭")) &&
		strings.ContainsRune(t, '─') && strings.ContainsRune(t, '┬')
}

func isBoxTableMid(line string) bool {
	t := strings.TrimSpace(line)
	return (strings.HasPrefix(t, "├") || strings.HasPrefix(t, "╞")) &&
		(strings.ContainsRune(t, '┼') || strings.ContainsRune(t, '╪')) &&
		(strings.ContainsRune(t, '─') || strings.ContainsRune(t, '═'))
}

func isBoxTableBottom(line string) bool {
	t := strings.TrimSpace(line)
	return (strings.HasPrefix(t, "└") || strings.HasPrefix(t, "╰")) &&
		strings.ContainsRune(t, '─') && strings.ContainsRune(t, '┴')
}

func isBoxTableDataRow(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "│") && !strings.HasPrefix(t, "┃") {
		return false
	}
	return strings.Count(t, "│")+strings.Count(t, "┃") >= 2
}

func isBoxTableStart(lines []string, i int) bool {
	if isBoxTableTop(lines[i]) {
		return true
	}
	// Handle a table whose top border scrolled off the top of the buffer:
	// data row(s) followed within a few lines by a ├──┼──┤ divider.
	if isBoxTableDataRow(lines[i]) {
		limit := min(len(lines), i+4)
		for j := i + 1; j < limit; j++ {
			if isBoxTableMid(lines[j]) {
				return true
			}
			if !isBoxTableDataRow(lines[j]) {
				return false
			}
		}
	}
	return false
}

func consumeBoxTable(lines []string, start int) int {
	i := start
	if isBoxTableTop(lines[i]) {
		i++
	}
	for i < len(lines) {
		switch {
		case isBoxTableBottom(lines[i]):
			return i + 1
		case isBoxTableMid(lines[i]) || isBoxTableDataRow(lines[i]):
			i++
		default:
			return i
		}
	}
	return i
}

func splitBoxTableCells(line string) []string {
	t := strings.TrimSpace(line)
	// Strip leading border glyph ("│" or "┃").
	if strings.HasPrefix(t, "│") {
		t = strings.TrimPrefix(t, "│")
	} else if strings.HasPrefix(t, "┃") {
		t = strings.TrimPrefix(t, "┃")
	}
	// Strip trailing border glyph if present (may be missing on a mid-stream line).
	if strings.HasSuffix(t, "│") {
		t = strings.TrimSuffix(t, "│")
	} else if strings.HasSuffix(t, "┃") {
		t = strings.TrimSuffix(t, "┃")
	}
	// Split on either "│" or "┃".
	var cells []string
	start := 0
	for idx, r := range t {
		if r == '│' || r == '┃' {
			cells = append(cells, strings.TrimSpace(t[start:idx]))
			start = idx + utf8.RuneLen(r)
		}
	}
	cells = append(cells, strings.TrimSpace(t[start:]))
	return cells
}

func isBorderlessTableMid(line string) (cols int, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "├") || strings.HasPrefix(t, "╞") {
		return 0, false
	}
	cross, horiz := 0, 0
	for _, r := range t {
		switch r {
		case '─', '━', '═':
			horiz++
		case '┼', '╪', '╂', '┿':
			cross++
		case ' ':
		default:
			return 0, false
		}
	}
	if cross < 1 || horiz < 3 {
		return 0, false
	}
	return cross + 1, true
}

func isBorderlessTableRow(line string, wantCols int) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	pipes := strings.Count(t, "│") + strings.Count(t, "┃")
	if pipes < 1 {
		return false
	}
	if wantCols > 0 {
		return pipes+1 == wantCols
	}
	return !strings.HasPrefix(t, "│") && !strings.HasPrefix(t, "┃")
}

func isBorderlessTableStart(lines []string, i int) (int, bool) {
	if !isBorderlessTableRow(lines[i], 0) {
		return 0, false
	}
	limit := min(len(lines), i+4)
	for j := i + 1; j < limit; j++ {
		if cols, ok := isBorderlessTableMid(lines[j]); ok {
			for k := i; k < j; k++ {
				if !isBorderlessTableRow(lines[k], cols) {
					return 0, false
				}
			}
			return cols, true
		}
		if !isBorderlessTableRow(lines[j], 0) {
			return 0, false
		}
	}
	return 0, false
}

func consumeBorderlessTable(lines []string, start, wantCols int) int {
	i := start
	for i < len(lines) {
		if cols, ok := isBorderlessTableMid(lines[i]); ok && cols == wantCols {
			i++
			continue
		}
		if isBorderlessTableRow(lines[i], wantCols) {
			i++
			continue
		}
		break
	}
	return i
}

func splitBorderlessTableCells(line string) []string {
	var cells []string
	start := 0
	for idx, r := range line {
		if r == '│' || r == '┃' {
			cells = append(cells, strings.TrimSpace(line[start:idx]))
			start = idx + utf8.RuneLen(r)
		}
	}
	cells = append(cells, strings.TrimSpace(line[start:]))
	return cells
}

func renderBorderlessTableAsGFM(block []string, wantCols int) []string {
	var items []tableRawLine
	midCount := 0
	for _, line := range block {
		if cols, ok := isBorderlessTableMid(line); ok && cols == wantCols {
			midCount++
			items = append(items, tableRawLine{isMid: true})
			continue
		}
		if isBorderlessTableRow(line, wantCols) {
			items = append(items, tableRawLine{cells: splitBorderlessTableCells(line)})
		}
	}
	if wantCols < 2 || midCount == 0 || len(items) == 0 {
		return nil
	}
	return formatTableItemsAsGFM(items, wantCols, midCount)
}

func renderBoxTableAsGFM(block []string) []string {
	var items []tableRawLine
	midCount := 0
	numCols := 0
	for _, line := range block {
		switch {
		case isBoxTableTop(line) || isBoxTableBottom(line):
			continue
		case isBoxTableMid(line):
			midCount++
			items = append(items, tableRawLine{isMid: true})
		case isBoxTableDataRow(line):
			cells := splitBoxTableCells(line)
			if len(cells) > numCols {
				numCols = len(cells)
			}
			items = append(items, tableRawLine{cells: cells})
		}
	}
	if numCols < 2 || len(items) == 0 {
		return nil
	}
	return formatTableItemsAsGFM(items, numCols, midCount)
}

type tableRawLine struct {
	isMid bool
	cells []string
}

func formatTableItemsAsGFM(items []tableRawLine, numCols, midCount int) []string {

	// Partition lines into header group (before first mid border) and body groups.
	var headerLines [][]string
	var bodyGroups [][][]string
	seenFirstMid := false
	var curGroup [][]string

	flushGroup := func() {
		if len(curGroup) > 0 {
			bodyGroups = append(bodyGroups, curGroup)
			curGroup = nil
		}
	}

	for _, it := range items {
		if it.isMid {
			if !seenFirstMid {
				seenFirstMid = true
			} else {
				flushGroup()
			}
			continue
		}
		if !seenFirstMid {
			headerLines = append(headerLines, it.cells)
			continue
		}
		if midCount >= 2 {
			// Explicit row dividers between body rows: merge all lines within each divider band.
			curGroup = append(curGroup, it.cells)
		} else {
			// Compact table (only header divider): start a new row unless col 0 is
			// empty and another column continues a wrapped cell of the previous row.
			if len(curGroup) > 0 && len(it.cells) > 0 && it.cells[0] == "" && hasNonEmptyCell(it.cells[1:]) {
				curGroup = append(curGroup, it.cells)
			} else {
				flushGroup()
				curGroup = append(curGroup, it.cells)
			}
		}
	}
	flushGroup()

	if len(headerLines) == 0 {
		return nil
	}

	mergeRow := func(rows [][]string) []string {
		merged := make([]string, numCols)
		for col := range numCols {
			var parts []string
			for _, r := range rows {
				if col < len(r) && r[col] != "" {
					parts = append(parts, r[col])
				}
			}
			merged[col] = escapeTableCell(joinTableCellParts(parts))
		}
		return merged
	}

	formatRow := func(cells []string) string {
		return "| " + strings.Join(cells, " | ") + " |"
	}

	sepCells := make([]string, numCols)
	for i := range sepCells {
		sepCells[i] = "---"
	}

	out := make([]string, 0, 2+len(bodyGroups))
	out = append(out, formatRow(mergeRow(headerLines)))
	out = append(out, formatRow(sepCells))
	for _, g := range bodyGroups {
		out = append(out, formatRow(mergeRow(g)))
	}
	return out
}

func hasNonEmptyCell(cells []string) bool {
	for _, c := range cells {
		if c != "" {
			return true
		}
	}
	return false
}

func joinTableCellParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	res := parts[0]
	for i := 1; i < len(parts); i++ {
		prev := parts[i-1]
		curr := parts[i]
		// Inside narrow table cells, column width is bounded by the cell itself.
		cellCols := max(displayWidth(prev), 12)
		sep := joinSeparator(prev, curr, cellCols)
		res += sep + curr
	}
	return res
}

func escapeTableCell(s string) string {
	if !strings.ContainsRune(s, '|') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func joinWrappedLines(lines []string, cols int) []string {
	if cols <= 0 {
		cols = 80
	}
	wrapCols := effectiveWrapCols(lines, cols)

	var out []string
	inFence := false
	rawPrev := ""
	var listStack []int
	inListItem := false
	activeItemIndent := 0
	activeItemBodyCol := 0

	for _, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			out = append(out, line)
			rawPrev = ""
			inListItem = false
			if !inFence {
				listStack = nil
			}
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if line == "" {
			// Collapse consecutive blank lines to one.
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			rawPrev = ""
			inListItem = false
			continue
		}

		ind := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)

		if marker, body, rawMarkerW, ok := parseListItem(trimmed); ok {
			for len(listStack) > 0 && listStack[len(listStack)-1] > ind {
				listStack = listStack[:len(listStack)-1]
			}
			if len(listStack) == 0 || listStack[len(listStack)-1] < ind {
				listStack = append(listStack, ind)
			}
			level := len(listStack) - 1
			out = append(out, strings.Repeat("  ", level)+marker+body)
			rawPrev = line
			inListItem = true
			activeItemIndent = ind
			activeItemBodyCol = ind + rawMarkerW
			continue
		}

		// Hanging-indent continuation of the current list item (including 3rd+ wrapped lines).
		if inListItem && len(out) > 0 && rawPrev != "" &&
			!startsMarkdownBlock(trimmed) &&
			ind >= activeItemIndent+2 && ind <= activeItemBodyCol+2 &&
			!endsWithBlockColon(strings.TrimSpace(rawPrev)) {
			sep := joinSeparator(rawPrev, line, wrapCols)
			out[len(out)-1] += sep + trimmed
			rawPrev = line
			continue
		}

		inListItem = false
		if ind == 0 {
			listStack = nil
		}

		if len(out) > 0 && rawPrev != "" && shouldJoinWrap(rawPrev, line, wrapCols) {
			sep := joinSeparator(rawPrev, line, wrapCols)
			out[len(out)-1] += sep + trimmed
			rawPrev = line
			continue
		}
		out = append(out, line)
		rawPrev = line
	}
	return out
}

func effectiveWrapCols(lines []string, cols int) int {
	maxW := 0
	inFence := false
	for _, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence || isBoxLine(line) || isHorizontalRule(line) {
			continue
		}
		if w := displayWidth(line); w > maxW {
			maxW = w
		}
	}
	// If agy wraps prose to a narrower column width than the terminal composer
	// box (e.g. 80/100 cols inside a 140-col pane), adapt to the observed width
	// while never shrinking below 56 so short lines (like "Status: OK") stay intact.
	if maxW >= 56 && maxW < cols-8 {
		return maxW + 2
	}
	return cols
}

func endsWithBlockColon(trimmed string) bool {
	return strings.HasSuffix(trimmed, ":") && !strings.HasSuffix(trimmed, "://")
}

func shouldJoinWrap(prev, curr string, cols int) bool {
	if prev == "" || curr == "" {
		return false
	}
	pt := strings.TrimSpace(prev)
	ct := strings.TrimSpace(curr)
	if pt == "" || ct == "" {
		return false
	}
	// Never join when curr starts a new markdown block.
	if startsMarkdownBlock(ct) {
		return false
	}
	// Never join onto headings, tables, fences, rules, box borders, or lines ending in ':'.
	if strings.HasPrefix(pt, "#") || strings.HasPrefix(pt, "|") ||
		isFenceLine(pt) || isMarkdownRule(pt) || isBoxLine(pt) || endsWithBlockColon(pt) {
		return false
	}

	prevIndent := leadingSpaces(prev)
	currIndent := leadingSpaces(curr)
	if currIndent != prevIndent {
		// Allow wrapped continuation of a bullet or numbered list item.
		if _, _, rawW, ok := parseListItem(pt); !(ok && currIndent >= prevIndent+2 && currIndent <= prevIndent+rawW+2) {
			return false
		}
	}

	prevW := displayWidth(prev)
	firstW := firstWordWidth(ct)
	minWrapW := max(36, cols/2)
	return prevW >= minWrapW && prevW+1+firstW >= cols-10
}

// joinSeparator returns "" when prev and curr were split mid-token or at
// intra-token punctuation (e.g. "README.m"+"d", "README"+".md", "agent-cli."+"sock",
// "app-"+"server", "--"+"plugin-dir", ", /"+"live"), and " " for normal word wraps.
func joinSeparator(prev, curr string, cols int) string {
	pt := strings.TrimSpace(prev)
	ct := strings.TrimSpace(curr)
	if pt == "" || ct == "" {
		return " "
	}

	firstCurr, _ := utf8.DecodeRuneInString(ct)
	lastPrev, lastSz := utf8.DecodeLastRuneInString(pt)
	beforeLast := rune(0)
	if len(pt) > lastSz {
		beforeLast, _ = utf8.DecodeLastRuneInString(pt[:len(pt)-lastSz])
	}

	// Unclosed/split bold markers ("**" at boundary).
	oddBold := strings.Count(pt, "**")%2 == 1
	if (strings.HasSuffix(pt, "**") && oddBold) || (strings.HasPrefix(ct, "**") && oddBold) {
		return ""
	}

	// URL scheme break ("https://", "file://", "ws://").
	if strings.HasSuffix(pt, "://") && isASCIITokenStart(firstCurr) {
		return ""
	}

	// Double-dash CLI flag break ("--" + "plugin-dir").
	if strings.HasSuffix(pt, "--") && isASCIIIdentRune(firstCurr) {
		return ""
	}

	// Hyphenated identifier/flag break ("app-" + "server", "mako-" + "<hash>").
	if lastPrev == '-' && isASCIIWordOrDot(beforeLast) && (isASCIIIdentRune(firstCurr) || firstCurr == '<') {
		return ""
	}

	// Path or route slash break ("docs/agents/" + "README.md", ", /" + "live"),
	// while keeping spaced separators like "mako-msg / mako-plugin".
	if lastPrev == '/' && (isASCIITokenStart(firstCurr) || firstCurr == '<') {
		if beforeLast != 0 && !unicode.IsSpace(beforeLast) {
			return ""
		}
		// If preceded by space (" /"), check the rune before that space:
		// punctuation/delimiters (", /live", "(/mako)") mean a leading path/route,
		// whereas a word ("mako-msg / mako-plugin") means a binary slash separator.
		trimmedBeforeSlash := strings.TrimRight(pt[:len(pt)-lastSz], " \t")
		if trimmedBeforeSlash == "" {
			return ""
		}
		pre, _ := utf8.DecodeLastRuneInString(trimmedBeforeSlash)
		if !unicode.IsLetter(pre) && !unicode.IsDigit(pre) && pre != ')' && pre != ']' && pre != '`' && pre != '*' {
			return ""
		}
	}

	// Break right before a file extension or dot-member ("README" + ".md", "hooks" + ".sock").
	if firstCurr == '.' && len(ct) > 1 {
		nextR, _ := utf8.DecodeRuneInString(ct[1:])
		if isASCIIIdentRune(nextR) && (isASCIIIdentRune(lastPrev) || lastPrev == '-' || lastPrev == '*') {
			return ""
		}
	}

	// Break right after a dot in a filename or qualified symbol
	// ("agent-cli." + "sock", "mcp_config." + "json", "pi." + "registerMcpServer",
	// "SubAgentHooks." + "IsSubAgent", "agents." + "As[T]").
	if lastPrev == '.' && isASCIIIdentRune(beforeLast) {
		if firstCurr >= 'a' && firstCurr <= 'z' {
			return ""
		}
		if firstCurr >= 'A' && firstCurr <= 'Z' && looksLikeCodeSymbol(firstToken(ct)) {
			return ""
		}
	}

	// Hard-wrapped long path/URL/token split mid-word at the right margin
	// (e.g. "/Users/.../README.m" + "d", "/Users/.../READM" + "E.md").
	if isASCIITokenContinue(lastPrev) && isASCIITokenStart(firstCurr) {
		lastTok := lastToken(pt)
		firstTok := firstToken(ct)
		isPathOrLong := strings.ContainsRune(lastTok, '/') || displayWidth(lastTok) >= 32
		if isPathOrLong {
			if oddBold {
				return ""
			}
			if displayWidth(prev) >= cols-5 && isLikelyMidTokenContinuation(lastTok, firstTok) {
				return ""
			}
		}
	}

	return " "
}

func isASCIIIdentRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func isASCIIWordOrDot(r rune) bool {
	return isASCIIIdentRune(r) || r == '.'
}

func isASCIITokenStart(r rune) bool {
	return isASCIIIdentRune(r) || r == '.' || r == '/' || r == '-' || r == '~'
}

func isASCIITokenContinue(r rune) bool {
	return isASCIIIdentRune(r) || r == '.' || r == '/' || r == '-' || r == ':'
}

func lastToken(s string) string {
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s
	}
	return s[i+1:]
}

func firstToken(s string) string {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s
	}
	return s[:i]
}

// looksLikeCodeSymbol reports whether an uppercase-initial token looks like a
// Go/JS/TS symbol (CamelCase, dotted, callable, generic, or snake_case) rather
// than a normal capitalized English sentence starter like "The" or "Next".
func looksLikeCodeSymbol(tok string) bool {
	tok = strings.TrimRight(tok, ".,;:!?)]}*`")
	if len(tok) < 2 {
		return false
	}
	hasLower := false
	for i, r := range tok {
		if i > 0 && r >= 'A' && r <= 'Z' && hasLower {
			return true // CamelCase (e.g. IsSubAgent)
		}
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
		if r == '(' || r == '[' || r == '_' || r == '.' {
			return true
		}
	}
	return false
}

// isLikelyMidTokenContinuation distinguishes a hard-split path/URL fragment
// (".../README.m" + "d", ".../READM" + "E.md") from a complete path followed
// by a normal word (".../README.md" + "explains").
func isLikelyMidTokenContinuation(lastTok, firstTok string) bool {
	cleanFirst := strings.TrimRight(firstTok, ",;:!?)")
	if strings.ContainsAny(cleanFirst, "./_*") {
		return true
	}
	// Very short 1-2 letter fragment (like "d" after "README.m") is a hard wrap.
	if len(cleanFirst) <= 2 && isAllASCIIAlphaNum(cleanFirst) {
		return true
	}
	// If the path's final segment has an incomplete 1-char extension (".m") or
	// ends in uppercase letters continued by uppercase ("READM" + "E").
	base := lastTok
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if dot := strings.LastIndexByte(base, '.'); dot >= 0 && len(base)-dot-1 == 1 {
		return true
	}
	return false
}

func isAllASCIIAlphaNum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// escapeRawHTMLInLines escapes tag-like placeholders (e.g. <hash>, <id>,
// <task-notification>) outside fenced code blocks and inline code spans so
// Goldmark renders them verbatim instead of stripping them as raw HTML tags.
func escapeRawHTMLInLines(lines []string) []string {
	inFence := false
	for i, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence || !strings.ContainsRune(line, '<') {
			continue
		}
		lines[i] = escapeRawHTMLOutsideCode(line)
	}
	return lines
}

func escapeRawHTMLOutsideCode(line string) string {
	var b strings.Builder
	b.Grow(len(line) + 8)
	i := 0
	for i < len(line) {
		if line[i] == '`' {
			// Copy inline backtick code span verbatim.
			ticks := 0
			for i+ticks < len(line) && line[i+ticks] == '`' {
				ticks++
			}
			delim := line[i : i+ticks]
			if end := strings.Index(line[i+ticks:], delim); end >= 0 {
				next := i + ticks + end + ticks
				b.WriteString(line[i:next])
				i = next
				continue
			}
			b.WriteString(delim)
			i += ticks
			continue
		}
		if line[i] == '<' && (i == 0 || line[i-1] != '\\') {
			if loc := rawHTMLTagRe.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
				b.WriteByte('\\')
				b.WriteString(line[i : i+loc[1]])
				i += loc[1]
				continue
			}
		}
		b.WriteByte(line[i])
		i++
	}
	return b.String()
}

func startsMarkdownBlock(trimmed string) bool {
	if strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "> ") || trimmed == ">" ||
		strings.HasPrefix(trimmed, "|") ||
		isFenceLine(trimmed) || isMarkdownRule(trimmed) || isListItem(trimmed) || isBoxLine(trimmed) {
		return true
	}
	return false
}

// unicodeBulletPrefixes are bullet glyphs rendered by agy's TUI for markdown
// unordered list items, normalized back to "- " for Goldmark rendering.
var unicodeBulletPrefixes = []string{"• ", "◦ ", "▪ ", "▫ ", "‣ ", "∙ ", "▸ ", "· "}

// parseListItem parses a markdown or agy TUI-rendered list item line.
// It returns the normalized markdown marker ("- " for unicode bullets, or the
// original ASCII marker), the item body, the original marker's display width,
// and whether the line is a list item.
func parseListItem(trimmed string) (marker, body string, rawMarkerWidth int, ok bool) {
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, p) {
			return p, trimmed[len(p):], 2, true
		}
	}
	for _, p := range unicodeBulletPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return "- ", trimmed[len(p):], displayWidth(p), true
		}
	}
	if m := numberedListRe.FindStringSubmatch(trimmed); m != nil {
		return m[1], m[2], displayWidth(m[1]), true
	}
	return "", "", 0, false
}

func isListItem(trimmed string) bool {
	_, _, _, ok := parseListItem(trimmed)
	return ok
}

func isMarkdownRule(trimmed string) bool {
	if isHorizontalRule(trimmed) {
		return true
	}
	if len(trimmed) < 3 {
		return false
	}
	first := rune(trimmed[0])
	if first != '-' && first != '*' && first != '_' {
		return false
	}
	count := 0
	for _, r := range trimmed {
		if r == first {
			count++
		} else if r != ' ' {
			return false
		}
	}
	return count >= 3
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

func runeDisplayWidth(r rune) int {
	if r < 0x20 || r == 0x7f || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

func firstWordWidth(s string) int {
	w := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			break
		}
		w += runeDisplayWidth(r)
	}
	return w
}

func matchesLogged(extracted, logged string) bool {
	na := normalizeForCompare(extracted)
	if na == "" {
		return false
	}
	match := func(candidate string) bool {
		nb := normalizeForCompare(candidate)
		if nb == "" {
			return false
		}
		if na == nb {
			return true
		}
		// If only a trailing or interior slice of an already-logged reply fits in
		// the buffer (e.g. long reply scrolled off top, or trailing rule/tip stripped),
		// suppress it as well.
		return utf8.RuneCountInString(na) >= 20 && strings.Contains(nb, na)
	}
	if match(logged) {
		return true
	}
	// agy's TUI may expand [label](file:///abs/path) to /abs/path on screen.
	if strings.Contains(logged, "](file://") {
		if match(fileLinkRe.ReplaceAllString(logged, "$1")) {
			return true
		}
	}
	return false
}

// normalizeForCompare strips markdown block/inline markers that agy renders
// differently on the PTY (links `[label](url)`, heading `#`, list `-`/`+`/`•`,
// blockquote `>`, code fences, horizontal rules, table pipes/box borders,
// backticks, bold/italic/strike `*`/`_`/`~`) and collapses whitespace so PTY
// plain text matches raw markdown stored in transcript_full.jsonl.
func normalizeForCompare(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := true
	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || isFenceLine(t) || isMarkdownRule(t) || isTitledDivider(t) || tableSeparatorRe.MatchString(t) {
			continue
		}
		// Replace markdown links/images with their visible label text.
		if strings.Contains(t, "](") {
			t = markdownLinkRe.ReplaceAllString(t, "$1")
		}
		// Normalize inline HTML line breaks ("<br>", "<br/>") to spaces.
		if strings.Contains(t, "<") {
			t = htmlBreakRe.ReplaceAllString(t, " ")
		}
		// Strip line-leading heading prefixes ("## Heading" -> "Heading").
		if hashes := strings.TrimLeft(t, "#"); len(hashes) < len(t) && (hashes == "" || strings.HasPrefix(hashes, " ") || strings.HasPrefix(hashes, "\t")) {
			t = strings.TrimSpace(hashes)
		}
		// Strip line-leading blockquote marker ("> quote" -> "quote").
		if strings.HasPrefix(t, "> ") {
			t = strings.TrimSpace(t[2:])
		}
		// Strip line-leading bullet list markers ("- ", "+ ", "* ", "• ", "◦ ", ...).
		if _, body, _, ok := parseListItem(t); ok {
			t = strings.TrimSpace(body)
		}
		runes := []rune(t)
		for i := 0; i < len(runes); i++ {
			r := runes[i]
			// Unescape markdown backslash escapes (e.g. "\_" -> "_", "\<" -> "<").
			if r == '\\' && i+1 < len(runes) {
				switch runes[i+1] {
				case '*', '_', '`', '[', ']', '~', '|', '#', '>', '<':
					continue
				}
			}
			switch r {
			case '`', '*', '_', '~', '•', '◦', '▪', '▫', '‣', '∙', '▸':
				continue
			case '|', '─', '━', '═', '│', '┃', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼', '╭', '╮', '╰', '╯':
				if !inSpace {
					b.WriteByte(' ')
					inSpace = true
				}
				continue
			}
			if unicode.IsSpace(r) {
				if !inSpace {
					b.WriteByte(' ')
					inSpace = true
				}
				continue
			}
			b.WriteRune(r)
			inSpace = false
		}
		if !inSpace {
			b.WriteByte(' ')
			inSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}
