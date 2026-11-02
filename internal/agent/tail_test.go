package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// tailLog is n records, every fifth longer than a reader's buffer.
func tailLog(n int, at time.Time) string {
	var b strings.Builder
	for i := range n {
		text := fmt.Sprintf("record %d", i)
		if i%5 == 4 {
			text = strings.Repeat("x", 100<<10)
		}
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"content":%q}}`+"\n", at.Add(time.Duration(i)*time.Second).UTC().Format(time.RFC3339Nano), text)
	}
	return b.String()
}

// A tail hands on each complete record once, long ones whole, a record
// still being written only once it is, and knows when the first and the
// latest were written. What it hands on lies in room the next record and
// other reads use, so each is copied here; tails reading at once, in the
// same pool of readers, each read their own log.
func TestTailHandsOnEachRecordOnce(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Go(func() {
			path := filepath.Join(dir, fmt.Sprintf("log%d.jsonl", n))
			body := tailLog(20+n, at)
			var want []string
			for l := range strings.Lines(body) {
				want = append(want, l)
			}
			cut := len(body) - len(want[len(want)-1])/2 // the last half written
			if err := os.WriteFile(path, []byte(body[:cut]), 0o600); err != nil {
				t.Error(err)
				return
			}
			var tail Tail
			var got []string
			read := func() { tail.Read(path, func() { got = nil }, func(l []byte) { got = append(got, string(l)) }) }
			read()
			if len(got) != len(want)-1 {
				t.Errorf("%s: %d records before the last was written, want %d", path, len(got), len(want)-1)
				return
			}
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.WriteString(body[cut:])
			f.Close()
			read()
			read() // nothing new
			if !slices.Equal(got, want) {
				t.Errorf("%s: records differ from the log's", path)
			}
			if !tail.Started.Equal(at) || !tail.Updated.Equal(at.Add(time.Duration(len(want)-1)*time.Second)) {
				t.Errorf("%s: written %v to %v", path, tail.Started, tail.Updated)
			}
		})
	}
	wg.Wait()
}

func BenchmarkTailRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(tailLog(200, time.Now())), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var t Tail
		n := 0
		t.Read(path, func() {}, func([]byte) { n++ })
		if n != 200 {
			b.Fatalf("%d records", n)
		}
	}
}
