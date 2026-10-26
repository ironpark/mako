package gui

import (
	"html"
	"image/color"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
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
	m   *model
	id  string // the file's tab
	doc docInfo
	src []byte
}

// markdownView is a Markdown file rendered, scrolling.
func markdownView(m *model, id string, src string) ggui.Widget {
	r := &mdRenderer{m: m, id: id, doc: ggui.Peek(m.Docs)[id], src: []byte(src)}
	root := markdown.Parser().Parse(text.NewReader(r.src))
	page := ggui.Box(ggui.Column(r.blocks(root)...).Gap(14).Stretch()).MaxWidth(880)
	return ggui.Box(ggui.Scroll(ggui.Align(ggui.Padding(page, 28, 36, 48, 36)).Top()).Bar(tokSurface3)).Fill(tokSurface0)
}

// blocks renders the block children of n.
func (r *mdRenderer) blocks(n ast.Node) []ggui.Widget {
	var out []ggui.Widget
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if w := r.block(c); w != nil {
			out = append(out, w)
		}
	}
	return out
}

func (r *mdRenderer) block(n ast.Node) ggui.Widget {
	switch n := n.(type) {
	case *ast.Heading:
		sizes := [...]float64{0, 26, 21, 17.5, 15.5, 14, 13.5}
		t := r.paragraph(n).Size(sizes[min(n.Level, 6)]).Weight(ggui.WeightSemibold).LineHeight(1.3)
		if n.Level <= 2 {
			return ggui.Column(ggui.Padding(t, 6, 0, 0, 0), hline()).Gap(8).Stretch()
		}
		return ggui.Padding(t, 4, 0, 0, 0)
	case *ast.Paragraph, *ast.TextBlock:
		if img, ok := onlyImage(n); ok {
			return r.image(string(img.Destination), r.altText(img))
		}
		return r.paragraph(n)
	case *ast.ThematicBreak:
		return hline()
	case *ast.FencedCodeBlock:
		return r.code(n, string(n.Language(r.src)))
	case *ast.CodeBlock:
		return r.code(n, "")
	case *ast.HTMLBlock:
		s := r.segmentsText(n.Lines())
		if n.HasClosure() {
			s += string(n.ClosureLine.Value(r.src))
		}
		return r.html(s)
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

// onlyImage reports whether a paragraph is just an image, which shows as
// one rather than as its alt text.
func onlyImage(n ast.Node) (*ast.Image, bool) {
	img, ok := n.FirstChild().(*ast.Image)
	return img, ok && n.FirstChild() == n.LastChild()
}

// paragraph is n's inline content as one paragraph of text, which the
// reader can select and copy.
func (r *mdRenderer) paragraph(n ast.Node) *ggui.RichTextWidget {
	var spans []ggui.TextSpan
	r.inline(n, mdStyle{r: r}, &spans)
	return ggui.RichText(spans...).Size(mdBodySize).LineHeight(1.55).Selectable()
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
	bar := color.Color(tokBorder)
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
			r.inlineNode(c, mdStyle{r: r}, out)
		}
	}
}

// html is a block of HTML as GitHub shows what it allows: its images, and
// its text without the tags.
func (r *mdRenderer) html(s string) ggui.Widget {
	var out []ggui.Widget
	for _, tag := range imgTag.FindAllString(s, -1) {
		if src, alt := imgAttrs(tag); src != "" {
			out = append(out, r.image(src, alt))
		}
	}
	s = htmlComment.ReplaceAllString(s, "")
	text := strings.Join(strings.Fields(htmlTag.ReplaceAllString(s, " ")), " ")
	if text != "" {
		out = append(out, ggui.Text(html.UnescapeString(text)).Size(mdBodySize).LineHeight(1.55).Selectable())
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	return ggui.Wrap(out...).Gap(8).RunGap(8)
}

var (
	imgTag      = regexp.MustCompile(`(?is)<img\b[^>]*>`)
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

// mdStyle is what a span of inline text is drawn in: the style its
// emphasis and code give it, and the link it is part of.
type mdStyle struct {
	text ggui.TextStyle
	link string // where a click goes, or ""
	r    *mdRenderer
}

func (st mdStyle) span(s string) ggui.TextSpan {
	sp := ggui.Span(s).Style(st.text)
	if st.link != "" {
		dest, r := st.link, st.r
		sp = sp.Color(tokAccentBright).Underline().OnTap(func() { r.open(dest) }).Name(dest)
	}
	return sp
}

func (st mdStyle) with(ts ggui.TextStyle) mdStyle { st.text = st.text.Merge(ts); return st }

// inline appends the spans of n's inline children, each in style with
// its own laid on.
func (r *mdRenderer) inline(n ast.Node, style mdStyle, out *[]ggui.TextSpan) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
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
		add(b.String(), style.with(ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: mdBodySize - 1, Color: tokAccentBright}))
	case *ast.Emphasis:
		w := ggui.WeightMedium
		if c.Level >= 2 {
			w = ggui.WeightBold
		}
		r.inline(c, style.with(ggui.TextStyle{Weight: w}), out)
	case *east.Strikethrough:
		r.inline(c, style.with(ggui.TextStyle{Color: tokExtraMuted}), out)
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
		add(mark, style.with(ggui.TextStyle{Color: tokMuted}))
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
		_ = runtime.OpenURL(dest)
		return
	}
	if i := strings.IndexAny(dest, "#?"); i >= 0 {
		dest = dest[:i]
	}
	p := r.beside(dest)
	it := fileItem{Key: dirKey(r.doc.Node, p), Node: r.doc.Node, Path: p, Name: baseName(p)}
	if isImage(it.Name) {
		r.m.openPreview(it)
		return
	}
	r.m.openFile(it)
}

// beside is a relative path from the file's folder, on its node.
func (r *mdRenderer) beside(rel string) string {
	if strings.HasPrefix(rel, "/") {
		return rel
	}
	if pathSep(r.doc.Path) == `\` {
		return filepath.Join(filepath.Dir(r.doc.Path), rel)
	}
	return path.Join(path.Dir(r.doc.Path), rel)
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
	return ggui.Box(ggui.Scroll(ggui.Padding(text, 12, 14)).Horizontal().Bar(tokSurface3)).
		Fill(tokSurface1).Border(1, tokBorder).Radius(8)
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
		return colorSpans(src, hl.colors(src, paletteFor(m.Prefs.Get().Theme, ggui.SystemDark().Get())))
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

func (r *mdRenderer) list(n *ast.List) ggui.Widget {
	var rows []ggui.Widget
	num := n.Start
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "•"
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
			p := r.paragraph(cell).Size(13.5)
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
			w = ggui.Box(w).Fill(tokSurface1)
		}
		if len(rows) > 0 {
			rows = append(rows, hline())
		}
		rows = append(rows, w)
	}
	return ggui.Box(ggui.Column(rows...).Stretch()).Border(1, tokBorder).Radius(6)
}

// image shows an image beside the file, read from its node; one from
// the web shows as a link.
func (r *mdRenderer) image(dest, label string) ggui.Widget {
	if strings.Contains(dest, "://") || !isImage(dest) {
		return ggui.RichText(r.linked(mdStyle{r: r}, dest).span("🖼 " + label)).Size(mdBodySize)
	}
	p := r.beside(dest)
	doc := r.m.docs[r.id]
	if doc == nil {
		return ggui.RichText(r.linked(mdStyle{r: r}, dest).span("🖼 " + label)).Size(mdBodySize)
	}
	if doc.images == nil {
		doc.images = map[string]*ggui.StateValue[mdImage]{}
	}
	img, ok := doc.images[p]
	if !ok {
		img = ggui.State(mdImage{Loading: true})
		doc.images[p] = img
		fetchImage(r.m, r.doc.Node, p, func(d decodedImage, err error) {
			if err != nil || d.Img == nil {
				img.Set(mdImage{})
				return
			}
			img.Set(mdImage{Img: ggfx.NewImageFromImage(d.Img)})
		})
	}
	return ggui.View(img, func(v mdImage) ggui.Widget {
		switch {
		case v.Loading:
			return ggui.Box(ui.Spinner()).Pad(8)
		case v.Img == nil:
			return muted(ggui.Text("🖼 " + label).Size(13))
		}
		return ggui.Align(ggui.Image(v.Img).Alt(label)).Left()
	})
}

// mdImage is an image a Markdown file shows, as read.
type mdImage struct {
	Loading bool
	Img     *ggfx.Image // nil for one that could not be read
}
