package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/proto"
)

func TestUsageAddsUpAsTheLogGrows(t *testing.T) {
	claude, _ := Lookup("claude")
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

	codex, _ := Lookup("codex")
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
