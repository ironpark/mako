package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestFindGoesThroughATerminalsScrollback(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 40, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	// Twenty lines, three with the needle, most long scrolled out of view.
	var out strings.Builder
	for i := range 20 {
		if i == 2 || i == 9 || i == 17 {
			fmt.Fprintf(&out, "line %d needle\r\n", i)
		} else {
			fmt.Fprintf(&out, "line %d\r\n", i)
		}
	}
	link.output(t, out.String())
	waitFor(t, func() bool { _, n := conn.find("needle", false); return n == 3 })
	seen := map[string]bool{}
	shown := map[string]bool{}
	for range 3 {
		at, n := conn.find("needle", false)
		if n != 3 || at < 1 || at > 3 {
			t.Fatalf("find is at %d of %d, want one of 3", at, n)
		}
		seen[conn.selection()] = true
		// The match is scrolled into the six rows in view.
		f, err := conn.frame()
		if err != nil {
			t.Fatal(err)
		}
		for row := range f.rows {
			var line strings.Builder
			for col := range f.cols {
				if c := f.textAt(row*f.cols + col); c != "\x00" {
					line.WriteString(c)
				}
			}
			if text := strings.TrimSpace(line.String()); strings.Contains(text, "needle") {
				shown[text] = true
			}
		}
	}
	if !shown["line 2 needle"] || !shown["line 9 needle"] {
		t.Fatalf("the lines in view as the matches were found: %v; want lines 2 and 9 among them", shown)
	}
	if !seen["needle"] || len(seen) != 1 {
		t.Fatalf("the matches selected %v, want needle each time", seen)
	}
	if _, n := conn.find("absent", false); n != 0 {
		t.Fatalf("%d matches of words not there", n)
	}
	conn.endFind()
	if got := conn.selection(); got != "" {
		t.Fatalf("closing the find left %q selected", got)
	}
}

func TestChatMatchesAreTheWordsOfItsTurns(t *testing.T) {
	turns := chatTurns([]*protocol.ChatMessage{
		{Role: "user", Text: "Fix the Path"},
		{Role: "tool", Id: "t1", Tool: "Bash", Text: `{"command":"cat path"}`},
		{Role: "assistant", Text: "The path was wrong; the PATH is set now."},
	}, nil)
	got := chatMatches(turns, "path")
	if len(got) != 3 || got[0].n != 0 || got[1].n != 0 || got[2].n != 1 || got[1].key != got[2].key || got[0].key == got[1].key {
		t.Fatalf("matches %+v, want the user's one and the agent's two, the tool's none", got)
	}
	if chatMatches(turns, "") != nil {
		t.Fatal("no words found something")
	}
}

func TestFindInAChatSelectsTheMatchInItsTurn(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	m.Selected.Set("s")
	state := m.chatState("s")
	state.loading = true
	var messages []*protocol.ChatMessage
	for i := range 300 {
		text := fmt.Sprintf("turn %d", i)
		if i == 10 || i == 250 {
			text = fmt.Sprintf("turn %d mentions the Haystack here", i)
		}
		messages = append(messages, &protocol.ChatMessage{Role: "user", Text: text})
	}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Conversation: "c", Messages: messages}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return chatView(m, "s", 1) }, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	selected := func() string {
		for _, n := range p.Semantics().All() {
			if n.Role == ggui.RoleText && n.SelEnd > n.SelStart {
				return n.Name
			}
		}
		return ""
	}
	m.FindText.Set("haystack")
	m.openFind()
	for range 8 {
		p.Frame()
	}
	if got := selected(); got != "turn 250 mentions the Haystack here" {
		t.Fatalf("find selected in %q, want the latest match, in turn 250", got)
	}
	if bar := p.Semantics().String(); !strings.Contains(bar, "1/2") {
		t.Fatalf("the bar does not say 1/2:\n%s", bar)
	}
	// Enter goes to the match before, far up the conversation.
	p.Key("enter")
	for range 8 {
		p.Frame()
	}
	if got := selected(); got != "turn 10 mentions the Haystack here" {
		t.Fatalf("Enter selected in %q, want turn 10", got)
	}
	p.Key("escape")
	p.Frame()
	if ggui.Peek(m.FindOpen) {
		t.Fatal("Escape left the find bar open")
	}
}
