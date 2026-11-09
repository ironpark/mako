package mdview

import (
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// A page's footnotes show where they are referred to as their numbers,
// raised, and together at its end, under a rule, in smaller type.

// footnoteSize is the size of a footnote's text.
const footnoteSize = mdBodySize - 2

// footnotes is the list of a page's footnotes.
func (r *mdRenderer) footnotes(n *east.FootnoteList) ggui.Widget {
	rows := []ggui.Widget{hline()}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		note, ok := c.(*east.Footnote)
		if !ok {
			continue
		}
		var body []ggui.Widget
		for b := note.FirstChild(); b != nil; b = b.NextSibling() {
			switch b.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				body = append(body, r.paragraph(b, footnoteSize))
			default:
				if w := r.block(b); w != nil {
					body = append(body, w)
				}
			}
		}
		mark := ggui.Box(muted(ggui.Text(strconv.Itoa(note.Index) + ".").Size(footnoteSize))).Width(22)
		rows = append(rows, ggui.Row(mark, ggui.Expanded(ggui.Column(body...).Gap(6).Stretch())).Align(ggui.AlignStart))
	}
	return ggui.Column(rows...).Gap(8).Stretch()
}

// superscript is n in raised digits, as a footnote's reference shows.
func superscript(n int) string {
	const raised = "⁰¹²³⁴⁵⁶⁷⁸⁹"
	digits := []rune(raised)
	var b strings.Builder
	for _, d := range strconv.Itoa(n) {
		b.WriteRune(digits[d-'0'])
	}
	return b.String()
}
