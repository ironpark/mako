//go:build unix

package daemon

import (
	"context"
	"os/exec"
	"testing"
	"time"

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
