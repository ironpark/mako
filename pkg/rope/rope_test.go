package rope

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestEmptyRope(t *testing.T) {
	r := New("")
	if r.Len() != 0 {
		t.Fatalf("expected Len 0, got %d", r.Len())
	}
	if r.Lines() != 1 {
		t.Fatalf("expected Lines 1, got %d", r.Lines())
	}
	if r.LongestLine() != 0 {
		t.Fatalf("expected LongestLine 0, got %d", r.LongestLine())
	}
	if r.LineStart(0) != 0 || r.LineStart(1) != 0 {
		t.Fatalf("unexpected LineStart: %d, %d", r.LineStart(0), r.LineStart(1))
	}
	if r.LineEnd(0) != 0 {
		t.Fatalf("unexpected LineEnd: %d", r.LineEnd(0))
	}
	if r.Line(0) != "" {
		t.Fatalf("unexpected Line: %q", r.Line(0))
	}
	if r.Slice(0, 0) != "" {
		t.Fatalf("unexpected Slice: %q", r.Slice(0, 0))
	}
}

func TestSmallRope(t *testing.T) {
	text := "func main() {\n\tprintln(\"hello world\")\n}\n"
	r := New(text)

	if r.Len() != len(text) {
		t.Fatalf("Len = %d, want %d", r.Len(), len(text))
	}
	if r.Lines() != 4 { // 3 newlines -> 4 lines (the 4th is empty)
		t.Fatalf("Lines = %d, want 4", r.Lines())
	}

	lines := strings.Split(text, "\n")
	for i := range lines {
		line := r.Line(i)
		if line != lines[i] {
			t.Errorf("Line(%d) = %q, want %q", i, line, lines[i])
		}
	}

	for i := 0; i < len(text); i++ {
		row := r.LineOf(i)
		start := r.LineStart(row)
		end := r.LineEnd(row)
		if i < start || (row < r.Lines()-1 && i > end) {
			t.Errorf("offset %d: row %d has range [%d, %d]", i, row, start, end)
		}
	}
}

func TestLargeRopeMultiChunk(t *testing.T) {
	var sb strings.Builder
	var expectedLines []string
	maxLen := 0
	for i := range 5000 {
		line := fmt.Sprintf("line %04d: %s (some text here for padding chunk)", i, strings.Repeat("x", i%100))
		sb.WriteString(line)
		sb.WriteByte('\n')
		expectedLines = append(expectedLines, line)
		if len(line) > maxLen {
			maxLen = len(line)
		}
	}
	fullText := sb.String()
	expectedLines = append(expectedLines, "") // trailing newline produces an empty line

	r := New(fullText)
	if r.Len() != len(fullText) {
		t.Fatalf("Len = %d, want %d", r.Len(), len(fullText))
	}
	if r.Lines() != len(expectedLines) {
		t.Fatalf("Lines = %d, want %d", r.Lines(), len(expectedLines))
	}
	if r.LongestLine() != maxLen {
		t.Fatalf("LongestLine = %d, want %d", r.LongestLine(), maxLen)
	}

	// Verify height is balanced (>0 because len > maxChunk)
	if r.root.height() == 0 {
		t.Fatalf("large rope expected internal nodes, got height 0")
	}

	// Verify line start and line end for sampled rows
	offset := 0
	for i, exp := range expectedLines {
		start := r.LineStart(i)
		end := r.LineEnd(i)
		if start != offset {
			t.Fatalf("row %d: LineStart = %d, want %d", i, start, offset)
		}
		if end != offset+len(exp) {
			t.Fatalf("row %d: LineEnd = %d, want %d", i, end, offset+len(exp))
		}
		gotLine := r.Line(i)
		if gotLine != exp {
			t.Fatalf("row %d: Line = %q, want %q", i, gotLine, exp)
		}
		if len(exp) > 0 {
			midOffset := start + len(exp)/2
			if row := r.LineOf(midOffset); row != i {
				t.Fatalf("offset %d: LineOf = %d, want %d", midOffset, row, i)
			}
			pt := r.OffsetToPoint(midOffset)
			if pt.Row != i || pt.Col != len(exp)/2 {
				t.Fatalf("offset %d: OffsetToPoint = %+v, want Row %d, Col %d", midOffset, pt, i, len(exp)/2)
			}
			if off := r.PointToOffset(pt); off != midOffset {
				t.Fatalf("PointToOffset(%+v) = %d, want %d", pt, off, midOffset)
			}
		}
		offset += len(exp) + 1 // +1 for '\n'
	}
}

func TestKoreanAndMultiByteUTF8(t *testing.T) {
	text := "안녕하세요\n세계 여러분!\nGo 텍스트 에디터 rope sumtree 개선\n"
	r := New(text)

	if r.Lines() != 4 {
		t.Fatalf("Lines = %d, want 4", r.Lines())
	}
	if r.Line(0) != "안녕하세요" {
		t.Fatalf("Line(0) = %q", r.Line(0))
	}
	if r.Line(1) != "세계 여러분!" {
		t.Fatalf("Line(1) = %q", r.Line(1))
	}

	pt := r.OffsetToPoint(len("안녕하세요\n세"))
	if pt.Row != 1 {
		t.Fatalf("expected row 1, got %d", pt.Row)
	}
	if pt.Col != len("세") {
		t.Fatalf("expected col %d, got %d", len("세"), pt.Col)
	}
}

func TestRopeSpliceReplace(t *testing.T) {
	orig := "The quick brown fox jumps over the lazy dog"
	r := New(orig)

	// Replace "brown" with "red"
	start := strings.Index(orig, "brown")
	end := start + len("brown")
	r2 := r.Replace(start, end, "red")

	expected := "The quick red fox jumps over the lazy dog"
	if got := r2.String(); got != expected {
		t.Fatalf("got %q, want %q", got, expected)
	}
	if r2.Len() != len(expected) {
		t.Fatalf("Len = %d, want %d", r2.Len(), len(expected))
	}

	// Insert at beginning
	r3 := r2.Insert(0, "Look! ")
	if got := r3.String(); got != "Look! "+expected {
		t.Fatalf("got %q", got)
	}

	// Delete from end (" dog")
	r4 := r3.Delete(len(r3.String())-4, len(r3.String()))
	if got := r4.String(); got != "Look! The quick red fox jumps over the lazy" {
		t.Fatalf("got %q", got)
	}
}

func BenchmarkRopeVsLinearLineOf(b *testing.B) {
	var sb strings.Builder
	for range 20000 {
		sb.WriteString("hello world this is a sample line in a large document\n")
	}
	text := sb.String()
	r := New(text)

	b.Run("RopeLineOf", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			targetOffset := (i * 997) % len(text)
			_ = r.LineOf(targetOffset)
		}
	})

	b.Run("LinearCountLineOf", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			targetOffset := (i * 997) % len(text)
			_ = strings.Count(text[:targetOffset], "\n")
		}
	})
}

func assertInvariants(t *testing.T, r *Rope, want string) {
	t.Helper()
	if r == nil || r.root == nil {
		t.Fatal("nil rope or root")
	}
	if got := r.String(); got != want {
		t.Fatalf("String() mismatch: len=%d, want len=%d", len(got), len(want))
	}
	if r.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d", r.Len(), len(want))
	}
	wantLines := strings.Count(want, "\n") + 1
	if r.Lines() != wantLines {
		t.Fatalf("Lines() = %d, want %d", r.Lines(), wantLines)
	}

	rootHeight := r.root.height()
	var checkNode func(n node, isRoot bool) Summary
	checkNode = func(n node, isRoot bool) Summary {
		if n == nil {
			t.Fatal("nil child node")
		}
		if n.leaf() {
			if n.height() != 0 {
				t.Fatalf("leaf has non-zero height %d", n.height())
			}
			l := n.(*leafNode)
			if !isRoot && len(l.text) == 0 {
				t.Fatal("empty leaf in non-root position")
			}
			if len(l.text) > maxChunk {
				t.Fatalf("leaf len %d exceeds maxChunk %d", len(l.text), maxChunk)
			}
			wantSumm := computeSummary(l.text)
			if l.summ != wantSumm {
				t.Fatalf("leaf summary mismatch: got %+v, want %+v", l.summ, wantSumm)
			}
			return wantSumm
		}
		in := n.(*internalNode)
		if len(in.children) < minChildren || len(in.children) > maxChildren {
			t.Fatalf("internal node degree %d out of [%d, %d]", len(in.children), minChildren, maxChildren)
		}
		var agg Summary
		for i, c := range in.children {
			if c.height() != in.h-1 {
				t.Fatalf("child %d height %d != parent height %d - 1", i, c.height(), in.h)
			}
			cs := checkNode(c, false)
			agg = agg.Add(cs)
		}
		if in.summ != agg {
			t.Fatalf("internal summary mismatch: got %+v, want %+v", in.summ, agg)
		}
		return agg
	}
	totalSumm := checkNode(r.root, true)
	if totalSumm != computeSummary(want) {
		t.Fatalf("root summary %+v != computeSummary %+v", totalSumm, computeSummary(want))
	}
	if rootHeight > 0 && r.LongestLine() != totalSumm.LongestLine {
		t.Fatalf("LongestLine = %d, want %d", r.LongestLine(), totalSumm.LongestLine)
	}

	// Spot-check LineStart/LineEnd/Line/LineOf against oracle lines.
	lines := strings.Split(want, "\n")
	if len(lines) != r.Lines() {
		t.Fatalf("line count mismatch: %d vs %d", len(lines), r.Lines())
	}
	step := max(len(lines)/15, 1)
	offset := 0
	for row, line := range lines {
		if row%step == 0 || row == len(lines)-1 {
			if got := r.LineStart(row); got != offset {
				t.Fatalf("LineStart(%d) = %d, want %d", row, got, offset)
			}
			if got := r.LineEnd(row); got != offset+len(line) {
				t.Fatalf("LineEnd(%d) = %d, want %d", row, got, offset+len(line))
			}
			if got := r.Line(row); got != line {
				t.Fatalf("Line(%d) = %q, want %q", row, got, line)
			}
			if got := r.LineOf(offset); got != row {
				t.Fatalf("LineOf(%d) = %d, want %d", offset, got, row)
			}
		}
		offset += len(line) + 1
	}
}

func TestRopeReplaceStructuralInvariantsAndImmutability(t *testing.T) {
	// Nil/empty rope cases.
	var nilRope *Rope
	assertInvariants(t, nilRope.Insert(0, "abc"), "abc")
	empty := New("")
	if got := empty.Replace(0, 0, ""); got != empty {
		t.Fatal("no-op Replace on empty rope should return receiver")
	}
	assertInvariants(t, empty.Insert(10, "hello\nworld"), "hello\nworld")

	// Multi-chunk base rope.
	baseText := strings.Repeat("alpha beta gamma delta\n", 400) // ~9.2 KiB
	r0 := New(baseText)
	assertInvariants(t, r0, baseText)

	// Insert multi-chunk payload in the middle.
	bigInsert := strings.Repeat("INSERTED_LINE\n", 250)
	mid := len(baseText) / 2
	r1 := r0.Insert(mid, bigInsert)
	want1 := baseText[:mid] + bigInsert + baseText[mid:]
	assertInvariants(t, r1, want1)
	// Original rope remains completely unmodified.
	assertInvariants(t, r0, baseText)

	// Delete across multiple internal/leaf boundaries.
	delStart, delEnd := mid-1500, mid+len(bigInsert)+1500
	r2 := r1.Delete(delStart, delEnd)
	want2 := want1[:delStart] + want1[delEnd:]
	assertInvariants(t, r2, want2)
	assertInvariants(t, r1, want1)

	// Replace with out-of-bounds clamping and full replacement.
	r3 := r2.Replace(-10, 5, "HEAD:")
	want3 := "HEAD:" + want2[5:]
	assertInvariants(t, r3, want3)

	r4 := r3.Replace(0, len(want3)+50, "fresh")
	assertInvariants(t, r4, "fresh")
}

func TestRopeRandomizedOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	oracle := strings.Repeat("line0: hello 세계\nline1: foo bar baz\n", 300) // >10 KiB with UTF-8
	r := New(oracle)
	assertInvariants(t, r, oracle)

	type snap struct {
		r *Rope
		s string
	}
	var snaps []snap

	snippets := []string{
		"",
		"x",
		"\n",
		"한글\n테스트",
		strings.Repeat("chunk_", 400), // > maxChunk
		"foo\nbar\nbaz\n",
	}

	for step := range 400 {
		n := len(oracle)
		a := rng.IntN(n + 1)
		b := rng.IntN(n + 1)
		if a > b {
			a, b = b, a
		}
		ins := snippets[rng.IntN(len(snippets))]
		if step%3 == 0 {
			// Single-char typing edit.
			b = a
			ins = "a"
		}
		r = r.Replace(a, b, ins)
		oracle = oracle[:a] + ins + oracle[b:]

		if r.String() != oracle {
			t.Fatalf("step %d: String() diverged from oracle", step)
		}
		if step%25 == 0 {
			assertInvariants(t, r, oracle)
			snaps = append(snaps, snap{r: r, s: oracle})
		}
	}
	assertInvariants(t, r, oracle)
	for i, sn := range snaps {
		if got := sn.r.String(); got != sn.s {
			t.Fatalf("snapshot %d mutated after subsequent edits", i)
		}
	}
}

func BenchmarkRopeReplaceLarge(b *testing.B) {
	text := strings.Repeat("func example(x int) int { return x + 1 }\n", 25000) // ~1 MiB
	r := New(text)
	mid := len(text) / 2

	b.Run("RopeReplace", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = r.Replace(mid, mid+1, "y")
		}
	})

	b.Run("FullRebuildNew", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = New(r.Slice(0, mid) + "y" + r.Slice(mid+1, r.Len()))
		}
	})
}
