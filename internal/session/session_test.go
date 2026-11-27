//go:build unix

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/gostty"

	"github.com/ironpark/mako/internal/kitty"
	"github.com/ironpark/mako/internal/protocol"
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
	_, _ = src.WriteString("prompt\r\n\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1h\x1b[?25l\x1b[>11utui screen")
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
	if got := kitty.Flags(term); got != kitty.Disambiguate|kitty.EventTypes|kitty.AllKeysAsCodes {
		t.Fatalf("Kitty keyboard flags %05b, want those pushed", got)
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

func TestSessionRestartKeepsSessionAndSubscribersAlive(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "in.txt")
	s, err := Start(Spec{
		ID:      "restart-1",
		Agent:   "codex",
		Project: dir,
		Command: []string{"/bin/sh", "-c", "printf '\033]9;4;3\007first-proc'; sleep 30"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sub := s.Subscribe()
	defer sub.Close()

	oldPID := s.PID()
	if oldPID <= 0 {
		t.Fatalf("unexpected initial PID %d", oldPID)
	}
	// Wait for first process output & busy progress.
	deadline := time.Now().Add(3 * time.Second)
	for !s.Info().GetBusy() {
		if time.Now().After(deadline) {
			t.Fatal("initial process never reported progress")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.SetStatus(protocol.AgentWorking)

	// Drain initial subscriber output in background while collecting post-restart chunks.
	var gotBuf strings.Builder
	subDone := make(chan struct{})
	seenSecond := make(chan struct{}, 1)
	go func() {
		defer close(subDone)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		for {
			chunk, ok := sub.Next(ctx)
			if !ok {
				return
			}
			gotBuf.Write(chunk)
			if strings.Contains(gotBuf.String(), "second-proc:tok-new") {
				select {
				case seenSecond <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	err = s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
		return []string{
			"/bin/sh", "-c",
			`stty -echo; printf 'second-proc:%s\n' "$MAKO_CODEX_WS_TOKEN"; IFS= read -r line; printf '%s' "$line" > in.txt; sleep 30`,
		}, map[string]string{"MAKO_CODEX_WS_TOKEN": "tok-new"}, nil
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}

	select {
	case <-s.Done():
		t.Fatal("s.Done() closed during in-place Restart")
	default:
	}
	if s.Info().GetState() != protocol.StateRunning {
		t.Fatalf("state = %q, want running", s.Info().GetState())
	}
	if s.Info().GetBusy() {
		t.Fatal("stale progress busy flag survived Restart")
	}
	if newPID := s.PID(); newPID <= 0 || newPID == oldPID {
		t.Fatalf("PID did not change across Restart: old=%d new=%d", oldPID, newPID)
	}
	select {
	case <-seenSecond:
	case <-time.After(3 * time.Second):
		t.Fatalf("subscriber never received replacement process output; got %q", gotBuf.String())
	}
	<-subDone

	// Writing to the session reaches the replacement process.
	if _, err := s.Write([]byte("hello-after-restart\n")); err != nil {
		t.Fatalf("Write after Restart: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(outPath)
		if string(b) == "hello-after-restart" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement process did not read input; got %q", string(b))
		}
		time.Sleep(15 * time.Millisecond)
	}

	// PostNotice updates SessionInfo.Notice and increments Seq.
	s.PostNotice("Recovered", "Backend restarted")
	if n := s.Info().GetNotice(); n.GetTitle() != "Recovered" || n.GetBody() != "Backend restarted" || n.GetSeq() != 1 {
		t.Fatalf("Notice = %+v", n)
	}
}

func TestSessionRestartFailureAndCloseCancellation(t *testing.T) {
	t.Run("prepare error exits session cleanly", func(t *testing.T) {
		s, err := Start(Spec{
			ID:      "restart-fail",
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Cols:    40, Rows: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		wantErr := errors.New("boom")
		if err := s.Restart(func(context.Context) ([]string, map[string]string, error) {
			return nil, nil, wantErr
		}); !errors.Is(err, wantErr) {
			t.Fatalf("Restart err = %v, want %v", err, wantErr)
		}
		select {
		case <-s.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("session did not exit after failed Restart")
		}
		if s.Info().GetState() != protocol.StateExited {
			t.Fatalf("state = %q, want exited", s.Info().GetState())
		}
	})

	t.Run("Close cancels in-flight Restart prepare", func(t *testing.T) {
		s, err := Start(Spec{
			ID:      "restart-cancel",
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Cols:    40, Rows: 10,
		})
		if err != nil {
			t.Fatal(err)
		}

		entered := make(chan struct{})
		restartErr := make(chan error, 1)
		go func() {
			restartErr <- s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
				close(entered)
				<-ctx.Done()
				return nil, nil, ctx.Err()
			})
		}()

		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("Restart prepare was not entered")
		}

		closeDone := make(chan error, 1)
		go func() { closeDone <- s.Close() }()

		select {
		case err := <-restartErr:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Restart err = %v, want context.Canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Restart hung after Close")
		}
		select {
		case <-closeDone:
		case <-time.After(3 * time.Second):
			t.Fatal("Close hung while Restart was in flight")
		}
	})

	t.Run("concurrent Restart during prepare is rejected immediately", func(t *testing.T) {
		s, err := Start(Spec{
			ID:      "restart-conc",
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Cols:    40, Rows: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		inPrepare := make(chan struct{})
		releasePrepare := make(chan struct{})
		firstErr := make(chan error, 1)
		go func() {
			firstErr <- s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
				close(inPrepare)
				<-releasePrepare
				return []string{"/bin/sh", "-c", "sleep 30"}, nil, nil
			})
		}()

		select {
		case <-inPrepare:
		case <-time.After(3 * time.Second):
			t.Fatal("first Restart did not enter prepare")
		}

		// A second Restart invoked while runRestart is active must fail immediately
		// rather than queueing with proc == nil and blocking forever.
		secondErr := make(chan error, 1)
		go func() {
			secondErr <- s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
				return []string{"/bin/sh", "-c", "sleep 30"}, nil, nil
			})
		}()
		select {
		case err := <-secondErr:
			if err == nil || !strings.Contains(err.Error(), "already in progress") {
				t.Fatalf("second Restart err = %v, want already in progress", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("second Restart blocked while first prepare was running")
		}

		close(releasePrepare)
		select {
		case err := <-firstErr:
			if err != nil {
				t.Fatalf("first Restart err = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("first Restart did not finish")
		}

		// A subsequent sequential Restart after the first completes succeeds.
		if err := s.Restart(func(ctx context.Context) ([]string, map[string]string, error) {
			return []string{"/bin/sh", "-c", "sleep 30"}, nil, nil
		}); err != nil {
			t.Fatalf("sequential Restart after completion failed: %v", err)
		}
	})
}
