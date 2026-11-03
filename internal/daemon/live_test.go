//go:build unix

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	k := liveKey("s", "")
	look := func(size int64) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.chats[k].look(size)
	}
	if !h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Hel"}}}) {
		t.Fatal("the first post is not said to be")
	}
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "lo"}, {Kind: "tool", Index: 1, Tool: "Bash"}}})
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "input", Index: 1, Text: `{"command":"ls`}, {Kind: "input", Index: 1, Text: strings.Repeat("x", 2*liveArgs)}}})
	if got := h.blocks(k); len(got) != 2 || got[0].GetText() != "Hello" || got[1].GetTool() != "Bash" || len(got[1].GetText()) > 2*liveArgs {
		t.Fatalf("blocks %q", texts(got))
	}
	if look(0) {
		t.Fatal("a log that did not grow")
	}
	if !look(10) || texts(h.blocks(k))[:len(`{"command":"ls`)] != `{"command":"ls` || len(h.blocks(k)) != 1 {
		t.Fatalf("after the log grew: %q", texts(h.blocks(k)))
	}
	h.take(livePost{Session: "s", Step: "t:0", End: true})
	look(20)
	if got := h.blocks(k); len(got) != 0 {
		t.Fatalf("a step ended and logged left %q", texts(got))
	}
	if h.take(livePost{Session: "s", Agent: "sub", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Sub"}}}) {
		t.Fatal("a subagent's post is the session's first")
	}
	if got := texts(h.blocks(liveKey("s", "sub"))); got != "Sub" || len(h.blocks(k)) != 0 {
		t.Fatalf("a subagent's reply %q, the session's %q", got, texts(h.blocks(k)))
	}
	h.take(livePost{Session: "s", Step: "t:1", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Next"}}, End: true})
	if got := texts(h.blocks(k)); got != "Next" {
		t.Fatalf("a new step: %q", got)
	}
	h.chats[k].ended = time.Now().Add(-liveStale - time.Second)
	look(20)
	if len(h.blocks(k)) != 0 {
		t.Fatal("a step never logged stayed")
	}
	h.forget("s")
	if len(h.chats) != 0 || h.blocks(k) != nil {
		t.Fatal("a session closed left its replies")
	}
}

// Two followers of one conversation hear the same: what streams, and that
// the log grew, which one does not take from the other.
func TestFollowersHearAlike(t *testing.T) {
	var h liveHub
	k := liveKey("s", "")
	var size atomic.Int64
	a, stopA := h.follow(k, size.Load)
	defer stopA()
	b, stopB := h.follow(k, size.Load)
	defer stopB()
	hear := func(feed <-chan *protocol.ChatLive, what string, ok func(*protocol.ChatLive) bool) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l := <-feed:
				if ok(l) {
					return
				}
			case <-deadline:
				t.Fatalf("no %s", what)
			}
		}
	}
	h.take(livePost{Session: "s", Step: "t:0", Pieces: []livePiece{{Kind: "text", Index: 0, Text: "Hi"}}, End: true})
	for _, feed := range []<-chan *protocol.ChatLive{a, b} {
		hear(feed, "the text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Hi" })
	}
	size.Store(10)
	for _, feed := range []<-chan *protocol.ChatLive{a, b} {
		hear(feed, "word of the log", func(l *protocol.ChatLive) bool { return l.GetLogged() && len(l.GetBlocks()) == 0 })
	}
	stopA()
	stopB()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chats[k].stop != nil {
		t.Fatal("the look-out outlived its followers")
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
	post := func(body string, want int) {
		t.Helper()
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", b.liveSocket)
		}}}
		resp, err := hc.Post("http://mako/live", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
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
	post(`{"session":"agent","token":"forged","step":"t:0","pieces":[{"k":"text","i":0,"t":"Forged"}]}`, http.StatusForbidden)
	token := b.liveToken("agent")
	post(`{"session":"agent","token":"`+token+`","step":"t:0","pieces":[{"k":"text","i":0,"t":"Thinking about it"}]}`, http.StatusNoContent)
	next("the streamed text", func(l *protocol.ChatLive) bool { return texts(l.GetBlocks()) == "Thinking about it" })
	post(`{"session":"agent","token":"`+token+`","step":"t:0","end":true}`, http.StatusNoContent)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant"}` + "\n")
	f.Close()
	next("word of the log", func(l *protocol.ChatLive) bool { return l.GetLogged() && len(l.GetBlocks()) == 0 })
}
