package gui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// BenchmarkChatScroll scrolls a long conversation of prose, lists, code
// and tool calls up by a wheel's step a frame, painting each.
func BenchmarkChatScroll(b *testing.B) {
	b.Setenv("MAKO_HOME", b.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	var messages []*protocol.ChatMessage
	code := "```go\n" + strings.Repeat("func f(x int) int {\n\treturn x * 2 // doubled\n}\n", 6) + "```"
	for i := range 300 {
		messages = append(messages,
			&protocol.ChatMessage{Role: "user", Text: fmt.Sprintf("turn %d: can you look at the **cache** and fix the `layout` bug?", i)},
			&protocol.ChatMessage{Role: "assistant", Text: "I looked at it. The problem is in `cache.go`:\n\n- the sources are copied\n- every cache keeps them\n\n" + code + "\n\nThat should do it. 한국어 문장도 섞여 있습니다."},
			&protocol.ChatMessage{Role: "assistant", Tool: "Bash", Kind: "Bash", Detail: "go test ./...", Result: strings.Repeat("ok  \tgithub.com/x/y\t0.3s\n", 8)},
			&protocol.ChatMessage{Role: "assistant", Tool: "Edit", Kind: "Edit", Detail: "cache.go"},
		)
	}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Conversation: "c", Messages: messages}})
	var p *ggui.Probe
	m.post = func(fn func()) { p.Post(fn) }
	p = ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s", 1) }, ggui.Sz(900, 1000))
	defer p.Close()
	img := ggfx.NewImage(900, 1000)
	for range 5 {
		p.Draw(img)
	}
	first := func() string {
		page := p.Semantics().String()
		i := strings.Index(page, "turn ")
		if i < 0 {
			return "none"
		}
		return page[i : i+9]
	}
	if os.Getenv("CHAT_SCROLL_TRACE") != "" {
		var ds []time.Duration
		for range 400 {
			start := time.Now()
			p.Scroll(ggui.Pt(450, 500), ggui.Pt(0, scrollStep))
			p.Draw(img)
			ds = append(ds, time.Since(start))
		}
		var total time.Duration
		for _, d := range ds {
			total += d
		}
		sorted := slices.Clone(ds)
		slices.Sort(sorted)
		b.Logf("400 frames %v, median %v, p90 %v, max %v, at top: %s", total, sorted[200], sorted[360], sorted[399], first())
		var line []string
		for i, d := range ds[:120] {
			line = append(line, fmt.Sprintf("%d:%.1f", i, float64(d.Microseconds())/1000))
		}
		b.Log(strings.Join(line, " "))
		return
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p.Scroll(ggui.Pt(450, 500), ggui.Pt(0, scrollStep))
		p.Draw(img)
	}
}

// scrollStep is how far a frame of BenchmarkChatScroll scrolls, as a
// flick with momentum does.
const scrollStep = 60
