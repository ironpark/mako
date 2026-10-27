package session

import (
	"strings"
	"testing"
	"time"

	"github.com/ironpark/gostty"
)

// An exited session keeps the screen its agent left, so it can still be
// attached to and read, and resizing it is harmless.
func TestAnExitedSessionKeepsItsScreen(t *testing.T) {
	s, err := Start(Spec{ID: "s", Command: []string{"/bin/sh", "-c", "echo goodbye"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not exit")
	}
	for _, styled := range []bool{false, true} {
		snap, err := s.Snapshot(styled)
		if err != nil || !strings.Contains(snap.GetText(), "goodbye") {
			t.Fatalf("snapshot (styled %v) = %q, %v", styled, snap.GetText(), err)
		}
	}
	if err := s.Resize(80, 24); err != nil {
		t.Fatalf("resizing an exited session: %v", err)
	}
}

// A client attaching after the initial OSC must receive the program's title,
// including after the process has exited and its terminal has been released.
func TestSnapshotAndInfoKeepTerminalTitle(t *testing.T) {
	s, err := Start(Spec{ID: "title", Command: []string{"/bin/sh", "-c", "printf '\033]2;first\007\033]2;작업 제목\007body'"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	s.SetName("custom name")
	if info := s.Info(); info.GetTitle() != "작업 제목" || info.GetName() != "custom name" {
		t.Fatalf("info: %+v", info)
	}
	for _, styled := range []bool{false, true} {
		snap, err := s.Snapshot(styled)
		if err != nil || snap.GetTitle() != "작업 제목" || !strings.Contains(snap.GetText(), "body") {
			t.Fatalf("snapshot styled=%v title=%q, text=%q, err=%v", styled, snap.GetTitle(), snap.GetText(), err)
		}
	}
}

func TestPTYProgressIsIncludedInReplayAndClearedOnExit(t *testing.T) {
	s, err := Start(Spec{ID: "progress", Command: []string{"/bin/sh", "-c", "printf '\033]9;4;3\007'; read answer"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !s.Info().GetBusy() {
		if time.Now().After(deadline) {
			t.Fatal("PTY progress was not received")
		}
		time.Sleep(time.Millisecond)
	}
	snap, err := s.Snapshot(true)
	if err != nil || snap.GetProgress() == nil || snap.GetProgress().GetState() != 3 || snap.GetProgress().GetRemainingMs() <= 0 {
		t.Fatalf("snapshot progress=%+v err=%v", snap.GetProgress(), err)
	}
	if _, err := s.Write([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}
	if s.Info().GetBusy() {
		t.Fatal("exited process still busy")
	}
	snap, err = s.Snapshot(true)
	if err != nil || snap.GetProgress() != nil {
		t.Fatalf("exited progress=%+v err=%v", snap.GetProgress(), err)
	}
}

// A shell at its prompt is idle; while a command it started runs in front
// of it, it is not.
func TestAShellIsIdleOnlyAtItsPrompt(t *testing.T) {
	s, err := Start(Spec{ID: "idle", Command: []string{"/bin/sh", "-i"}, Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	until := func(what string, idle bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if s.Info().GetIdle() == idle {
				return
			}
		}
		t.Fatalf("%s: idle stayed %v", what, !idle)
	}
	until("at the prompt", true)
	if _, err := s.Write([]byte("sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	until("running sleep", false)
	if _, err := s.Write([]byte{3}); err != nil { // ^C
		t.Fatal(err)
	}
	until("back at the prompt", true)
}

// A terminal fed a snapshot is in the modes its program set: a TUI on the
// alternate screen that asked for the mouse keeps the wheel when another
// window attaches to it.
func TestASnapshotCarriesTheProgramsModes(t *testing.T) {
	src, err := gostty.New(40, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	_, _ = src.WriteString("prompt\r\n\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1h\x1b[?25ltui screen")
	text, err := src.FormatString(gostty.FormatOptions{Format: gostty.FormatterFormatVt, Cursor: true})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := gostty.New(40, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	_, _ = dst.WriteString(modesAround(src.Terminal(), text))
	term := dst.Terminal()
	if term.ActiveScreenKey() != gostty.ScreenKeyAlternate {
		t.Fatal("not on the alternate screen")
	}
	if term.MouseTracking() == gostty.MouseTrackingNone || !term.ModeEnabled(gostty.ModeMouseFormatSgr) {
		t.Fatal("the mouse is not reported")
	}
	if !term.ModeEnabled(gostty.ModeBracketedPaste) || !term.ModeEnabled(gostty.ModeCursorKeys) || term.ModeEnabled(gostty.ModeCursorVisible) {
		t.Fatal("modes lost")
	}
	plain, _ := dst.FormatString(gostty.FormatOptions{})
	if !strings.Contains(plain, "tui screen") {
		t.Fatalf("screen %q", plain)
	}
	// A shell's prompt sets nothing.
	plainSrc, _ := gostty.New(40, 6)
	defer plainSrc.Close()
	if got := modesAround(plainSrc.Terminal(), "x"); got != "x" {
		t.Fatalf("a plain screen got %q", got)
	}
}
