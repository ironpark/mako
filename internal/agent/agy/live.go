package agy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/detect"
)

var (
	spinnerLineRe    = regexp.MustCompile(`^\s*[\x{2801}-\x{28FF}]+(?:\s|$)`)
	promptFooterRe   = regexp.MustCompile(`^\s*\? for shortcuts\b`)
	numberedListRe   = regexp.MustCompile(`^\d+[.)]\s+`)
	expandHintRe     = regexp.MustCompile(`^(?:\(ctrl\+o to expand\)|(?:…|\.\.\.)\s*\+\d+\s+(?:more\s+)?lines?)$`)
	markdownLinkRe   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)\s]+(?:\s+"[^"]*")?\)`)
	tableSeparatorRe = regexp.MustCompile(`^\|?(?:\s*:?-{3,}:?\s*\|)+\s*:?-{3,}:?\s*\|?$`)
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

	// Gate 1: Reject idle, dialog, permission, trust, and slash-menu screens.
	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.State == detect.Idle {
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
	if detected.State == detect.Blocked || hasDialogFooter(lines) {
		return ""
	}
	return extractBodyText(lines, true)
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
	if detected.State == detect.Blocked || detected.ID == "slash_menu" || hasDialogFooter(lines) {
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
				if !isSpinnerLine(lines[i]) || isLiveActivityHeader(lines[i]) {
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

// ExtractActivity extracts only active background Swarm/Agent status rows from
// agy's bottom chrome (never foreground braille spinners), stripped of raw
// keybinding hints like "alt+s board · alt+x dismiss". Returns "" on dialogs
// or when no Swarm/Agent work is active.
func (*Agent) ExtractActivity(screen string) string {
	norm := strings.ReplaceAll(strings.ReplaceAll(screen, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(norm, "\n")
	end := trimTrailingBlanks(rawLines)
	if end == 0 {
		return ""
	}
	lines := rawLines[:end]

	detected := screenRules.Detect(detect.Input{Screen: norm})
	if detected.State == detect.Blocked || detected.ID == "slash_menu" || hasDialogFooter(lines) {
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
// horizontal rules around a ">" line) within the bottom 24 lines of the screen.
func findBottomComposerBox(lines []string) (top, bottom int) {
	floor := max(0, len(lines)-24)
	for b := len(lines) - 1; b >= floor; b-- {
		if !isHorizontalRule(lines[b]) {
			continue
		}
		limit := max(0, b-12)
		for t := b - 1; t >= limit; t-- {
			if isHorizontalRule(lines[t]) {
				if looksLikeComposerBody(lines[t+1 : b]) {
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

func normalizeStatusLabel(line string) string {
	t := strings.TrimSpace(line)
	// Strip leading braille spinner rune(s) and separating whitespace.
	t = strings.TrimLeftFunc(t, func(r rune) bool {
		return (r >= 0x2801 && r <= 0x28FF) || unicode.IsSpace(r)
	})
	t = strings.TrimSpace(t)
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

func hasDialogFooter(lines []string) bool {
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

	// 2. Iteratively strip stacked bottom chrome above the footer: composer
	//    prompt box(es), Swarm/Agent status bars, artifact Tip banners, working
	//    spinners, and horizontal rules.
	for end > 0 {
		prev := end
		// Strip a prompt composer box:
		//    ────────────────────
		//    > (optional draft)
		//    ────────────────────
		if end > 0 && isHorizontalRule(lines[end-1]) {
			if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-1])); w >= 20 && w > cols {
				cols = w
			}
			boxTop := -1
			limit := max(0, end-14)
			for i := end - 2; i >= limit; i-- {
				if isHorizontalRule(lines[i]) {
					if looksLikeComposerBody(lines[i+1 : end-1]) {
						boxTop = i
					}
					break
				}
				if isToolHeader(lines[i]) {
					break
				}
			}
			if boxTop >= 0 {
				if w := utf8.RuneCountInString(strings.TrimSpace(lines[boxTop])); w > cols && w >= 20 {
					cols = w
				}
				end = boxTop
			} else {
				end--
			}
		} else if end >= 2 && isComposerLine(lines[end-1]) && isHorizontalRule(lines[end-2]) {
			if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-2])); w >= 20 && w > cols {
				cols = w
			}
			end -= 2
		}

		// Strip trailing blanks, spinners, rules, Swarm/Agent bars, and Tip banners.
		for end > 0 && (isBottomChromeLine(lines[end-1]) || isActivityContinuationCandidate(lines, end-1)) {
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
	return strings.HasPrefix(t, "Tip:")
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
		isTipLine(line) ||
		isSwarmChromeLine(line) ||
		isExpandHint(line)
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
			isToolHeader(curr) || isToolDetail(curr) || isSpinnerLine(curr) ||
			isHorizontalRule(curr) || isBoxStart(curr) || isAnnouncementLine(curr) {
			break
		}
		prevLen := utf8.RuneCountInString(strings.TrimRight(prevLine, " \t"))
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

// skipThroughLastTool scans a turn's lines and returns the index immediately
// after the final tool call / tool output / bordered box block. Any assistant
// text preceding a tool call belonged to an earlier planner step.
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
		case isToolHeader(line):
			i++
			i = consumeToolBody(lines, i)
			afterLast = i
		case isToolDetail(line):
			i = consumeDetailBlock(lines, i)
			afterLast = i
		case isBoxStart(line):
			i = consumeBox(lines, i)
			afterLast = i
		case isAnnouncementLine(line) || isTipLine(line) || isSwarmChromeLine(line):
			i++
			afterLast = i
		default:
			i++
		}
	}
	return afterLast
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

	out := joinWrappedLines(trimmed, effCols)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func joinWrappedLines(lines []string, cols int) []string {
	if cols <= 0 {
		cols = 80
	}
	var out []string
	inFence := false
	rawPrev := ""
	for _, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			out = append(out, line)
			rawPrev = ""
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
			continue
		}
		if len(out) > 0 && rawPrev != "" && shouldJoinWrap(rawPrev, line, cols) {
			out[len(out)-1] += " " + strings.TrimSpace(line)
			rawPrev = line
			continue
		}
		out = append(out, line)
		rawPrev = line
	}
	return out
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
		isFenceLine(pt) || isMarkdownRule(pt) || isBoxLine(pt) || strings.HasSuffix(pt, ":") {
		return false
	}

	prevIndent := leadingSpaces(prev)
	currIndent := leadingSpaces(curr)
	if currIndent != prevIndent {
		// Allow wrapped continuation of a bullet or numbered list item.
		if !(isListItem(pt) && currIndent >= prevIndent+2 && currIndent <= prevIndent+4) {
			return false
		}
	}

	prevLen := utf8.RuneCountInString(prev)
	firstWord := firstWordLen(ct)
	minWrapLen := max(36, cols/2)
	return prevLen >= minWrapLen && prevLen+1+firstWord >= cols-10
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

func isListItem(trimmed string) bool {
	for _, p := range []string{"- ", "* ", "+ ", "• "} {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return numberedListRe.MatchString(trimmed)
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

func firstWordLen(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			break
		}
		n++
	}
	return n
}

func matchesLogged(extracted, logged string) bool {
	na := normalizeForCompare(extracted)
	nb := normalizeForCompare(logged)
	if na == "" || nb == "" {
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
		if t == "" || isFenceLine(t) || isMarkdownRule(t) || tableSeparatorRe.MatchString(t) {
			continue
		}
		// Replace markdown links/images with their visible label text.
		if strings.Contains(t, "](") {
			t = markdownLinkRe.ReplaceAllString(t, "$1")
		}
		// Strip line-leading heading prefixes ("## Heading" -> "Heading").
		if hashes := strings.TrimLeft(t, "#"); len(hashes) < len(t) && (hashes == "" || strings.HasPrefix(hashes, " ") || strings.HasPrefix(hashes, "\t")) {
			t = strings.TrimSpace(hashes)
		}
		// Strip line-leading blockquote marker ("> quote" -> "quote").
		if strings.HasPrefix(t, "> ") {
			t = strings.TrimSpace(t[2:])
		}
		// Strip line-leading bullet list markers ("- ", "+ ", "* ", "• ").
		for _, p := range []string{"- ", "+ ", "* ", "• "} {
			if strings.HasPrefix(t, p) {
				t = strings.TrimSpace(t[len(p):])
				break
			}
		}
		runes := []rune(t)
		for i := 0; i < len(runes); i++ {
			r := runes[i]
			// Unescape markdown backslash escapes (e.g. "\_" -> "_").
			if r == '\\' && i+1 < len(runes) {
				switch runes[i+1] {
				case '*', '_', '`', '[', ']', '~', '|', '#', '>':
					continue
				}
			}
			switch r {
			case '`', '*', '_', '~', '•':
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
