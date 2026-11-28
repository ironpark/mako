package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
	"github.com/ironpark/mako/pkg/rope"
)

func initTempGitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	root := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		// Isolate git config from global/system settings.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
		return string(out)
	}
	runGit("init", "-q", "-b", "main")
	runGit("config", "user.name", "E2E Tester")
	runGit("config", "user.email", "e2e@example.com")
	return root, runGit
}

func TestGitStatTier1(t *testing.T) {
	ctx := context.Background()

	t.Run("status_diff_and_discard_files", func(t *testing.T) {
		root, git := initTempGitRepo(t)
		writeFile := func(rel, content string) {
			t.Helper()
			p := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		writeFile("a.txt", "line1\nline2\n")
		git("add", "a.txt")
		git("commit", "-qm", "initial")

		// Modify tracked file and add untracked file.
		writeFile("a.txt", "line1\nline2-edited\n")
		writeFile("untracked.txt", "temp\n")

		st, err := gitstat.Status(ctx, root)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.GetBranch() != "main" || len(st.GetFiles()) != 2 {
			t.Fatalf("unexpected Status: branch=%q files=%+v", st.GetBranch(), st.GetFiles())
		}

		diff, err := gitstat.Diff(root, "a.txt", "")
		if err != nil || len(diff.GetLines()) == 0 {
			t.Fatalf("Diff: %+v err=%v", diff, err)
		}

		if _, err := gitstat.Run(ctx, &protocol.GitRun{
			Root:  root,
			Op:    protocol.GitOpDiscardFiles,
			Paths: []string{"a.txt", "untracked.txt"},
		}); err != nil {
			t.Fatalf("Run(DiscardFiles): %v", err)
		}

		gotA, err := os.ReadFile(filepath.Join(root, "a.txt"))
		if err != nil || string(gotA) != "line1\nline2\n" {
			t.Fatalf("a.txt after discard = %q, err=%v", string(gotA), err)
		}
		if _, err := os.Stat(filepath.Join(root, "untracked.txt")); !os.IsNotExist(err) {
			t.Fatalf("expected untracked.txt to be removed, stat err=%v", err)
		}
	})

	t.Run("worktree_add_merge_remove", func(t *testing.T) {
		root, git := initTempGitRepo(t)
		if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "base.txt")
		git("commit", "-qm", "base")

		repo, wtPath, _, err := gitstat.AddWorktree(ctx, root, "feat/e2e", "main")
		if err != nil {
			t.Fatalf("AddWorktree: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(wtPath)) })

		if err := os.WriteFile(filepath.Join(wtPath, "base.txt"), []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		wtCommit := exec.Command("git", "-C", wtPath, "commit", "-qam", "feat change")
		wtCommit.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := wtCommit.CombinedOutput(); err != nil {
			t.Fatalf("commit in worktree: %v\n%s", err, string(out))
		}

		wt := &protocol.Worktree{Repo: repo, Path: wtPath, Branch: "feat/e2e", Base: "main"}
		if out, err := gitstat.Merge(ctx, wt); err != nil {
			t.Fatalf("Merge: %v\n%s", err, out)
		}
		got, err := os.ReadFile(filepath.Join(root, "base.txt"))
		if err != nil || string(got) != "v2\n" {
			t.Fatalf("merged base.txt = %q, err=%v", string(got), err)
		}
		if out, err := gitstat.RemoveWorktree(ctx, wt, gitstat.RemoveWorktreeOptions{}); err != nil {
			t.Fatalf("RemoveWorktree: %v\n%s", err, out)
		}
		if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
			t.Fatalf("worktree dir %s still exists after RemoveWorktree", wtPath)
		}
	})
}

func TestGitStatTier2DiscardBoundariesB15(t *testing.T) {
	t.Run("B15_canceled_ctx_preserves_committed_file", func(t *testing.T) {
		root, git := initTempGitRepo(t)
		trackedPath := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(trackedPath, []byte("precious committed content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "tracked.txt")
		git("commit", "-qm", "commit tracked")

		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := gitstat.Run(canceledCtx, &protocol.GitRun{
			Root:  root,
			Op:    protocol.GitOpDiscardFiles,
			Paths: []string{"tracked.txt"},
		})
		if err == nil {
			t.Error("expected GitOpDiscardFiles with canceled context to return error (B15)")
		}
		got, readErr := os.ReadFile(trackedPath)
		if readErr != nil {
			t.Fatalf("committed tracked.txt was deleted on git error (B15): %v", readErr)
		}
		if string(got) != "precious committed content\n" {
			t.Fatalf("tracked.txt content altered: %q", string(got))
		}
	})

	t.Run("B15_ls_tree_failure_preserves_committed_file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX shell wrapper requires non-Windows OS")
		}
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("LookPath(git): %v", err)
		}
		root, git := initTempGitRepo(t)
		trackedPath := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(trackedPath, []byte("precious committed content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "tracked.txt")
		git("commit", "-qm", "commit tracked")

		// Modify tracked.txt in working tree so discardFiles would normally restore it,
		// then shadow git in PATH to fail specifically on `ls-tree` while delegating all other commands.
		if err := os.WriteFile(trackedPath, []byte("modified local work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		binDir := t.TempDir()
		wrapper := filepath.Join(binDir, "git")
		escapedReal := strings.ReplaceAll(realGit, `'`, `'\''`)
		script := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"ls-tree\" ]; then\n    echo \"simulated ls-tree failure\" >&2\n    exit 1\n  fi\ndone\nexec '" + escapedReal + "' \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
			t.Fatalf("write git wrapper: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err = gitstat.Run(context.Background(), &protocol.GitRun{
			Root:  root,
			Op:    protocol.GitOpDiscardFiles,
			Paths: []string{"tracked.txt"},
		})
		if err == nil || !strings.Contains(err.Error(), "simulated ls-tree failure") {
			t.Fatalf("expected GitOpDiscardFiles to fail on ls-tree error, got err=%v (B15)", err)
		}
		got, readErr := os.ReadFile(trackedPath)
		if readErr != nil {
			t.Fatalf("committed tracked.txt was deleted when ls-tree failed (B15): %v", readErr)
		}
		if string(got) != "modified local work\n" {
			t.Fatalf("tracked.txt content altered on ls-tree failure: %q", string(got))
		}
	})

	t.Run("B15_unborn_head_discard_staged_and_untracked", func(t *testing.T) {
		root, git := initTempGitRepo(t)
		stagedPath := filepath.Join(root, "staged.txt")
		untrackedPath := filepath.Join(root, "untracked.txt")
		if err := os.WriteFile(stagedPath, []byte("staged\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(untrackedPath, []byte("untracked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "staged.txt")

		if _, err := gitstat.Run(context.Background(), &protocol.GitRun{
			Root:  root,
			Op:    protocol.GitOpDiscardFiles,
			Paths: []string{"staged.txt", "untracked.txt"},
		}); err != nil {
			t.Fatalf("DiscardFiles on unborn HEAD failed: %v", err)
		}
		if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
			t.Fatalf("expected staged.txt to be removed on unborn HEAD, stat err=%v", err)
		}
		if _, err := os.Stat(untrackedPath); !os.IsNotExist(err) {
			t.Fatalf("expected untracked.txt to be removed on unborn HEAD, stat err=%v", err)
		}
		st, err := gitstat.Status(context.Background(), root)
		if err != nil {
			t.Fatalf("Status after unborn HEAD discard: %v", err)
		}
		if len(st.GetFiles()) != 0 {
			t.Fatalf("expected clean unborn HEAD worktree, got %+v", st.GetFiles())
		}
	})
}

func TestRopeTier1AndTier2(t *testing.T) {
	t.Run("empty_and_boundary_offsets", func(t *testing.T) {
		empty := rope.New("")
		if empty.Len() != 0 || empty.Lines() != 1 || empty.LongestLine() != 0 || empty.String() != "" {
			t.Fatalf("unexpected empty rope metrics: len=%d lines=%d longest=%d", empty.Len(), empty.Lines(), empty.LongestLine())
		}
		if empty.Slice(0, 10) != "" || empty.Line(0) != "" {
			t.Fatalf("unexpected empty slice/line: %q %q", empty.Slice(0, 10), empty.Line(0))
		}
		ins := empty.Insert(0, "hello\nworld")
		if ins.String() != "hello\nworld" || ins.Lines() != 2 || ins.LongestLine() != 5 {
			t.Fatalf("insert into empty: %q lines=%d longest=%d", ins.String(), ins.Lines(), ins.LongestLine())
		}
		delAll := ins.Delete(0, ins.Len())
		if delAll.Len() != 0 || delAll.String() != "" {
			t.Fatalf("delete all: len=%d str=%q", delAll.Len(), delAll.String())
		}
		clampedRep := ins.Replace(-5, 999, "reset")
		if clampedRep.String() != "reset" {
			t.Fatalf("clamped Replace(-5, 999) = %q, want %q", clampedRep.String(), "reset")
		}
		clampedDel := clampedRep.Delete(2, 100)
		if clampedDel.String() != "re" {
			t.Fatalf("clamped Delete(2, 100) = %q, want %q", clampedDel.String(), "re")
		}
	})

	t.Run("multichunk_utf8_edits_and_coordinates", func(t *testing.T) {
		// >12 KiB multi-line UTF-8 content spanning many SumTree chunks (maxChunk=2048).
		var sb strings.Builder
		for i := range 260 {
			sb.WriteString("line-")
			sb.WriteString(strings.Repeat("한글αβ", 4))
			if i%5 == 0 {
				sb.WriteString("-extra")
			}
			sb.WriteByte('\n')
		}
		orig := sb.String()
		if len(orig) <= 10*1024 {
			t.Fatalf("expected >10 KiB test input, got %d bytes", len(orig))
		}

		r := rope.New(orig)
		if r.Len() != len(orig) || r.String() != orig {
			t.Fatalf("initial rope mismatch: len=%d want=%d", r.Len(), len(orig))
		}

		// Verify Insert, Delete, Replace against exact string ground truth.
		mid := len(orig) / 2
		// Align mid to a newline boundary so line math stays clean.
		nl := strings.IndexByte(orig[mid:], '\n')
		mid += nl + 1

		r1 := r.Insert(mid, "INSERTED_한국어\n")
		want1 := orig[:mid] + "INSERTED_한국어\n" + orig[mid:]
		if r1.String() != want1 {
			t.Fatal("Insert mismatch on multi-chunk UTF-8 rope")
		}
		// Original rope remains immutable.
		if r.String() != orig {
			t.Fatal("original rope mutated after Insert")
		}

		delEnd := mid + len("INSERTED_한국어\n") + 40
		r2 := r1.Delete(mid, delEnd)
		want2 := want1[:mid] + want1[delEnd:]
		if r2.String() != want2 {
			t.Fatal("Delete mismatch on multi-chunk UTF-8 rope")
		}

		repStart := 120
		repEnd := len(want2) - 120
		r3 := r2.Replace(repStart, repEnd, "MIDDLE_λ\nSECOND_LINE")
		want3 := want2[:repStart] + "MIDDLE_λ\nSECOND_LINE" + want2[repEnd:]
		if r3.String() != want3 || r3.Len() != len(want3) {
			t.Fatal("Replace mismatch across multi-chunk span")
		}

		// Check Slice, Line, LineStart, LineEnd, LineOf, OffsetToPoint, PointToOffset, LongestLine.
		lines := strings.Split(want3, "\n")
		if r3.Lines() != len(lines) {
			t.Fatalf("Lines() = %d, want %d", r3.Lines(), len(lines))
		}
		maxLineBytes := 0
		offset := 0
		for row, wantLine := range lines {
			if len(wantLine) > maxLineBytes {
				maxLineBytes = len(wantLine)
			}
			if got := r3.Line(row); got != wantLine {
				t.Fatalf("Line(%d) = %q, want %q", row, got, wantLine)
			}
			if gotStart := r3.LineStart(row); gotStart != offset {
				t.Fatalf("LineStart(%d) = %d, want %d", row, gotStart, offset)
			}
			if gotEnd := r3.LineEnd(row); gotEnd != offset+len(wantLine) {
				t.Fatalf("LineEnd(%d) = %d, want %d", row, gotEnd, offset+len(wantLine))
			}
			if gotRow := r3.LineOf(offset); gotRow != row {
				t.Fatalf("LineOf(%d) = %d, want %d", offset, gotRow, row)
			}
			pt := r3.OffsetToPoint(offset + len(wantLine))
			if pt.Row != row || pt.Col != len(wantLine) {
				t.Fatalf("OffsetToPoint(%d) = %+v, want {Row:%d Col:%d}", offset+len(wantLine), pt, row, len(wantLine))
			}
			if back := r3.PointToOffset(pt); back != offset+len(wantLine) {
				t.Fatalf("PointToOffset(%+v) = %d, want %d", pt, back, offset+len(wantLine))
			}
			offset += len(wantLine) + 1
		}
		if r3.LongestLine() != maxLineBytes {
			t.Fatalf("LongestLine() = %d, want %d", r3.LongestLine(), maxLineBytes)
		}
		if gotSlice := r3.Slice(0, r3.Len()); gotSlice != want3 {
			t.Fatal("full Slice mismatch")
		}
	})
}

func TestFsutilWriteTier1AndTier2(t *testing.T) {
	t.Run("atomic_create_overwrite_mode_and_no_temp_leftovers", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "state.json")

		if err := fsutil.Write(target, []byte(`{"v":1}`)); err != nil {
			t.Fatalf("fsutil.Write create: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat target: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("mode = %#o, want 0600", perm)
		}

		if err := fsutil.Write(target, []byte(`{"v":2,"ok":true}`)); err != nil {
			t.Fatalf("fsutil.Write overwrite: %v", err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != `{"v":2,"ok":true}` {
			t.Fatalf("read after overwrite = %q, err=%v", string(got), err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "state.json" {
			t.Fatalf("leftover temp files in dir: %v", entries)
		}
	})

	t.Run("missing_parent_dir_returns_error", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist", "file.txt")
		if err := fsutil.Write(missing, []byte("nope")); err == nil {
			t.Fatal("expected error writing into missing parent directory")
		}
	})

	t.Run("readonly_dir_preserves_existing_file", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("chmod read-only dir check requires non-root POSIX permissions")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "keep.txt")
		if err := fsutil.Write(target, []byte("original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		if err := fsutil.Write(target, []byte("corrupted")); err == nil {
			t.Fatal("expected error writing in read-only directory")
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "original" {
			t.Fatalf("existing file changed after failed write: %q err=%v", string(got), err)
		}
	})
}
