package gitstat

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// cliRepo makes an empty repository with the git program, which the
// history and its operations need.
func cliRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "T")
	run("config", "user.email", "t@example.com")
	run("config", "commit.gpgsign", "false")
	return root, run
}

func TestLogShowAndCommitDiff(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	if h, err := Log(ctx, root, 0, ""); err != nil || len(h.GetCommits()) != 0 || h.GetCurrent() != "main" {
		t.Fatalf("empty log=%+v %v", h, err)
	}
	write(t, root, "a.txt", "one\ntwo\n")
	write(t, root, "old.txt", "keep\nkeep\nkeep\n")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	write(t, root, "a.txt", "one\nTWO\nthree\n")
	run("mv", "old.txt", "new.txt")
	write(t, root, "bin.dat", "\x00\x01")
	run("add", "-A")
	run("commit", "-q", "-m", "second\n\nthe body")
	run("tag", "v1")

	h, err := Log(ctx, root, 0, "")
	if err != nil || len(h.GetCommits()) != 2 || h.GetMore() {
		t.Fatalf("log=%+v %v", h, err)
	}
	top := h.GetCommits()[0]
	if top.GetSubject() != "second" || len(top.GetParents()) != 1 || top.GetParents()[0] != h.GetCommits()[1].GetHash() || top.GetAuthor() != "T" || !slices.Contains(top.GetRefs(), "tag: v1") {
		t.Fatalf("top=%+v", top)
	}
	if h, _ := Log(ctx, root, 1, ""); len(h.GetCommits()) != 1 || !h.GetMore() {
		t.Fatalf("capped log=%+v", h)
	}
	if again, _ := Log(ctx, root, 0, h.GetFingerprint()); !again.GetUnchanged() || again.GetCommits() != nil {
		t.Fatalf("an unchanged history read again: %+v", again)
	}

	info, err := Show(ctx, root, top.GetHash())
	if err != nil || info.GetBody() != "the body" || info.GetEmail() != "t@example.com" {
		t.Fatalf("show=%+v %v", info, err)
	}
	want := []*protocol.GitFile{
		{Path: "a.txt", Status: protocol.GitModified, Added: 2, Removed: 1},
		{Path: "bin.dat", Status: protocol.GitAdded, Binary: true},
		{Path: "new.txt", From: "old.txt", Status: protocol.GitRenamed},
	}
	if !protocol.Equal(info.GetFiles(), want) {
		t.Fatalf("files=%+v", info.GetFiles())
	}
	root0, _ := Show(ctx, root, h.GetCommits()[1].GetHash())
	if len(root0.GetFiles()) != 2 || root0.GetFiles()[0].GetStatus() != protocol.GitAdded || root0.GetFiles()[0].GetAdded() != 2 {
		t.Fatalf("root commit files=%+v", root0.GetFiles())
	}

	d, err := CommitDiff(ctx, root, top.GetHash(), "a.txt", "")
	var ops string
	for _, l := range d.GetLines() {
		ops += l.GetOp()
	}
	if err != nil || ops != "@ -++" {
		t.Fatalf("diff=%+v %v", d, err)
	}
	if d, _ := CommitDiff(ctx, root, top.GetHash(), "bin.dat", ""); !d.GetBinary() {
		t.Fatal("binary not told")
	}
	if _, err := Show(ctx, root, "HEAD; rm -rf"); err == nil {
		t.Fatal("not a hash, but shown")
	}
}

func TestRunCommitsBranchesAndPushes(t *testing.T) {
	root, run := cliRepo(t)
	ctx := context.Background()
	write(t, root, "a.txt", "a\n")
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCommit, Message: "  "}); err == nil {
		t.Fatal("committed without a message")
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCommit, Message: "all"}); err != nil {
		t.Fatal(err)
	}
	// Only the paths named are committed.
	write(t, root, "a.txt", "a2\n")
	write(t, root, "b.txt", "b\n")
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCommit, Message: "just b", Paths: []string{"b.txt"}}); err != nil {
		t.Fatal(err)
	}
	if st := strings.TrimSpace(run("status", "--porcelain")); st != "M a.txt" {
		t.Fatalf("status after a partial commit: %q", st)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCommit, Message: "x", Paths: []string{"../escape"}}); err == nil {
		t.Fatal("a path outside the work tree")
	}

	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpBranch, Branch: "feature"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCheckout, Branch: "--orphan"}); err == nil {
		t.Fatal("an option taken as a branch")
	}
	if h, _ := Log(ctx, root, 0, ""); h.GetCurrent() != "feature" {
		t.Fatalf("current=%+v", h)
	}
	if st, _ := Status(root); st.GetBranch() != "feature" || !slices.Equal(st.GetBranches(), []string{"feature", "main"}) {
		t.Fatalf("branches=%+v", st)
	}

	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpPush}); err == nil {
		t.Fatal("pushed with no remote")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	run("remote", "add", "origin", remote)
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpPush}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpCommit, Message: "ahead", Paths: []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	st, err := Status(root)
	if err != nil || st.GetUpstream() != "origin/feature" || st.GetAhead() != 1 || st.GetBehind() != 0 {
		t.Fatalf("tracking=%+v %v", st, err)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpPush}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, &protocol.GitRun{Root: root, Op: protocol.GitOpFetch}); err != nil {
		t.Fatal(err)
	}
	if st, _ := Status(root); st.GetAhead() != 0 {
		t.Fatalf("still ahead after push: %+v", st)
	}
}
