//go:build !windows

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunTellsTheHookOfItsEvent(t *testing.T) {
	project := t.TempDir()
	ev := Event{Name: TurnDone, SessionID: "s1", Agent: "claude", Project: project, Branch: "feat/x"}
	out, err := Run(t.Context(), Hook{Event: TurnDone, Command: `printf '%s|%s|%s|%s|%s|%s' "$AGENT_MAKO_SESSION" "$AGENT_MAKO_AGENT" "$AGENT_MAKO_PROJECT" "$AGENT_MAKO_BRANCH" "$AGENT_MAKO_EVENT" "$(pwd -P)"`}, ev)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(project)
	if want := strings.Join([]string{"s1", "claude", project, "feat/x", TurnDone, real}, "|"); out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	out, err = Run(t.Context(), Hook{Event: TurnDone, Command: "echo broken >&2; exit 3"}, ev)
	if err == nil || out != "broken" {
		t.Fatalf("failing hook: %q, %v", out, err)
	}
}

func TestRunKeepsBoundedOutput(t *testing.T) {
	out, err := Run(t.Context(), Hook{Event: TurnDone, Command: "yes | head -c 100000"}, Event{Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxOutput+100 || !strings.Contains(out, "more bytes not kept") {
		t.Fatalf("kept %d bytes: %q", len(out), out[max(0, len(out)-60):])
	}
}

// alive reports whether the process pid runs, a zombie aside.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// pidIn waits for the process ID a hook writes to file.
func pidIn(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(file); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
	}
	t.Fatal("the hook did not start")
	return 0
}

// gone waits for the process pid to end.
func gone(t *testing.T, pid int, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: process %d still runs", what, pid)
		}
	}
}

// A hook that outlasts its timeout is killed with everything it started,
// and says it timed out.
func TestRunKillsAHookThatTimesOut(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	start := time.Now()
	_, err := Run(t.Context(), Hook{Event: TurnDone, TimeoutSec: 1, Command: `sleep 60 & echo $! > ` + pidFile + `; wait`}, Event{Project: dir})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s", took)
	}
	gone(t, pidIn(t, pidFile), "after its timeout")
}

// What a hook leaves running in the background ends as its command ends,
// rather than keep the output, or the daemon, waiting.
func TestRunEndsWhatTheHookLeftRunning(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	start := time.Now()
	out, err := Run(t.Context(), Hook{Event: TurnDone, Command: `sleep 60 & echo $! > ` + pidFile + `; echo done`}, Event{Project: dir})
	if err != nil || out != "done" {
		t.Fatalf("%q, %v", out, err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s", took)
	}
	gone(t, pidIn(t, pidFile), "after its command exited")
}

// A hook ends with the context it runs under, as the daemon's ends on
// shutdown, its children with it.
func TestRunEndsWithItsContext(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Hook{Event: TurnDone, TimeoutSec: 60, Command: `sleep 60 & echo $! > ` + pidFile + `; wait`}, Event{Project: dir})
		done <- err
	}()
	pid := pidIn(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled hook succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hook outlived its context")
	}
	gone(t, pid, "after its context ended")
}
