//go:build unix

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// texts are the blocks' texts, joined, each a tool's name for a call.
func texts(blocks []*protocol.LiveBlock) string {
	var out []string
	for _, b := range blocks {
		out = append(out, b.GetText()+b.GetTool())
	}
	return strings.Join(out, "|")
}

// A step's pieces join into its blocks; a new step starts again; a log
// that grows drops the blocks before the one streaming, and all of a step
// that ended; a step that ended and was never logged goes in time.
func TestLiveBlocksLeaveAsTheLogTakesThem(t *testing.T) {
	var h liveHub
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Hel"}}})
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "lo"}, {Kind: "tool", Index: 1, Tool: "Bash"}}})
	if got := texts(h.blocks("s")); got != "Hello|Bash" {
		t.Fatalf("blocks %q", got)
	}
	if grew, changed := h.logged("s", 0); grew || changed {
		t.Fatal("a log that did not grow changed the blocks")
	}
	if grew, changed := h.logged("s", 10); !grew || !changed || texts(h.blocks("s")) != "Bash" {
		t.Fatalf("after the log grew: %q", texts(h.blocks("s")))
	}
	h.take(livePost{Session: "s", Step: "t:0", End: true})
	h.logged("s", 20)
	if got := h.blocks("s"); len(got) != 0 {
		t.Fatalf("a step ended and logged left %q", texts(got))
	}
	h.take(livePost{Session: "s", Step: "t:1", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Next"}}, End: true})
	if got := texts(h.blocks("s")); got != "Next" {
		t.Fatalf("a new step: %q", got)
	}
	h.chats["s"].ended = time.Now().Add(-liveStale - time.Second)
	if _, changed := h.logged("s", 20); !changed || len(h.blocks("s")) != 0 {
		t.Fatal("a step never logged stayed")
	}
}

// A front-end follows a session on another node: what the agent's mod
// posts there streams to it, and the log growing says so.
func TestAChatIsFollowedAcrossNodes(t *testing.T) {
	a := meshDaemon(t, "live-a")
	b := meshDaemon(t, "live-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	addSessionOf(t, b, "agent", "claude")
	s, _ := b.sessions.get("agent")
	log := filepath.Join(t.TempDir(), "c.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.SetConversation("c", log)
	if b.liveSocket == "" {
		t.Fatal("no live socket")
	}
	post := func(body string) {
		t.Helper()
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", b.liveSocket)
		}}}
		resp, err := hc.Post("http://mako/live", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("post answered %s", resp.Status)
		}
	}
	waitFor(t, "the other node", func() bool { return lists(t.Context(), dialDaemon(t, a), "agent") })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got := make(chan *protocol.ChatLive, 16)
	go func() {
		_ = dialDaemon(t, a).WatchChat(ctx, &protocol.WatchChat{NodeId: b.cfg.NodeID, SessionId: "agent"}, func(l *protocol.ChatLive) { got <- l })
	}()
	next := func(what string, ok func(*protocol.ChatLive) bool) {
		t.Helper()
		for {
			select {
			case l := <-got:
				if ok(l) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %s", what)
			}
		}
	}
	next("first word", func(*protocol.ChatLive) bool { return true })
	post(`{"session":"agent","step":"t:0","pieces":[{"k":"text","i":0,"t":"Thinking about it"}]}`)
	next("the streamed text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Thinking about it" })
	post(`{"session":"agent","step":"t:0","end":true}`)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant"}` + "\n")
	f.Close()
	next("word of the log", func(l *protocol.ChatLive) bool { return l.GetLogged() && len(l.GetBlocks()) == 0 })
}
