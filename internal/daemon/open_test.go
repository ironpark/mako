//go:build unix

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A session opened says its repository as the listing does, so a front-end
// puts it in its project's section, and the panes kept for it, at once.
func TestASessionOpenedSaysItsRepository(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:me/app.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	d := meshDaemon(t, "open")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	info, err := dialDaemon(t, d).Open(ctx, &protocol.OpenSession{Agent: "shell", Project: repo, Cols: 40, Rows: 6})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.closeSession(info.GetId()) })
	listed := d.withHead(&protocol.SessionInfo{Project: repo})
	if info.GetRepo() == "" || info.GetRemote() == "" || info.GetRemote() != listed.GetRemote() || info.GetRepo() != listed.GetRepo() {
		t.Fatalf("opened as repo %q remote %q, listed as %q %q", info.GetRepo(), info.GetRemote(), listed.GetRepo(), listed.GetRemote())
	}
}

// OpenSession with a prompt spawns the process without passing the prompt as a
// CLI argument, and instead types the prompt into the terminal once ready.
func TestOpenSessionDeliversPromptToTerminal(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	body := "#!/bin/sh\n" +
		"if [ \"$#\" -gt 0 ]; then\n" +
		"  echo \"unexpected arg: $*\" > args_err\n" +
		"  exit 2\n" +
		"fi\n" +
		"printf 'ready> '\n" +
		"IFS= read -r line\n" +
		"printf '%s' \"$line\" > got\n" +
		"sleep 10\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	d := meshDaemonWith(t, "prompt", func(cfg *config.Config) {
		cfg.Agents = map[string]agentspec.Agent{
			"strict": {Command: []string{script}},
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	info, err := dialDaemon(t, d).Open(ctx, &protocol.OpenSession{
		Agent:   "strict",
		Project: dir,
		Prompt:  "hello from mako",
		Cols:    40,
		Rows:    6,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.closeSession(info.GetId()) })

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dir, "args_err")); err == nil {
			t.Fatalf("prompt was passed as CLI arg: %s", b)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "got")); err == nil && strings.TrimSpace(string(b)) == "hello from mako" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "got"))
	t.Fatalf("timed out waiting for initial prompt in terminal; got=%q", got)
}
