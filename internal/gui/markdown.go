package gui

import (
	"cmp"
	"html"
	"image/color"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// A Markdown file's tab shows it rendered as well as its source: the
// header's switch flips between them. The page is laid out from the
// file's syntax tree, as GitHub renders one: headings, paragraphs with
// their emphasis, code and links, lists and task lists, quotes, tables,
// code blocks highlighted in their language, and the images beside the
// file.

// isMarkdown reports whether a file's name says it is Markdown.
func isMarkdown(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd", ".mdx":
		return true
	}
	return false
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// mdBodySize is the page's text size; headings scale from it.
const mdBodySize = 14

// mdRenderer turns one file's syntax tree into widgets.
type mdRenderer struct {
	m     *model
	id    string // the file's tab
	doc   docInfo
	src   []byte
	depth int // how deep the list being rendered is nested
	folds int // the details rendered so far
}

// markdownView is a Markdown file rendered, scrolling. Its text selects as
// one page: a drag runs from one block into the next, and a click anywhere
// in the view, a block or the space around them, drops the whole
// selection.
func markdownView(m *model, id string, src string) ggui.Widget {
	r := &mdRenderer{m: m, id: id, doc: ggui.Peek(m.Docs)[id], src: []byte(src)}
	root := markdown.Parser().Parse(text.NewReader(r.src))
	page := ggui.Box(ggui.Column(r.blocks(root)...).Gap(14).Stretch()).MaxWidth(880)
	scroll := ggui.Scroll(ggui.Align(ggui.Padding(page, 28, 36, 48, 36)).Top()).Bar(skin.Surface3)
	return ggui.Box(ggui.SelectionArea(scroll)).Fill(skin.Surface0)
}

// markdownText is Markdown written somewhere other than a file, as on
// GitHub: its blocks, to set in a column of their own. Its relative links
// lead nowhere.
func markdownText(m *model, src string) ggui.Widget {
	r := &mdRenderer{m: m, src: []byte(src)}
	root := markdown.Parser().Parse(text.NewReader(r.src))
	return ggui.Column(r.blocks(root)...).Gap(10).Stretch()
}

// blocks renders the block children of n.
func (r *mdRenderer) blocks(n ast.Node) []ggui.Widget {
	var nodes []ast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		nodes = append(nodes, c)
	}
	return r.blockList(nodes)
}

func (r *mdRenderer) block(n ast.Node) ggui.Widget {
	switch n := n.(type) {
	case *ast.Heading:
		sizes := [...]float64{0, 26, 21, 17.5, 15.5, 14, 13.5}
		t := r.paragraph(n, sizes[min(n.Level, 6)]).Weight(ggui.WeightSemibold).LineHeight(1.3)
		if n.Level <= 2 {
			return ggui.Column(ggui.Padding(t, 6, 0, 0, 0), hline()).Gap(8).Stretch()
		}
		return ggui.Padding(t, 4, 0, 0, 0)
	case *ast.Paragraph, *ast.TextBlock:
		if pics, ok := r.picturesOnly(n); ok {
			return r.pictures(pics, false)
		}
		return r.paragraph(n, mdBodySize)
	case *ast.ThematicBreak:
		return hline()
	case *ast.FencedCodeBlock:
		return r.code(n, string(n.Language(r.src)))
	case *ast.CodeBlock:
		return r.code(n, "")
	case *ast.HTMLBlock:
		return r.html(r.htmlBlockText(n))
	case *ast.Blockquote:
		return r.quote(n)
	case *ast.List:
		return r.list(n)
	case *east.Table:
		return r.table(n)
	}
	if n.HasChildren() {
		return ggui.Column(r.blocks(n)...).Gap(10).Stretch()
	}
	return nil
}

// mdPicture is an image a page shows: where it is, what it shows, the
// link it opens, and the size its tag asks for, 0 for its own.
type mdPicture struct {
	dest, alt, link string
	width, height   float64
}

// picturesOnly is a paragraph's images when it holds nothing else but the
// links around them and the space between, as a README's row of badges:
// they show as images, not their alt text.
func (r *mdRenderer) picturesOnly(n ast.Node) ([]mdPicture, bool) {
	var pics []mdPicture
	var walk func(n ast.Node, link string) bool
	walk = func(n ast.Node, link string) bool {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Image:
				pics = append(pics, mdPicture{dest: string(c.Destination), alt: r.altText(c), link: link})
			case *ast.Link:
				if !walk(c, string(c.Destination)) {
					return false
				}
			case *ast.Text:
				if strings.TrimSpace(string(c.Value(r.src))) != "" {
					return false
				}
			case *ast.RawHTML:
				tag := r.segmentsText(c.Segments)
				name, closing, _ := htmlTagName(tag)
				switch {
				case strings.HasPrefix(strings.ToLower(tag), "<img"):
					pics = append(pics, imgPicture(tag, link))
				case name == "a" && !closing:
					link = attr(tag, "href")
				case name == "a":
					link = ""
				case name != "br" && name != "p":
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return pics, walk(n, "") && len(pics) > 0
}

// imgPicture is the image an <img> tag shows, inside the link link.
func imgPicture(tag, link string) mdPicture {
	src, alt := imgAttrs(tag)
	w, _ := strconv.ParseFloat(strings.TrimSuffix(attr(tag, "width"), "px"), 64)
	h, _ := strconv.ParseFloat(strings.TrimSuffix(attr(tag, "height"), "px"), 64)
	return mdPicture{dest: src, alt: alt, link: link, width: w, height: h}
}

// pictures shows images in a row that wraps, each opening its link when
// it has one; centred as a tag that says so has them.
func (r *mdRenderer) pictures(pics []mdPicture, center bool) ggui.Widget {
	items := make([]ggui.Widget, len(pics))
	for i, p := range pics {
		items[i] = r.image(p)
		if p.link != "" {
			dest := p.link
			items[i] = ui.Tooltip(ggui.Tap(items[i], func() { r.open(dest) }).Cursor(ggui.CursorShapePointer), dest)
		}
	}
	var w ggui.Widget = items[0]
	if len(items) > 1 {
		w = ggui.Wrap(items...).Gap(6).RunGap(6)
	}
	if center {
		return ggui.Align(w).At(0.5, 0)
	}
	return w
}

// paragraph is n's inline content as one paragraph of text at size, which
// the reader can select and copy.
func (r *mdRenderer) paragraph(n ast.Node, size float64) *ggui.RichTextWidget {
	var spans []ggui.TextSpan
	r.inline(n, mdStyle{r: r, size: size}, &spans)
	return ggui.RichText(spans...).Size(size).LineHeight(1.55).Selectable()
}

// alerts are the kinds of GitHub's alert quotes, "> [!NOTE]", and their
// colours.
var alerts = map[string]struct {
	Title string
	Color color.Color
}{
	"NOTE":      {"Note", color.RGBA{0x44, 0x93, 0xf8, 0xff}},
	"TIP":       {"Tip", color.RGBA{0x3f, 0xb9, 0x50, 0xff}},
	"IMPORTANT": {"Important", color.RGBA{0xab, 0x7d, 0xf8, 0xff}},
	"WARNING":   {"Warning", color.RGBA{0xd2, 0x99, 0x22, 0xff}},
	"CAUTION":   {"Caution", color.RGBA{0xf8, 0x51, 0x49, 0xff}},
}

// quote is a block quote, or an alert where its first line names one.
func (r *mdRenderer) quote(n *ast.Blockquote) ggui.Widget {
	bar := color.Color(skin.Border)
	var body []ggui.Widget
	if p, ok := n.FirstChild().(*ast.Paragraph); ok && p.Lines().Len() > 0 {
		first := p.Lines().At(0)
		line := strings.TrimSpace(string(first.Value(r.src)))
		if kind, ok := strings.CutPrefix(line, "[!"); ok {
			if a, ok := alerts[strings.ToUpper(strings.TrimSuffix(kind, "]"))]; ok && strings.HasSuffix(kind, "]") {
				bar = a.Color
				body = append(body, ggui.Row(icon("info").Size(15).Color(a.Color),
					ggui.Text(T(a.Title)).Size(mdBodySize).Weight(ggui.WeightSemibold).Color(a.Color)).Gap(8))
				// The rest of the paragraph, after the line naming it.
				var spans []ggui.TextSpan
				r.inlineAfter(p, first.Stop, &spans)
				if len(spans) > 0 {
					body = append(body, ggui.RichText(spans...).Size(mdBodySize).LineHeight(1.55).Selectable())
				}
				for c := p.NextSibling(); c != nil; c = c.NextSibling() {
					if w := r.block(c); w != nil {
						body = append(body, w)
					}
				}
			}
		}
	}
	if body == nil {
		body = r.blocks(n)
	}
	return &quoteBar{child: ggui.Column(body...).Gap(10).Stretch(), color: bar}
}

// quoteBar draws a bar down the left of its child, as tall as it is.
type quoteBar struct {
	child           ggui.Widget
	color, resolved color.Color
	size            ggui.Size
}

const quoteIndent = 17 // the bar and the gap after it

func (q *quoteBar) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	q.resolved = ggui.ResolveColor(q.color, env)
	q.size = q.child.Layout(ggui.Loose(ggui.Sz(max(c.MaxW-quoteIndent, 0), c.MaxH)), env)
	return c.Constrain(ggui.Sz(q.size.W+quoteIndent, q.size.H))
}

func (q *quoteBar) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.FillRoundRect(ggui.Rct(r.Origin, ggui.Sz(3, r.Size.H)), 1.5, q.resolved)
	dst.Paint(q.child, ggui.Rct(ggui.Pt(r.Origin.X+quoteIndent, r.Origin.Y), q.size))
}

// inlineAfter is inline for the children of n that start at or after
// the source offset from.
func (r *mdRenderer) inlineAfter(n ast.Node, from int, out *[]ggui.TextSpan) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); !ok || t.Segment.Start >= from {
			r.inlineNode(c, mdStyle{r: r, size: mdBodySize}, out)
		}
	}
}

// html is a block of HTML as GitHub shows what it allows: its images,
// each opening the link around it, then its text without the tags,
// centred where the block asks to be, as a README's header is.
func (r *mdRenderer) html(s string) ggui.Widget {
	s = htmlComment.ReplaceAllString(s, "")
	center := alignCenter.MatchString(s)
	var pics []mdPicture
	link := ""
	for _, tag := range htmlTag.FindAllString(s, -1) {
		name, closing, _ := htmlTagName(tag)
		switch {
		case strings.HasPrefix(strings.ToLower(tag), "<img"):
			if p := imgPicture(tag, link); p.dest != "" {
				pics = append(pics, p)
			}
		case name == "a" && !closing:
			link = attr(tag, "href")
		case name == "a":
			link = ""
		}
	}
	var out []ggui.Widget
	if len(pics) > 0 {
		out = append(out, r.pictures(pics, center))
	}
	text := strings.Join(strings.Fields(htmlTag.ReplaceAllString(s, " ")), " ")
	if text != "" {
		var t ggui.Widget = ggui.Text(html.UnescapeString(text)).Size(mdBodySize).LineHeight(1.55).Selectable()
		if center {
			t = ggui.Align(t).At(0.5, 0)
		}
		out = append(out, t)
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	return ggui.Column(out...).Gap(8).Stretch()
}

var (
	alignCenter = regexp.MustCompile(`(?i)align\s*=\s*["']?center`)
	htmlTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlAttr    = regexp.MustCompile(`(?i)\b([a-z-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// attr is the value of an HTML tag's attribute.
func attr(tag, name string) string {
	for _, m := range htmlAttr.FindAllStringSubmatch(tag, -1) {
		if strings.EqualFold(m[1], name) {
			return html.UnescapeString(m[2] + m[3] + m[4])
		}
	}
	return ""
}

// imgAttrs are an <img> tag's source and description, or its file's name.
func imgAttrs(tag string) (src, alt string) {
	src, alt = attr(tag, "src"), attr(tag, "alt")
	if alt == "" {
		alt = path.Base(src)
	}
	return src, alt
}

// segmentsText is the source of segments, joined.
func (r *mdRenderer) segmentsText(segs *text.Segments) string {
	var b strings.Builder
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		b.Write(seg.Value(r.src))
	}
	return b.String()
}

// mdStyle is what a span of inline text is drawn in: the weight and colour
// its emphasis gives it, whether it is italic, code or struck out, the
// paragraph's size, which code is set a little under, and the link it is
// part of.
type mdStyle struct {
	text                 ggui.TextStyle
	italic, code, strike bool
	size                 float64
	link                 string // where a click goes, or ""
	r                    *mdRenderer
}

// mdCodeScale is inline code's size, of its paragraph's: monospace runs
// larger than the text around it.
const mdCodeScale = 0.88

func (st mdStyle) span(s string) ggui.TextSpan {
	ts := st.text
	sans, mono := ggui.SystemItalicFonts()
	switch {
	case st.code:
		ts.Font, ts.Size = ggui.DefaultMonoFont(), cmp.Or(st.size, mdBodySize)*mdCodeScale
		if st.italic && mono != nil {
			ts.Font = mono
		}
	case st.italic && sans != nil:
		ts.Font = sans
	case st.italic && ts.Weight == 0:
		ts.Weight = ggui.WeightMedium // where the platform has no italic
	}
	sp := ggui.Span(s).Style(ts)
	if st.code {
		sp = sp.Background(skin.Surface2)
	}
	if st.strike {
		sp = sp.Strike()
	}
	if st.link != "" {
		dest, r := st.link, st.r
		sp = sp.Color(skin.AccentBright).Underline().OnTap(func() { r.open(dest) }).Name(dest)
	}
	return sp
}

func (st mdStyle) with(ts ggui.TextStyle) mdStyle { st.text = st.text.Merge(ts); return st }

// tagged is style inside the inline HTML tag name, opened by tag: bold,
// italic, code, struck out or a link; the same for a tag of none of these,
// whose text shows as it is.
func (st mdStyle) tagged(name, tag string) mdStyle {
	switch name {
	case "b", "strong":
		return st.with(ggui.TextStyle{Weight: ggui.WeightBold})
	case "i", "em", "cite", "var":
		st.italic = true
	case "code", "kbd", "samp", "tt":
		st.code = true
	case "s", "del", "strike":
		st.strike = true
	case "a":
		if href := attr(tag, "href"); href != "" {
			st.link = href
		}
	}
	return st
}

// htmlTagName is the name of an inline HTML tag, lower case, and whether
// it closes one; ok is false for what is not an opening or closing tag,
// as a comment, and for one that closes itself, as <br/>.
func htmlTagName(tag string) (name string, closing, ok bool) {
	m := tagName.FindStringSubmatch(tag)
	if m == nil || strings.HasSuffix(tag, "/>") {
		return "", false, false
	}
	return strings.ToLower(m[2]), m[1] == "/", true
}

var tagName = regexp.MustCompile(`^<(/?)([A-Za-z][A-Za-z0-9]*)\b`)

// inline appends the spans of n's inline children, each in style with
// its own laid on. An inline HTML tag, as <b> or <kbd>, styles what
// follows it up to the tag that closes it.
func (r *mdRenderer) inline(n ast.Node, style mdStyle, out *[]ggui.TextSpan) {
	var open []mdStyle // the styles the tags open replaced
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if raw, ok := c.(*ast.RawHTML); ok {
			tag := r.segmentsText(raw.Segments)
			if name, closing, ok := htmlTagName(tag); ok && name != "br" && name != "img" {
				switch {
				case !closing:
					open = append(open, style)
					style = style.tagged(name, tag)
				case len(open) > 0:
					style, open = open[len(open)-1], open[:len(open)-1]
				}
				continue
			}
		}
		r.inlineNode(c, style, out)
	}
}

// inlineNode appends the spans of one inline node.
func (r *mdRenderer) inlineNode(c ast.Node, style mdStyle, out *[]ggui.TextSpan) {
	add := func(s string, st mdStyle) {
		if s != "" {
			*out = append(*out, st.span(s))
		}
	}
	switch c := c.(type) {
	case *ast.Text:
		add(string(c.Value(r.src)), style)
		switch {
		case c.HardLineBreak():
			add("\n", style)
		case c.SoftLineBreak():
			add(" ", style)
		}
	case *ast.String:
		add(string(c.Value), style)
	case *ast.CodeSpan:
		var b strings.Builder
		for t := c.FirstChild(); t != nil; t = t.NextSibling() {
			if t, ok := t.(*ast.Text); ok {
				b.Write(t.Value(r.src))
			}
		}
		code := style
		code.code = true
		add(b.String(), code)
	case *ast.Emphasis:
		// *one* is italic, **two** bold, ***three*** both.
		if c.Level >= 2 {
			r.inline(c, style.with(ggui.TextStyle{Weight: ggui.WeightBold}), out)
		} else {
			italic := style
			italic.italic = true
			r.inline(c, italic, out)
		}
	case *east.Strikethrough:
		struck := style
		struck.strike = true
		r.inline(c, struck, out)
	case *ast.Link:
		r.inline(c, r.linked(style, string(c.Destination)), out)
	case *ast.AutoLink:
		add(string(c.Label(r.src)), r.linked(style, string(c.URL(r.src))))
	case *ast.Image:
		// An image in a line of text is its alt text, which opens it.
		add("🖼 "+r.altText(c), r.linked(style, string(c.Destination)))
	case *ast.RawHTML:
		// Of the tags, a line break breaks the line and an image is
		// its alt text; the rest only mark up text that shows anyway.
		tag := r.segmentsText(c.Segments)
		switch lower := strings.ToLower(tag); {
		case strings.HasPrefix(lower, "<br"):
			add("\n", style)
		case strings.HasPrefix(lower, "<img"):
			src, alt := imgAttrs(tag)
			add("🖼 "+alt, r.linked(style, src))
		}
	case *east.TaskCheckBox:
		mark := "☐ "
		if c.IsChecked {
			mark = "☑ "
		}
		add(mark, style.with(ggui.TextStyle{Color: skin.Muted}))
	default:
		r.inline(c, style, out)
	}
}

// altText is an image's description, or its file's name.
func (r *mdRenderer) altText(n *ast.Image) string {
	var alt []ggui.TextSpan
	r.inline(n, mdStyle{r: r}, &alt)
	var b strings.Builder
	for _, s := range alt {
		b.WriteString(s.Text())
	}
	if b.Len() == 0 {
		return path.Base(string(n.Destination))
	}
	return b.String()
}

// linked is style inside a link to dest.
func (r *mdRenderer) linked(style mdStyle, dest string) mdStyle {
	style.link = dest
	return style
}

// open follows a link: a web address in the browser, a file beside this
// one in a tab of its own.
func (r *mdRenderer) open(dest string) {
	switch {
	case dest == "" || strings.HasPrefix(dest, "#"):
		return
	case strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:"):
		r.m.openWeb(r.doc.Node, dest) // a server a file on another node names, as a README's
		return
	}
	if r.doc.Path == "" {
		return // not a file's, with nothing beside it
	}
	if i := strings.IndexAny(dest, "#?"); i >= 0 {
		dest = dest[:i]
	}
	p := r.beside(dest)
	r.m.openItem(fileItem{Key: dirKey(r.doc.Node, p), Node: r.doc.Node, Path: p, Name: baseName(p)}, 0)
}

// beside is a relative path from the file's folder, on its node.
func (r *mdRenderer) beside(rel string) string {
	return resolvePath(parentDir(r.doc.Path), rel)
}

// code is a code block, highlighted in its language when there is one,
// selectable, and scrolling sideways rather than wrapping.
func (r *mdRenderer) code(n ast.Node, lang string) ggui.Widget {
	if f := strings.Fields(lang); len(f) > 0 {
		lang = f[0] // the info string's first word
	} else {
		lang = ""
	}
	var l language
	ok := false
	if lang != "" {
		l, ok = r.m.hl.languageNamed(lang)
	}
	text := codeText(r.m, r.segmentsText(n.Lines()), l, ok, ggui.Peek(r.m.Prefs).FontSize)
	return ggui.Box(ggui.Scroll(ggui.Padding(text, 12, 14)).Horizontal().Bar(skin.Surface3)).
		Fill(skin.Surface1).Border(1, skin.Border).Radius(8)
}

// codeText is src as selectable lines of code, highlighted in l when has
// is true, its tabs stopping as in the editor.
func codeText(m *model, src string, l language, has bool, size float64) *ggui.RichTextWidget {
	tab := defaultTab
	var hl *docSyntax
	ready := ggui.State(0) // counts the highlights the worker has found
	if has {
		hl = m.syntaxFor(l)
		hl.wake = func() { m.post(func() { ready.Set(ggui.Peek(ready) + 1) }) }
		tab = pick(l.TabSize > 0, l.TabSize, tab)
	}
	src = strings.TrimSuffix(expandTabs(src, tab), "\n")
	spans := ggui.Derived(func() []ggui.TextSpan {
		ready.Get()
		if hl == nil {
			return []ggui.TextSpan{ggui.Span(src)}
		}
		return colorSpans(src, hl.colors(src, m.themePalette()))
	})
	return ggui.RichText().BindSpans(spans).NoWrap().Selectable().
		Font(ggui.DefaultMonoFont()).Size(size).LineHeight(max(ggui.Peek(m.Prefs).LineHeight, 1.2))
}

// colorSpans is src as spans coloured by styles, in order, and the text
// between them in the paragraph's colour.
func colorSpans(src string, styles []styleSpan) []ggui.TextSpan {
	var out []ggui.TextSpan
	at := 0
	for _, s := range styles {
		start, end := max(s.Start, at), min(s.End, len(src))
		if start >= end {
			continue
		}
		if start > at {
			out = append(out, ggui.Span(src[at:start]))
		}
		out = append(out, ggui.Span(src[start:end]).Color(s.Color))
		at = end
	}
	if at < len(src) {
		out = append(out, ggui.Span(src[at:]))
	}
	return out
}

// expandTabs puts spaces in place of tabs, stopping every tab columns.
func expandTabs(s string, tab int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			n := tab - col%tab
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case '\n':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col += runeCells(r)
		}
	}
	return b.String()
}

// listMarks are a list's bullets, by how deep it is nested.
var listMarks = [...]string{"•", "◦", "▪"}

func (r *mdRenderer) list(n *ast.List) ggui.Widget {
	var rows []ggui.Widget
	num := n.Start
	r.depth++
	defer func() { r.depth-- }()
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		marker := listMarks[(r.depth-1)%len(listMarks)]
		if n.IsOrdered() {
			marker = strconv.Itoa(num) + "."
			num++
		}
		var mark ggui.Widget = ggui.Box(muted(ggui.Text(marker).Size(mdBodySize))).Width(22)
		if isTask(item) {
			mark = ggui.Box().Width(4) // the box is the item's first span
		}
		rows = append(rows, ggui.Row(
			mark,
			ggui.Expanded(ggui.Column(r.blocks(item)...).Gap(6).Stretch()),
		).Align(ggui.AlignStart).Stretch())
	}
	gap := 4.0
	if !n.IsTight {
		gap = 10
	}
	return ggui.Padding(ggui.Column(rows...).Gap(gap).Stretch(), 0, 0, 0, 4)
}

// isTask reports whether a list item starts with a task's checkbox.
func isTask(item ast.Node) bool {
	if p := item.FirstChild(); p != nil {
		_, ok := p.FirstChild().(*east.TaskCheckBox)
		return ok
	}
	return false
}

func (r *mdRenderer) table(t *east.Table) ggui.Widget {
	var rows []ggui.Widget
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		_, head := row.(*east.TableHeader)
		var cells []ggui.Widget
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			p := r.paragraph(cell, 13.5)
			if head {
				p.Weight(ggui.WeightSemibold)
			}
			aligned := ggui.Align(p).At(0, 0)
			switch cell.(*east.TableCell).Alignment {
			case east.AlignRight:
				aligned = ggui.Align(p).At(1, 0)
			case east.AlignCenter:
				aligned = ggui.Align(p).At(0.5, 0)
			}
			if len(cells) > 0 {
				cells = append(cells, vline())
			}
			cells = append(cells, ggui.Expanded(ggui.Box(aligned).Pad(7, 12)))
		}
		var w ggui.Widget = ggui.Row(cells...).Stretch()
		if head {
			w = ggui.Box(w).Fill(skin.Surface1)
		}
		if len(rows) > 0 {
			rows = append(rows, hline())
		}
		rows = append(rows, w)
	}
	return ggui.Box(ggui.Column(rows...).Stretch()).Border(1, skin.Border).Radius(6)
}

// mdImage is an image a Markdown file shows, as read: Size is how large
// it is drawn at its own size, which an SVG's pixels are twice of.
type mdImage struct {
	Loading bool
	Img     *ggfx.Image // nil for one that could not be read
	Size    ggui.Size
	// Labels are an SVG's text, set over Img; Origin is its view box's
	// corner, which they are placed from.
	Labels []svgText
	Origin ggui.Point
}
