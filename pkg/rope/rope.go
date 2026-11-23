package rope

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	minChunk = 512
	maxChunk = 2048

	minChildren = 2
	maxChildren = 8
)

// node is an element in the SumTree: either a leafNode or an internalNode.
type node interface {
	summary() Summary
	height() int
	leaf() bool
}

// leafNode contains a raw slice of text and its Summary.
type leafNode struct {
	text string
	summ Summary
}

func newLeaf(text string) *leafNode {
	return &leafNode{text: text, summ: computeSummary(text)}
}

func (l *leafNode) summary() Summary { return l.summ }
func (l *leafNode) height() int      { return 0 }
func (l *leafNode) leaf() bool       { return true }

// internalNode points to child subtrees and caches their aggregated Summary.
type internalNode struct {
	children []node
	summ     Summary
	h        int
}

func newInternal(children []node) *internalNode {
	var summ Summary
	for _, c := range children {
		summ = summ.Add(c.summary())
	}
	return &internalNode{
		children: children,
		summ:     summ,
		h:        children[0].height() + 1,
	}
}

func (in *internalNode) summary() Summary { return in.summ }
func (in *internalNode) height() int      { return in.h }
func (in *internalNode) leaf() bool       { return false }

// Rope is a fast text buffer backed by a balanced SumTree.
// All operations that read or seek lines, points, and byte offsets run in
// O(log N) or O(1) time.
type Rope struct {
	root node
}

// New builds a balanced Rope from a string.
func New(s string) *Rope {
	if len(s) <= maxChunk {
		return &Rope{root: newLeaf(s)}
	}

	// Split into chunks, preferring newline boundaries.
	var leaves []node
	for len(s) > 0 {
		if len(s) <= maxChunk {
			leaves = append(leaves, newLeaf(s))
			break
		}
		cut := maxChunk
		nl := strings.LastIndexByte(s[minChunk:maxChunk], '\n')
		if nl >= 0 {
			cut = minChunk + nl + 1
		} else {
			// Ensure we do not split in the middle of a multi-byte UTF-8 rune.
			for cut > minChunk && !utf8.RuneStart(s[cut]) {
				cut--
			}
		}
		leaves = append(leaves, newLeaf(s[:cut]))
		s = s[cut:]
	}

	// Bottom-up construction of the balanced SumTree.
	nodes := leaves
	for len(nodes) > 1 {
		var next []node
		for len(nodes) > 0 {
			k := min(len(nodes), maxChildren)
			// Avoid leaving a lone remainder (< minChildren) if possible.
			rem := len(nodes) - k
			if rem > 0 && rem < minChildren {
				k = len(nodes) / 2
			}
			next = append(next, newInternal(nodes[:k]))
			nodes = nodes[k:]
		}
		nodes = next
	}

	return &Rope{root: nodes[0]}
}

// Len returns the total byte length of the text.
func (r *Rope) Len() int {
	if r == nil || r.root == nil {
		return 0
	}
	return r.root.summary().Bytes
}

// Lines returns the total number of lines in the text (1 for an empty string,
// newline count + 1 in general).
func (r *Rope) Lines() int {
	if r == nil || r.root == nil {
		return 1
	}
	return r.root.summary().Lines + 1
}

// LongestLine returns the length of the longest line in bytes.
func (r *Rope) LongestLine() int {
	if r == nil || r.root == nil {
		return 0
	}
	return r.root.summary().LongestLine
}

// LineStart returns the byte offset where row (0-indexed) begins.
// If row <= 0, it returns 0. If row exceeds the last line, it returns Len().
func (r *Rope) LineStart(row int) int {
	if r == nil || r.root == nil || row <= 0 {
		return 0
	}
	if row > r.root.summary().Lines {
		return r.root.summary().Bytes
	}
	return lineStartInNode(r.root, row)
}

func lineStartInNode(n node, row int) int {
	if n.leaf() {
		l := n.(*leafNode)
		s := l.text
		count := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				count++
				if count == row {
					return i + 1
				}
			}
		}
		return len(s)
	}

	in := n.(*internalNode)
	accumBytes := 0
	for _, c := range in.children {
		lines := c.summary().Lines
		if row <= lines {
			return accumBytes + lineStartInNode(c, row)
		}
		row -= lines
		accumBytes += c.summary().Bytes
	}
	return accumBytes
}

// LineEnd returns the byte offset where row (0-indexed) ends, excluding the
// terminating newline (or the end of the text if it is the last line).
func (r *Rope) LineEnd(row int) int {
	if r == nil || r.root == nil || row < 0 {
		return 0
	}
	totalLines := r.root.summary().Lines
	if row >= totalLines {
		return r.root.summary().Bytes
	}
	return r.LineStart(row+1) - 1
}

// LineOf returns the row (0-indexed) containing byte offset.
func (r *Rope) LineOf(offset int) int {
	if r == nil || r.root == nil || offset <= 0 {
		return 0
	}
	if offset >= r.root.summary().Bytes {
		return r.root.summary().Lines
	}
	return lineOfInNode(r.root, offset)
}

func lineOfInNode(n node, offset int) int {
	if n.leaf() {
		l := n.(*leafNode)
		limit := min(offset, len(l.text))
		return strings.Count(l.text[:limit], "\n")
	}

	in := n.(*internalNode)
	accumLines := 0
	for _, c := range in.children {
		bytes := c.summary().Bytes
		if offset < bytes {
			return accumLines + lineOfInNode(c, offset)
		}
		offset -= bytes
		accumLines += c.summary().Lines
	}
	return accumLines
}

// Point represents a (Row, Col) coordinate in a text buffer.
type Point struct {
	Row int // 0-indexed line
	Col int // byte offset within the line
}

// OffsetToPoint converts a byte offset to a Point (Row, Col).
func (r *Rope) OffsetToPoint(offset int) Point {
	row := r.LineOf(offset)
	start := r.LineStart(row)
	return Point{Row: row, Col: max(offset-start, 0)}
}

// PointToOffset converts a Point (Row, Col) to a byte offset.
func (r *Rope) PointToOffset(pt Point) int {
	start := r.LineStart(pt.Row)
	end := r.LineEnd(pt.Row)
	return min(start+max(pt.Col, 0), end)
}

// Line returns the text of row (0-indexed), without trailing newline.
func (r *Rope) Line(row int) string {
	start := r.LineStart(row)
	end := r.LineEnd(row)
	if start >= end {
		return ""
	}
	return r.Slice(start, end)
}

// Slice returns the substring in the byte range [start, end).
func (r *Rope) Slice(start, end int) string {
	if r == nil || r.root == nil {
		return ""
	}
	start = max(start, 0)
	end = min(end, r.root.summary().Bytes)
	if start >= end {
		return ""
	}
	var b strings.Builder
	b.Grow(end - start)
	collectSlice(r.root, start, end, 0, &b)
	return b.String()
}

func collectSlice(n node, start, end, offset int, b *strings.Builder) {
	nodeStart := offset
	nodeEnd := offset + n.summary().Bytes
	if start >= nodeEnd || end <= nodeStart {
		return
	}
	if n.leaf() {
		l := n.(*leafNode)
		s := max(start-nodeStart, 0)
		e := min(end-nodeStart, len(l.text))
		if s < e {
			b.WriteString(l.text[s:e])
		}
		return
	}
	in := n.(*internalNode)
	childOffset := nodeStart
	for _, c := range in.children {
		cBytes := c.summary().Bytes
		if end > childOffset && start < childOffset+cBytes {
			collectSlice(c, start, end, childOffset, b)
		}
		childOffset += cBytes
	}
}

// String returns the full text content.
func (r *Rope) String() string {
	if r == nil || r.root == nil {
		return ""
	}
	return r.Slice(0, r.Len())
}

// Replace replaces the byte range [start, end) with text, returning a new Rope.
func (r *Rope) Replace(start, end int, text string) *Rope {
	if r == nil || r.root == nil {
		return New(text)
	}
	n := r.Len()
	start = min(max(start, 0), n)
	end = min(max(end, start), n)
	if start == end && text == "" {
		return r
	}
	if start == 0 && end == n {
		return New(text)
	}

	var left, right node
	if start == end {
		left, right = splitNode(r.root, start)
	} else {
		left, _ = splitNode(r.root, start)
		_, right = splitNode(r.root, end)
	}
	var mid node
	if text != "" {
		mid = New(text).root
	}
	root := concatNodes(concatNodes(left, mid), right)
	if root == nil {
		return New("")
	}
	return &Rope{root: root}
}

func fromNodes(nodes []node) node {
	switch len(nodes) {
	case 0:
		return nil
	case 1:
		return nodes[0]
	default:
		return newInternal(slices.Clone(nodes))
	}
}

func splitNode(n node, offset int) (left, right node) {
	if n == nil || offset <= 0 {
		return nil, n
	}
	if offset >= n.summary().Bytes {
		return n, nil
	}
	if n.leaf() {
		l := n.(*leafNode)
		return newLeaf(l.text[:offset]), newLeaf(l.text[offset:])
	}
	in := n.(*internalNode)
	for i, c := range in.children {
		cb := c.summary().Bytes
		if offset < cb {
			cl, cr := splitNode(c, offset)
			return concatNodes(fromNodes(in.children[:i]), cl),
				concatNodes(cr, fromNodes(in.children[i+1:]))
		}
		if offset == cb {
			return fromNodes(in.children[:i+1]), fromNodes(in.children[i+1:])
		}
		offset -= cb
	}
	return n, nil
}

func concatNodes(a, b node) node {
	if a == nil || a.summary().Bytes == 0 {
		return b
	}
	if b == nil || b.summary().Bytes == 0 {
		return a
	}

	ah, bh := a.height(), b.height()
	if ah == bh {
		if ah == 0 {
			al, bl := a.(*leafNode), b.(*leafNode)
			if len(al.text)+len(bl.text) <= maxChunk {
				return newLeaf(al.text + bl.text)
			}
			return newInternal([]node{a, b})
		}
		ai, bi := a.(*internalNode), b.(*internalNode)
		ac, bc := ai.children, bi.children
		combined := make([]node, 0, len(ac)+len(bc))
		combined = append(combined, ac...)
		if ah == 1 {
			lastA := combined[len(combined)-1].(*leafNode)
			firstB := bc[0].(*leafNode)
			if len(lastA.text)+len(firstB.text) <= maxChunk {
				combined[len(combined)-1] = newLeaf(lastA.text + firstB.text)
				bc = bc[1:]
			}
		}
		combined = append(combined, bc...)
		return joinChildren(combined)
	}

	if ah > bh {
		ai := a.(*internalNode)
		ac := ai.children
		merged := concatNodes(ac[len(ac)-1], b)
		if merged.height() == ah-1 {
			children := make([]node, len(ac))
			copy(children, ac[:len(ac)-1])
			children[len(ac)-1] = merged
			return newInternal(children)
		}
		mc := merged.(*internalNode).children
		combined := make([]node, 0, len(ac)-1+len(mc))
		combined = append(combined, ac[:len(ac)-1]...)
		combined = append(combined, mc...)
		return joinChildren(combined)
	}

	bi := b.(*internalNode)
	bc := bi.children
	merged := concatNodes(a, bc[0])
	if merged.height() == bh-1 {
		children := make([]node, len(bc))
		children[0] = merged
		copy(children[1:], bc[1:])
		return newInternal(children)
	}
	mc := merged.(*internalNode).children
	combined := make([]node, 0, len(mc)+len(bc)-1)
	combined = append(combined, mc...)
	combined = append(combined, bc[1:]...)
	return joinChildren(combined)
}

func joinChildren(combined []node) node {
	if len(combined) <= maxChildren {
		return newInternal(combined)
	}
	mid := len(combined) / 2
	return newInternal([]node{
		newInternal(slices.Clone(combined[:mid])),
		newInternal(slices.Clone(combined[mid:])),
	})
}

// Insert inserts text at byte offset.
func (r *Rope) Insert(offset int, text string) *Rope {
	return r.Replace(offset, offset, text)
}

// Delete deletes the byte range [start, end).
func (r *Rope) Delete(start, end int) *Rope {
	return r.Replace(start, end, "")
}
