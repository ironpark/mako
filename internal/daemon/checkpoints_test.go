package daemon

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// gitRepo is a repository with a.txt committed, and git run in it.
func gitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return root, run
}

// A turn's hook returns once its checkpoint is taken, of the work tree as
// the prompt found it; one turn has one checkpoint.
func TestPromptHooksCheckpointTheirTurnFirst(t *testing.T) {
	project, git := gitRepo(t)
	d, s, socket := hookedDaemonIn(t, project)
	list := func() []gitstat.CheckpointEntry {
		t.Helper()
		l, err := gitstat.ListCheckpoints(context.Background(), project, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	hook := func(input string) {
		t.Helper()
		runHook([]string{socket}, strings.NewReader(input), io.Discard)
	}

	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("as the turn found it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook(`{"hook_event_name":"UserPromptSubmit","prompt":"fix it"}`)
	// No wait: the hook, which the agent waits on, waited for it.
	l := list()
	if len(l) != 1 || l[0].Prompt != gitstat.PromptKey("fix it") || l[0].Kind != gitstat.CheckpointTurn {
		t.Fatalf("checkpoints %+v", l)
	}
	if got := git("show", l[0].Commit+":a.txt"); got != "as the turn found it\n" {
		t.Fatalf("checkpoint holds %q", got)
	}

	// A model call that names no prompt, as agy's PreInvocation, starts
	// no turn while one is under way; a prompt does.
	hook(`{"hook_event_name":"UserPromptSubmit"}`)
	if l := list(); len(l) != 1 {
		t.Fatalf("a prompt-less call of the turn captured again: %+v", l)
	}
	hook(`{"hook_event_name":"UserPromptSubmit","prompt":"and this"}`)
	if l := list(); len(l) != 2 || l[0].Prompt != gitstat.PromptKey("and this") {
		t.Fatalf("a second prompt: %+v", l)
	}
	hook(`{"hook_event_name":"Stop"}`)
	hook(`{"hook_event_name":"UserPromptSubmit"}`)
	if l := list(); len(l) != 3 || l[0].Prompt != "" {
		t.Fatalf("the next turn's first call: %+v", l)
	}

	// Closing the session for good drops its checkpoints.
	if err := d.closeSession(s.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "checkpoints of a closed session kept", func() bool { return len(list()) == 0 })
}

// A project that is no repository has no checkpoints, and its turns do not
// wait for one.
func TestPromptHooksOutsideARepository(t *testing.T) {
	project := t.TempDir()
	_, s, socket := hookedDaemonIn(t, project)
	runHook([]string{socket}, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`), io.Discard)
	if _, err := os.Stat(filepath.Join(project, ".git")); !os.IsNotExist(err) {
		t.Fatalf("a checkpoint made a repository: %v", err)
	}
	if s.Info().GetStatus() != protocol.AgentWorking {
		t.Fatalf("status %v", s.Info().GetStatus())
	}
}
