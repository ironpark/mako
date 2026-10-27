package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestStashDiscardAndIgnore(t *testing.T) {
	root, git := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "one\n")
	git("add", ".")
	git("commit", "-qm", "first")
	op := func(r *protocol.GitRun) {
		t.Helper()
		r.Root = root
		if _, err := Run(ctx, r); err != nil {
			t.Fatalf("%s: %v", r.GetOp(), err)
		}
	}

	// A stash takes changes and new files, and pops them back.
	write(t, root, "a.txt", "two\n")
	write(t, root, "new.txt", "new\n")
	op(&protocol.GitRun{Op: protocol.GitOpStash, Message: "wip"})
	st, err := Status(root)
	if err != nil || len(st.GetFiles()) != 0 || len(st.GetStashes()) != 1 || !strings.Contains(st.GetStashes()[0].GetMessage(), "wip") {
		t.Fatalf("after stash: %v %v", st, err)
	}
	op(&protocol.GitRun{Op: protocol.GitOpStashPop, Stash: 0})
	if st, _ = Status(root); len(st.GetFiles()) != 2 || len(st.GetStashes()) != 0 {
		t.Fatalf("after pop: %v", st)
	}

	// Discarding puts a tracked file back and removes a new one.
	op(&protocol.GitRun{Op: protocol.GitOpDiscardFiles, Paths: []string{"a.txt", "new.txt"}})
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "one\n" {
		t.Fatalf("a.txt %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt is still there")
	}

	// Ignoring writes an anchored line, once.
	write(t, root, "build/out.bin", "x")
	op(&protocol.GitRun{Op: protocol.GitOpIgnore, Paths: []string{"build"}})
	op(&protocol.GitRun{Op: protocol.GitOpIgnore, Paths: []string{"build"}})
	if b, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(b) != "/build\n" {
		t.Fatalf(".gitignore %q", b)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpDiscardFiles, Paths: []string{"../x"}}); err == nil {
		t.Fatal("discarded outside the work tree")
	}
}

func TestBranchesCommitsAndAConflict(t *testing.T) {
	root, git := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "base\n")
	git("add", ".")
	git("commit", "-qm", "base")
	base := strings.TrimSpace(git("rev-parse", "HEAD"))
	op := func(r *protocol.GitRun) (string, error) {
		r.Root = root
		return Run(ctx, r)
	}
	must := func(r *protocol.GitRun) {
		t.Helper()
		if _, err := op(r); err != nil {
			t.Fatalf("%s: %v", r.GetOp(), err)
		}
	}

	// A branch from a commit, a commit on it, merged into main.
	must(&protocol.GitRun{Op: protocol.GitOpBranch, Branch: "side", Commit: base})
	write(t, root, "a.txt", "side\n")
	git("commit", "-qam", "side change")
	git("switch", "-q", "main")
	write(t, root, "a.txt", "main\n")
	git("commit", "-qam", "main change")
	if _, err := op(&protocol.GitRun{Op: protocol.GitOpMergeBranch, Branch: "side"}); err == nil {
		t.Fatal("a conflicting merge succeeded")
	}
	st, _ := Status(root)
	if st.GetOperation() != "merge" || len(st.GetFiles()) != 1 || st.GetFiles()[0].GetStatus() != protocol.GitConflicted {
		t.Fatalf("mid-merge status: %v", st)
	}
	must(&protocol.GitRun{Op: protocol.GitOpAbort})
	if st, _ = Status(root); st.GetOperation() != "" || len(st.GetFiles()) != 0 {
		t.Fatalf("after abort: %v", st)
	}

	// Resolved, a merge goes on as a commit.
	_, _ = op(&protocol.GitRun{Op: protocol.GitOpMergeBranch, Branch: "side"})
	write(t, root, "a.txt", "both\n")
	must(&protocol.GitRun{Op: protocol.GitOpContinue})
	if st, _ = Status(root); st.GetOperation() != "" || !strings.Contains(git("log", "-1", "--format=%s"), "Merge") {
		t.Fatalf("after continue: %v", st)
	}

	// A tag, a revert, a detached checkout, and deleting the branch.
	head := strings.TrimSpace(git("rev-parse", "HEAD"))
	must(&protocol.GitRun{Op: protocol.GitOpTag, Tag: "v1", Commit: head, Message: "first"})
	if strings.TrimSpace(git("tag")) != "v1" {
		t.Fatal("no tag")
	}
	write(t, root, "b.txt", "extra\n")
	git("add", "b.txt")
	git("commit", "-qm", "extra")
	must(&protocol.GitRun{Op: protocol.GitOpRevert, Commit: strings.TrimSpace(git("rev-parse", "--short", "HEAD"))})
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("the revert left b.txt")
	}
	must(&protocol.GitRun{Op: protocol.GitOpDetach, Commit: base})
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "base\n" {
		t.Fatalf("detached a.txt %q", b)
	}
	git("switch", "-q", "main")
	must(&protocol.GitRun{Op: protocol.GitOpDeleteBranch, Branch: "side"})
	if strings.Contains(git("branch"), "side") {
		t.Fatal("side is still there")
	}
	if _, err := op(&protocol.GitRun{Op: protocol.GitOpDetach, Commit: "--help"}); err == nil {
		t.Fatal("an option passed for a commit")
	}
}
