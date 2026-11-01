package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// A message is typed into its recipient's terminal once the agent waits at
// its prompt, never during a turn, and one prompt at a time.
func TestMessagesAreTypedIntoAnAgentAtItsPrompt(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	s, err := session.Start(session.Spec{
		ID: "r", NodeID: "test-node", Agent: "shell", Project: dir,
		Command: []string{"sh", "-c", `stty -echo; printf '\033[?2004h'; while IFS= read -r line; do printf '%s\n' "$line" >> out; done`},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFor(t, "the program never asked for bracketed paste", func() bool {
		snap, err := s.Snapshot(true)
		return err == nil && strings.Contains(snap.GetText(), "\x1b[?2004h")
	})
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	_, release := d.projectAgents.server(t.Context(), "", s.ID(), "")
	defer release()
	peer := d.projectAgents.activate(s)
	go d.deliverAgentMessages(peer)
	typed := func() string {
		got, _ := os.ReadFile(out)
		return string(got)
	}
	state := func(id uint64) string {
		_, history := d.projectAgents.messages.monitor("", map[string]bool{s.ID(): true}, 10)
		for _, msg := range history {
			if msg.GetId() == id {
				return msg.GetState()
			}
		}
		return ""
	}

	s.SetStatus(protocol.AgentWorking)
	first, err := d.projectAgents.messages.send("p", s.ID(), "check\x1b[201~ cancellation", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(deliverySettle + deliveryPoll)
	if typed() != "" || state(first.ID) != "pending" {
		t.Fatalf("typed during a turn: %q, %s", typed(), state(first.ID))
	}

	s.SetStatus(protocol.AgentDone)
	waitForWithin(t, 10*time.Second, "the message was never typed", func() bool { return strings.Contains(typed(), "check[201~ cancellation") })
	if !strings.Contains(typed(), "[Mako message 1 from session p.") || state(first.ID) != "delivered" {
		t.Fatalf("typed %q, %s", typed(), state(first.ID))
	}
	if inbox, _ := d.projectAgents.messages.pending(s.ID()); len(inbox) != 0 {
		t.Fatalf("delivered messages stayed in the inbox: %+v", inbox)
	}

	// Until the agent says it took the prompt, the next waits.
	second, err := d.projectAgents.messages.send("p", s.ID(), "and the tests", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(deliveryQuiet + deliveryPoll)
	if strings.Contains(typed(), "and the tests") {
		t.Fatal("typed over a prompt the agent had not taken")
	}
	s.SetStatus(protocol.AgentWorking)
	s.SetStatus(protocol.AgentDone)
	waitForWithin(t, 10*time.Second, "the next message was never typed", func() bool { return strings.Contains(typed(), "and the tests") })
	if state(second.ID) != "delivered" {
		t.Fatalf("second is %s", state(second.ID))
	}
}

func TestTypableKeepsLinesAndDropsKeys(t *testing.T) {
	if got, want := typable("a\r\nb\rc\td\x1b[201~\x03\u0085e"), "a\nb\nc\td[201~e"; got != want {
		t.Fatalf("typable = %q, want %q", got, want)
	}
}
