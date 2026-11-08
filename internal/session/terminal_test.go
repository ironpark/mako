//go:build unix

package session

import (
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// replies starts a program that waits for a byte, then runs prompt and
// prints what the terminal answers it, ESC shown as E. ready runs once the
// program waits.
func replies(t *testing.T, prompt string, ready func(s *Session)) string {
	t.Helper()
	script := `stty raw -echo; printf R; head -c1 >/dev/null; stty min 0 time 10; printf 'Q'; ` + prompt + `; cat | tr '\033' E; printf DONE; sleep 5`
	s, err := Start(Spec{ID: "replies", Command: []string{"/bin/sh", "-c", script}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	screen := func() string {
		snap, _ := s.Snapshot(false)
		return snap.GetText()
	}
	waitFor(t, func() bool { return strings.Contains(screen(), "R") })
	ready(s)
	if _, err := s.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(screen(), "DONE") })
	text := screen()
	return text[strings.Index(text, "Q")+1 : strings.Index(text, "DONE")]
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A program that asked for in-band size reports is told of a resize.
func TestAResizeIsReportedInBand(t *testing.T) {
	got := replies(t, `printf '\033[?2048h'; sleep 0.3`, func(s *Session) {
		go func() {
			time.Sleep(100 * time.Millisecond)
			if err := s.ResizeCells(100, 30, 8, 16); err != nil {
				t.Error(err)
			}
		}()
	})
	if want := "E[48;30;100;480;800t"; !strings.Contains(got, want) {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

// The terminal answers with the front-end's colours, and says it is dark
// when the front-end is.
func TestTheTerminalAnswersWithTheFrontEndsColours(t *testing.T) {
	got := replies(t, `printf '\033]11;?\033\\\033]10;?\033\\\033[?996n'`, func(s *Session) {
		s.SetColors(&protocol.TerminalColors{Foreground: 0xfafafa, Background: 0x181b1a, Dark: true})
	})
	for _, want := range []string{"E]11;rgb:1818/1b1b/1a1a", "E]10;rgb:fafa/fafa/fafa", "E[?997;1n"} {
		if !strings.Contains(got, want) {
			t.Errorf("replies %q lack %q", got, want)
		}
	}
}

// Bells and the program's own notifications are counted for front-ends.
func TestBellsAndNotificationsReachTheInfo(t *testing.T) {
	s, err := Start(Spec{ID: "bell", Command: []string{"/bin/sh", "-c", `printf '\a\a\033]9;hello\a\033]777;notify;Built;all done\033\\'`}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	info := s.Info()
	if info.GetBells() != 2 {
		t.Errorf("bells = %d, want 2", info.GetBells())
	}
	if n := info.GetNotice(); n.GetTitle() != "Built" || n.GetBody() != "all done" || n.GetSeq() != 2 {
		t.Errorf("notice = %v", n)
	}
}

// A program that asks the terminal things and never reads the answers
// fills its input, and the answers then wait without holding the session.
func TestUnreadRepliesDoNotHoldTheSession(t *testing.T) {
	script := `stty raw -echo; while :; do printf '\033[c\033[6n\033[c\033[6n\033[c\033[6n\033[c\033[6n'; done`
	s, err := Start(Spec{ID: "unread", Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	time.Sleep(time.Second) // long enough to fill the PTY's input
	got := make(chan struct{})
	go func() {
		for range 10 {
			s.Info()
			time.Sleep(50 * time.Millisecond)
		}
		close(got)
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("Info blocked behind the program's unread input")
	}
}
