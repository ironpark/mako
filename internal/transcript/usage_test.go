package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/proto"
)

func TestUsageAddsUpAsTheLogGrows(t *testing.T) {
	claude, _ := logsOf("claude")
	path := filepath.Join(t.TempDir(), "log.jsonl")
	write := func(lines ...string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, l := range lines {
			_, _ = f.WriteString(l + "\n")
		}
	}
	var scan usageScan
	write(
		`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}`,
		// The same reply's second block repeats its usage.
		`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}`,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	)
	if got := scan.read(path, claude); !proto.Equal(got, &protocol.Usage{Input: 102, CachedInput: 1000, Output: 5, Context: 1102}) {
		t.Fatalf("first read %+v", got)
	}
	write(
		`{"type":"assistant","isSidechain":true,"message":{"id":"s","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"assistant","message":{"id":"b","usage":{"input_tokens":1,"cache_read_input_tokens":1200,"output_tokens":7}}}`,
		`{"type":"assistant","message":{"id":"c","usage"`, // still being written
	)
	if got := scan.read(path, claude); !proto.Equal(got, &protocol.Usage{Input: 113, CachedInput: 2200, Output: 13, Context: 1201}) {
		t.Fatalf("second read %+v", got)
	}

	codex, _ := logsOf("codex")
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	_ = os.WriteFile(rollout, []byte(`{"type":"event_msg","payload":{"type":"token_count","info":null}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":17110,"cached_input_tokens":13184,"output_tokens":5},"last_token_usage":{"input_tokens":9000,"cached_input_tokens":8000,"output_tokens":5},"model_context_window":258400},"rate_limits":{"primary":{"used_percent":7.0,"window_minutes":10080,"resets_at":1793528251},"secondary":null}}}
{"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":12.5,"window_minutes":300,"resets_at":1793500000},"secondary":{"used_percent":8.0,"window_minutes":10080,"resets_at":1793528251}}}}
`), 0o600)
	var cs usageScan
	if got := cs.read(rollout, codex); !proto.Equal(got, &protocol.Usage{Input: 3926, CachedInput: 13184, Output: 5, Context: 9000, Window: 258400,
		Limits: []*protocol.RateLimit{{Minutes: 300, Used: 12.5, Resets: 1793500000}, {Minutes: 10080, Used: 8, Resets: 1793528251}}}) {
		t.Fatalf("codex %+v", got)
	}
}

// Records longer than a reader's buffer count as the short ones do, read
// at once or as the log grows, while other reads use the same readers.
func TestUsageCountsLongRecords(t *testing.T) {
	claude, _ := logsOf("claude")
	dir := t.TempDir()
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","message":{"id":"m%d","content":[{"type":"text","text":%q}],"usage":{"input_tokens":%d,"output_tokens":5}}}`, i, strings.Repeat("x", (i%3)*(70<<10)), i))
	}
	want := &protocol.Usage{Input: 29 * 30 / 2, Output: 150, Context: 29}
	var wg sync.WaitGroup
	for n := range 4 {
		wg.Go(func() {
			path := filepath.Join(dir, fmt.Sprintf("log%d.jsonl", n))
			var whole, grown usageScan
			f, _ := os.Create(path)
			for i, l := range lines {
				_, _ = f.WriteString(l + "\n")
				if i%(n+2) == 0 {
					grown.read(path, claude)
				}
			}
			f.Close()
			if got := grown.read(path, claude); !proto.Equal(got, want) {
				t.Errorf("read as it grew: %v, want %v", got, want)
			}
			if got := whole.read(path, claude); !proto.Equal(got, want) {
				t.Errorf("read at once: %v, want %v", got, want)
			}
		})
	}
	wg.Wait()
}

func BenchmarkUsageRead(b *testing.B) {
	claude, _ := logsOf("claude")
	path := filepath.Join(b.TempDir(), "log.jsonl")
	var body []byte
	for i := range 200 {
		body = fmt.Appendf(body, `{"type":"assistant","message":{"id":"m%d","content":[{"type":"text","text":%q}],"usage":{"input_tokens":2,"output_tokens":5}}}`+"\n", i, strings.Repeat("x", 1+(i%5/4)*(100<<10)))
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var scan usageScan
		if scan.read(path, claude).GetOutput() != 1000 {
			b.Fatal("miscounted")
		}
	}
}
