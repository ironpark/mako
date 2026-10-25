package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestTitlesUpdateWithoutPollingOrPainting(t *testing.T) {
	link := newFakeLink()
	changed := make(chan struct{}, 16)
	conn, err := newTermConn("s", link, 80, 24, func() {}, func() { changed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	m := newModel(config.Default(), "")
	m.Sessions.Set([]protocol.SessionInfo{{ID: "s", Agent: "shell", Title: "stale"}, {ID: "unattached", Title: "daemon title"}})
	m.conns["s"] = conn
	update := func(want string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("title notification missing")
		}
		m.syncTitles()
		if got := ggui.Peek(m.Titles)["s"]; got != want {
			t.Fatalf("title=%q, want %q", got, want)
		}
		if got := ggui.Peek(m.Titles)["unattached"]; got != "daemon title" {
			t.Fatalf("unattached title=%q", got)
		}
	}
	// Snapshot metadata restores a title without needing a new OSC from the agent.
	env, err := protocol.Encode(protocol.TypeSnapshot, "", protocol.Snapshot{SessionID: "s", Cols: 80, Rows: 24, Title: "before attach", Text: "body"})
	if err != nil {
		t.Fatal(err)
	}
	link.in <- env
	update("before attach")
	// No terminal paints between these updates: hidden tab labels still change.
	link.output(t, "\x1b]2;live title\x07")
	update("live title")
	link.output(t, "\x1b]2;next title\x1b\\")
	update("next title")
	// An explicit clear must not resurrect an older title from session polling.
	link.output(t, "\x1b]2;\x07")
	update("")
}
