package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/sanbo/internal/protocol"
)

// usageScan adds up the tokens a log records as it grows: each read goes on
// from where the last stopped, so a long conversation is read once.
type usageScan struct {
	path   string
	offset int64
	usage  *protocol.Usage // nil until a record counts
	// seen is the Claude messages counted: a reply written in several
	// records repeats its usage in each.
	seen map[string]bool
}

// read brings the scan up to the end of path's complete records, and
// returns a copy of the usage, nil while nothing was counted.
func (u *usageScan) read(path string, a *Agent) *protocol.Usage {
	f, err := os.Open(path)
	if err != nil {
		return u.result()
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil || u.path != path || stat.Size() < u.offset {
		*u = usageScan{path: path} // another log, or this one rewritten
	}
	if _, err := f.Seek(u.offset, io.SeekStart); err != nil {
		return u.result()
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // the writer may be in the middle of a record
		}
		u.offset += int64(len(line))
		if a.rollout {
			u.codex(line)
		} else {
			u.claude(line)
		}
	}
	return u.result()
}

// result is a copy of the usage counted, for the caller to keep; nil for
// none.
func (u *usageScan) result() *protocol.Usage {
	if u.usage.IsZero() {
		return nil
	}
	return proto.CloneOf(u.usage)
}

// counted is the usage to add a record to.
func (u *usageScan) counted() *protocol.Usage {
	if u.usage == nil {
		u.usage = &protocol.Usage{}
	}
	return u.usage
}

func (u *usageScan) claude(line []byte) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	var r struct {
		Type      string `json:"type"`
		Sidechain bool   `json:"isSidechain"`
		Message   struct {
			ID    string `json:"id"`
			Usage *struct {
				Input         int64 `json:"input_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
				Output        int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.Message.Usage == nil {
		return
	}
	if r.Message.ID != "" {
		if u.seen == nil {
			u.seen = map[string]bool{}
		}
		if u.seen[r.Message.ID] {
			return
		}
		u.seen[r.Message.ID] = true
	}
	t := r.Message.Usage
	usage := u.counted()
	usage.Input += t.Input + t.CacheCreation
	usage.CachedInput += t.CacheRead
	usage.Output += t.Output
	if !r.Sidechain {
		usage.Context = t.Input + t.CacheCreation + t.CacheRead
	}
}

func (u *usageScan) codex(line []byte) {
	if !bytes.Contains(line, []byte(`"token_count"`)) {
		return
	}
	type tokens struct {
		Input  int64 `json:"input_tokens"` // the cached included
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	}
	type limit struct {
		Used    float64 `json:"used_percent"`
		Minutes int64   `json:"window_minutes"`
		Resets  int64   `json:"resets_at"`
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				Total  tokens `json:"total_token_usage"`
				Last   tokens `json:"last_token_usage"`
				Window int64  `json:"model_context_window"`
			} `json:"info"`
			Limits *struct {
				Primary   *limit `json:"primary"`
				Secondary *limit `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil || r.Payload.Type != "token_count" {
		return
	}
	// Codex records its running totals.
	if info := r.Payload.Info; info != nil {
		t := info.Total
		usage := u.counted()
		usage.Input, usage.CachedInput, usage.Output = t.Input-t.Cached, t.Cached, t.Output
		usage.Context, usage.Window = info.Last.Input, info.Window
	}
	if l := r.Payload.Limits; l != nil {
		for _, w := range []*limit{l.Primary, l.Secondary} {
			if w != nil && w.Minutes != 0 {
				u.counted().SetLimit(&protocol.RateLimit{Minutes: w.Minutes, Used: w.Used, Resets: w.Resets})
			}
		}
	}
}
