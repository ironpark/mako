package gitstat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
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
	st, err := Status(t.Context(), root)
	if err != nil || len(st.GetFiles()) != 0 || len(st.GetStashes()) != 1 || !strings.Contains(st.GetStashes()[0].GetMessage(), "wip") {
		t.Fatalf("after stash: %v %v", st, err)
	}
	op(&protocol.GitRun{Op: protocol.GitOpStashPop, Stash: 0})
	if st, _ = Status(t.Context(), root); len(st.GetFiles()) != 2 || len(st.GetStashes()) != 0 {
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
	st, _ := Status(t.Context(), root)
	if st.GetOperation() != "merge" || len(st.GetFiles()) != 1 || st.GetFiles()[0].GetStatus() != protocol.GitConflicted {
		t.Fatalf("mid-merge status: %v", st)
	}
	must(&protocol.GitRun{Op: protocol.GitOpAbort})
	if st, _ = Status(t.Context(), root); st.GetOperation() != "" || len(st.GetFiles()) != 0 {
		t.Fatalf("after abort: %v", st)
	}

	// Resolved, a merge goes on as a commit.
	_, _ = op(&protocol.GitRun{Op: protocol.GitOpMergeBranch, Branch: "side"})
	write(t, root, "a.txt", "both\n")
	must(&protocol.GitRun{Op: protocol.GitOpContinue})
	if st, _ = Status(t.Context(), root); st.GetOperation() != "" || !strings.Contains(git("log", "-1", "--format=%s"), "Merge") {
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

func TestDiscardFilesPreservesOnCanceledCtxAndGitErrors(t *testing.T) {
	root, git := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-qm", "first")
	write(t, root, "a.txt", "two\n")

	// Canceled context must fail and preserve modified tracked file.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(canceled, &protocol.GitRun{Root: root, Op: protocol.GitOpDiscardFiles, Paths: []string{"a.txt"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx err = %v, want context.Canceled", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(b) != "two\n" {
		t.Fatalf("a.txt altered on canceled ctx: %q, %v", b, err)
	}

	// Non-repository directory must error and preserve files.
	nonRepo := t.TempDir()
	write(t, nonRepo, "keep.txt", "keep\n")
	if _, err := Run(context.Background(), &protocol.GitRun{Root: nonRepo, Op: protocol.GitOpDiscardFiles, Paths: []string{"keep.txt"}}); err == nil {
		t.Fatal("expected error for non-git directory")
	}
	if b, err := os.ReadFile(filepath.Join(nonRepo, "keep.txt")); err != nil || string(b) != "keep\n" {
		t.Fatalf("keep.txt altered in non-repo: %q, %v", b, err)
	}

	// Failing git ls-tree must NOT fall through to os.RemoveAll on tracked files.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"ls-tree\" ]; then\n    echo \"fatal: simulated ls-tree failure\" >&2\n    exit 128\n  fi\ndone\nexec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := Run(context.Background(), &protocol.GitRun{Root: root, Op: protocol.GitOpDiscardFiles, Paths: []string{"a.txt"}}); err == nil {
		t.Fatal("expected error when ls-tree fails")
	}
	if b, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(b) != "two\n" {
		t.Fatalf("tracked file deleted/altered on ls-tree error: %q, %v", b, err)
	}
}

func TestDiscardFilesUnbornHead(t *testing.T) {
	root, git := cliRepo(t)
	write(t, root, "staged.txt", "staged\n")
	git("add", "staged.txt")
	write(t, root, "untracked.txt", "untracked\n")

	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"staged.txt", "untracked.txt"},
	}); err != nil {
		t.Fatalf("unborn HEAD discardFiles failed: %v", err)
	}
	for _, name := range []string{"staged.txt", "untracked.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after discard on unborn HEAD: %v", name, err)
		}
	}
	if cached := strings.TrimSpace(git("ls-files")); cached != "" {
		t.Fatalf("index not cleared on unborn HEAD: %q", cached)
	}
}

func TestDiscardFilesRejectsSymlinkTraversalAndDotGit(t *testing.T) {
	root, git := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-qm", "first")
	write(t, root, "a.txt", "modified\n")

	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Intermediate symlink pointing outside root must be rejected without touching victim or tracked files.
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"a.txt", "sub/victim.txt"},
	}); err == nil {
		t.Fatal("expected error when discarding through an intermediate symlink")
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep\n" {
		t.Fatalf("outside victim altered via symlink traversal: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(b) != "modified\n" {
		t.Fatalf("tracked file altered when batch rejected: %q, %v", b, err)
	}

	// Nested intermediate symlink must also be rejected.
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested", "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"nested/sub/victim.txt"},
	}); err == nil {
		t.Fatal("expected error when discarding through nested intermediate symlink")
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep\n" {
		t.Fatalf("outside victim altered via nested symlink traversal: %q, %v", b, err)
	}

	// .git metadata paths must be rejected and .git/HEAD left intact.
	headBefore, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git", ".git/HEAD", "./.git/config", "sub/.git/HEAD", ".GIT", ".GIT/HEAD", "./.Git/config", "sub/.GIT/HEAD"} {
		if _, err := Run(context.Background(), &protocol.GitRun{
			Root:  root,
			Op:    protocol.GitOpDiscardFiles,
			Paths: []string{p},
		}); err == nil {
			t.Fatalf("expected error for .git metadata path %q", p)
		}
	}
	if headAfter, err := os.ReadFile(filepath.Join(root, ".git", "HEAD")); err != nil || string(headAfter) != string(headBefore) {
		t.Fatalf(".git/HEAD altered: got %q (%v), want %q", headAfter, err, headBefore)
	}

	// Discarding an untracked symlink leaf itself removes only the link, leaving target intact.
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"link"},
	}); err != nil {
		t.Fatalf("discarding symlink leaf failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("symlink leaf still exists: %v", err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep\n" {
		t.Fatalf("outside victim altered when discarding symlink leaf: %q, %v", b, err)
	}

	// Nonexistent nested untracked path behaves cleanly without error.
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"missing_dir/file.txt"},
	}); err != nil {
		t.Fatalf("discarding nonexistent nested path failed: %v", err)
	}
}

func TestDiscardFilesLiteralPathspecs(t *testing.T) {
	root, git := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-qm", "first")
	write(t, root, "a.txt", "modified\n")

	write(t, root, "c.txt", "staged\n")
	git("add", "c.txt")

	// "*" must be treated literally and must NOT unstage c.txt.
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"*"},
	}); err != nil {
		t.Fatalf("discardFiles([*]) failed: %v", err)
	}
	if staged := strings.TrimSpace(git("diff", "--cached", "--name-only")); staged != "c.txt" {
		t.Fatalf("staged files = %q, want %q", staged, "c.txt")
	}
	if b, err := os.ReadFile(filepath.Join(root, "c.txt")); err != nil || string(b) != "staged\n" {
		t.Fatalf("c.txt altered: %q, %v", b, err)
	}

	// ":(top)a.txt" must be treated literally and must NOT revert a.txt.
	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{":(top)a.txt"},
	}); err != nil {
		t.Fatalf("discardFiles([:(top)a.txt]) failed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(b) != "modified\n" {
		t.Fatalf("a.txt reverted via pathspec magic: %q, %v", b, err)
	}
}

func TestDiscardFilesRejectsNonDirectoryParent(t *testing.T) {
	root, git := cliRepo(t)
	write(t, root, "a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-qm", "first")
	write(t, root, "a.txt", "modified\n")
	write(t, root, "regularfile", "keep\n")

	if _, err := Run(context.Background(), &protocol.GitRun{
		Root:  root,
		Op:    protocol.GitOpDiscardFiles,
		Paths: []string{"a.txt", "regularfile/child"},
	}); err == nil {
		t.Fatal("expected error when discarding path through non-directory parent")
	}
	if b, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(b) != "modified\n" {
		t.Fatalf("a.txt altered when batch rejected: %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "regularfile")); err != nil || string(b) != "keep\n" {
		t.Fatalf("regularfile altered when batch rejected: %q, %v", b, err)
	}
}
