package agy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/detect"
)

var (
	spinnerLineRe  = regexp.MustCompile(`^\s*[\x{2801}-\x{28FF}]+(?:\s|$)`)
	promptFooterRe = regexp.MustCompile(`^\s*\? for shortcuts\b`)
	numberedListRe = regexp.MustCompile(`^\d+[.)]\s+`)
	expandHintRe   = regexp.MustCompile(`^(?:\(ctrl\+o to expand\)|(?:…|\.\.\.)\s*\+\d+\s+(?:more\s+)?lines?)$`)
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

	// Step 1: Strip bottom chrome (footer, stacked custom statusLine, prompt
	// composer box, and working spinner) and infer terminal width from the
	// prompt box rule.
	bodyEnd, cols := stripBottomChrome(lines)
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
	cleaned := formatExtractedLines(cand, cols)
	if cleaned == "" {
		return ""
	}

	// Step 5: Suppress text already persisted to transcript_full.jsonl.
	if lastLogged != "" && matchesLogged(cleaned, lastLogged) {
		return ""
	}
	return cleaned
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
		line := lines[i]
		if promptFooterRe.MatchString(line) {
			return true
		}
		lower := strings.ToLower(line)
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

// stripBottomChrome removes the working footer (plus any stacked custom
// statusLine beneath it), the composer prompt box, and trailing spinners/rules.
// It returns the exclusive end index of the conversation body and the inferred
// terminal column width (0 if unknown).
func stripBottomChrome(lines []string) (end int, cols int) {
	end = len(lines)

	// 1. Strip default working footer and any custom statusLine below it.
	footerFloor := max(0, end-10)
	for i := end - 1; i >= footerFloor; i-- {
		if isEscCancelLine(lines[i]) {
			end = i
			break
		}
	}
	end = trimTrailingBlanks(lines[:end])

	// 2. Strip the prompt composer box:
	//    ────────────────────
	//    > (optional draft)
	//    ────────────────────
	if end > 0 && isHorizontalRule(lines[end-1]) {
		if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-1])); w >= 20 {
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
		if w := utf8.RuneCountInString(strings.TrimSpace(lines[end-2])); w >= 20 {
			cols = w
		}
		end -= 2
	}

	// 3. Strip trailing blank lines, spinners, and stray rules right above the prompt box.
	for end > 0 {
		line := lines[end-1]
		if strings.TrimSpace(line) == "" || isSpinnerLine(line) || isHorizontalRule(line) {
			end--
			continue
		}
		break
	}
	return end, cols
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
		case isAnnouncementLine(line):
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
		l := lines[start]
		if strings.TrimSpace(l) == "" || isSpinnerLine(l) || isHorizontalRule(l) || isExpandHint(l) {
			start++
			continue
		}
		break
	}
	end := len(lines)
	for end > start {
		l := lines[end-1]
		if strings.TrimSpace(l) == "" || isSpinnerLine(l) || isHorizontalRule(l) || isExpandHint(l) {
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
	// If only the trailing portion of a long already-logged reply fits in the
	// buffer, suppress it as well.
	return len(na) >= 20 && strings.HasSuffix(nb, na)
}

// normalizeForCompare strips markdown block/inline markers that agy renders
// differently on the PTY (heading `#`, list `-`/`+`/`•`, blockquote `>`,
// backticks, bold/italic `*`/`_`) and collapses whitespace so PTY plain text
// matches raw markdown stored in transcript_full.jsonl.
func normalizeForCompare(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := true
	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
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
		for _, r := range t {
			switch r {
			case '`', '*', '_', '•':
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
