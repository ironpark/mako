package mdview

import (
	"html"
	"regexp"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/yuin/goldmark/ast"
)

// <details> folds what it holds away under its <summary>, as on GitHub.
// Markdown inside it is parted from the tags by blank lines, which makes
// the tags HTML blocks of their own around the Markdown's blocks:
//
//	<details>
//	<summary>More</summary>
//
//	Markdown
//
//	</details>
//
// so a details is the blocks from the one that opens it to the one that
// closes it.

var (
	detailsOpen  = regexp.MustCompile(`(?i)<details\b[^>]*>`)
	detailsClose = regexp.MustCompile(`(?i)</details\s*>`)
	summaryTag   = regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary\s*>`)
	openAttr     = regexp.MustCompile(`(?i)\sopen\b`)
)

// blockList renders blocks, a details and what it holds as one.
func (r *mdRenderer) blockList(nodes []ast.Node) []ggui.Widget {
	var out []ggui.Widget
	add := func(w ggui.Widget) {
		if w != nil {
			out = append(out, w)
		}
	}
	for i := 0; i < len(nodes); i++ {
		hb, ok := nodes[i].(*ast.HTMLBlock)
		if !ok {
			add(r.block(nodes[i]))
			continue
		}
		s := r.htmlBlockText(hb)
		open := detailsOpen.FindStringIndex(s)
		if open == nil {
			add(r.block(hb))
			continue
		}
		add(r.html(s[:open[0]]))
		head := s[open[0]:]
		depth := len(detailsOpen.FindAllString(head, -1)) - len(detailsClose.FindAllString(head, -1))
		var body []ast.Node
		tail := ""
		closed := depth <= 0
		j := i + 1
		for ; !closed && j < len(nodes); j++ {
			if hb, ok := nodes[j].(*ast.HTMLBlock); ok {
				t := r.htmlBlockText(hb)
				depth += len(detailsOpen.FindAllString(t, -1)) - len(detailsClose.FindAllString(t, -1))
				if depth <= 0 {
					tail = t
					break
				}
			}
			body = append(body, nodes[j])
		}
		if closed {
			j = i // nothing past the block that opens it
		}
		i = j
		if closed {
			// It closes in the block that opens it.
			if end := detailsClose.FindAllStringIndex(head, -1); len(end) > 0 {
				last := end[len(end)-1]
				head, tail = head[:last[0]], head[last[0]:]
			}
		}
		add(r.details(head, body, tail))
	}
	return out
}

// details is a fold: head is from its tag to the Markdown it holds, body
// that Markdown, tail from the end of it to past the closing tag.
func (r *mdRenderer) details(head string, body []ast.Node, tail string) ggui.Widget {
	tag := detailsOpen.FindString(head)
	head = head[len(tag):]
	title := r.host.Text("Details")
	if m := summaryTag.FindStringSubmatchIndex(head); m != nil {
		inner := htmlTag.ReplaceAllString(head[m[2]:m[3]], " ")
		if t := strings.Join(strings.Fields(html.UnescapeString(inner)), " "); t != "" {
			title = t
		}
		head = head[:m[0]] + head[m[1]:]
	}
	var content []ggui.Widget
	if w := r.html(head); w != nil {
		content = append(content, w)
	}
	content = append(content, r.blockList(body)...)
	before, after := tail, ""
	if end := detailsClose.FindStringIndex(tail); end != nil {
		before, after = tail[:end[0]], tail[end[1]:]
	}
	if w := r.html(before); w != nil {
		content = append(content, w)
	}
	fold := ui.Collapsible(r.fold(openAttr.MatchString(tag)), title,
		ggui.Padding(ggui.Column(content...).Gap(10).Stretch(), 6, 0, 0, 18))
	if w := r.html(after); w != nil {
		return ggui.Column(fold, w).Gap(14).Stretch()
	}
	return fold
}

// fold is whether the page's next details is open, as the host keeps it.
func (r *mdRenderer) fold(open bool) *ggui.StateValue[bool] {
	n := r.folds
	r.folds++
	return r.host.Fold(n, open)
}

// htmlBlockText is an HTML block's source, its closing line with it.
func (r *mdRenderer) htmlBlockText(n *ast.HTMLBlock) string {
	s := r.segmentsText(n.Lines())
	if n.HasClosure() {
		s += string(n.ClosureLine.Value(r.src))
	}
	return s
}
