package gitstat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Worktrees of one repository made at once, as an isolated team's or
// spawn_agent's, bootstrap one at a time: no two setup_cmds run together
// over the paths they share.
func TestWorktreesOfARepoBootstrapOneAtATime(t *testing.T) {
	setup := `if mkdir "$MAKO_WORKTREE_REPO/.busy" 2>/dev/null; then sleep 0.3; rmdir "$MAKO_WORKTREE_REPO/.busy"; else echo "$MAKO_WORKTREE_BRANCH" >> "$MAKO_WORKTREE_REPO/overlap"; fi`
	root, _ := bootstrapRepo(t, fmt.Sprintf("setup_cmd = %q\n", setup), "")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			made, err := AddWorktree(context.Background(), root, NewWorktree{Branch: fmt.Sprintf("w%d", i), Start: "main", Trusted: trustAll})
			errs[i] = err
			if err == nil {
				t.Cleanup(func() { os.RemoveAll(filepath.Dir(made.Path)) })
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "overlap")); err == nil {
		t.Fatalf("setup_cmds ran together, for %q", b)
	}
}

// Waiting for a repository another worktree is being made in ends with
// the caller's context.
func TestLockRepoEndsWithTheContext(t *testing.T) {
	repo := t.TempDir()
	unlock, err := lockRepo(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := lockRepo(ctx, repo); err == nil {
		t.Fatal("took a repository already held")
	}
	unlock()
	again, err := lockRepo(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	again()
}
