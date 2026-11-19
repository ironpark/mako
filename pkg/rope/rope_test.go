package rope

import (
	"fmt"
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
	for i := 0; i < len(lines); i++ {
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
	for i := 0; i < 5000; i++ {
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
	for i := 0; i < 20000; i++ {
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
