package syntax

import (
	"context"
	"strings"
	"testing"
	"time"
)

const htmlChunk = `<section class="card" id="item">
  <!-- card header -->
  <h2 title='Heading'>Title &amp; subtitle</h2>
  <p>Some <em>text</em> with <a href="https://example.com/?a=1&b=2">a link</a>.</p>
  <img src="pic.png" alt="picture"/>
  <ul><li>one</li><li>two</li><li data-x="3">three</li></ul>
</section>
`

const luaChunk = `local function step(self, x, ...)
  -- advance the state
  if x == nil then return self end
  local t = { a = 1, b = "two", [3] = 0x10, MAX_N = 1e3 }
  for i, v in ipairs(t) do print(i, v, #t) end
  return setmetatable(t, { __index = self })
end
`

// bigSource repeats chunk until it is about size bytes.
func bigSource(chunk string, size int) []byte {
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(chunk)
	}
	return []byte(b.String())
}

func benchHighlight(b *testing.B, name, chunk string) {
	r := newRuntime(b)
	l := load(b, r, name)
	q := highlights(b, l)
	src := bigSource(chunk, 100<<10)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for b.Loop() {
		tree, err := l.Parse(src, nil)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := q.Captures(tree, src, 0, len(src)); err != nil {
			b.Fatal(err)
		}
		tree.Close()
	}
}

func BenchmarkHTMLParseHighlight100K(b *testing.B) { benchHighlight(b, "html", htmlChunk) }
func BenchmarkLuaParseHighlight100K(b *testing.B)  { benchHighlight(b, "lua", luaChunk) }

// TestTiming logs what an editor would see on a ~100 KB file: a full
// parse, highlighting everything, highlighting one screen, and an
// incremental reparse after a one-byte edit.
func TestTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	start := time.Now()
	r, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	t.Logf("NewRuntime: %v", time.Since(start))

	for _, tc := range []struct{ name, chunk string }{{"html", htmlChunk}, {"lua", luaChunk}} {
		start = time.Now()
		l := load(t, r, tc.name)
		loadTime := time.Since(start)
		q := highlights(t, l)
		src := bigSource(tc.chunk, 100<<10)

		start = time.Now()
		tree, err := l.Parse(src, nil)
		if err != nil {
			t.Fatal(err)
		}
		parse := time.Since(start)

		start = time.Now()
		caps, err := q.Captures(tree, src, 0, len(src))
		if err != nil {
			t.Fatal(err)
		}
		all := time.Since(start)

		start = time.Now()
		screen, err := q.Captures(tree, src, len(src)/2, len(src)/2+4000)
		if err != nil {
			t.Fatal(err)
		}
		view := time.Since(start)

		// Insert one byte in the middle of the file and reparse.
		at := len(src) / 2
		for src[at] != '\n' {
			at++
		}
		edited := append(append(append([]byte{}, src[:at]...), ' '), src[at:]...)
		row := strings.Count(string(src[:at]), "\n")
		col := at - (strings.LastIndexByte(string(src[:at]), '\n') + 1)
		tree.Edit(InputEdit{
			StartByte: at, OldEndByte: at, NewEndByte: at + 1,
			StartPoint: Point{row, col}, OldEndPoint: Point{row, col}, NewEndPoint: Point{row, col + 1},
		})
		start = time.Now()
		next, err := l.Parse(edited, tree)
		if err != nil {
			t.Fatal(err)
		}
		re := time.Since(start)
		next.Close()
		tree.Close()

		t.Logf("%s %d KB: load %v, parse %v, highlight all %v (%d captures), one screen %v (%d), reparse after edit %v",
			tc.name, len(src)>>10, loadTime, parse, all, len(caps), view, len(screen), re)
	}
}
