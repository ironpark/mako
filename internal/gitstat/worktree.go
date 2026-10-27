package gitstat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// A session can work in a git worktree of its own, on a branch made for it,
// so agents working on one repository at once do not write over each
// other's files. The worktree sits beside the repository, in a folder
// named after it: ../<repo>.worktrees/<branch>.

// AddWorktree makes a worktree for branch, new, from base ("" for HEAD), in
// the repository dir is in, and returns the repository's main work tree
// and the new worktree's path.
func AddWorktree(ctx context.Context, dir, branch, base string) (repo, path string, err error) {
	if err := checkBranch(ctx, dir, branch); err != nil {
		return "", "", err
	}
	if base != "" && strings.HasPrefix(base, "-") {
		return "", "", fmt.Errorf("%q is not a branch or commit", base)
	}
	// The main work tree, even from inside another worktree.
	common, err := gitOut(ctx, dir, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	repo = filepath.Dir(strings.TrimSpace(string(common)))
	path = filepath.Join(filepath.Dir(repo), filepath.Base(repo)+".worktrees", strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(path); err == nil {
		return "", "", fmt.Errorf("%s already exists", path)
	}
	args := []string{"worktree", "add", "-b", branch, path}
	if base != "" {
		args = append(args, base)
	}
	if _, err := run(ctx, repo, nil, args...); err != nil {
		return "", "", err
	}
	return repo, path, nil
}

// checkBranch refuses what is not a name a new branch can have.
func checkBranch(ctx context.Context, dir, branch string) error {
	if _, err := gitOut(ctx, dir, nil, "check-ref-format", "--branch", branch); err != nil || branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%q is not a branch name", branch)
	}
	return nil
}

// RemoveWorktree removes a session's worktree, which must have no changes
// left, and its branch once merged.
func RemoveWorktree(ctx context.Context, w *protocol.Worktree) (string, error) {
	if w.GetPath() == "" || w.GetRepo() == "" {
		return "", errors.New("no worktree to remove")
	}
	out, err := run(ctx, w.GetRepo(), nil, "worktree", "remove", w.GetPath())
	if err != nil {
		return out, err
	}
	// A branch with work not merged anywhere stays: -d refuses it.
	if more, err := run(ctx, w.GetRepo(), nil, "branch", "-d", w.GetBranch()); err == nil {
		out = strings.TrimSpace(out + "\n" + more)
	}
	return out, nil
}

// Merge merges a session's branch into the branch checked out in the main
// work tree, which must be the one the worktree started from.
func Merge(ctx context.Context, w *protocol.Worktree) (string, error) {
	if err := checkBranch(ctx, w.GetRepo(), w.GetBranch()); err != nil {
		return "", err
	}
	head, err := gitOut(ctx, w.GetRepo(), nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if cur := strings.TrimSpace(string(head)); w.GetBase() != "" && cur != w.GetBase() {
		return "", fmt.Errorf("the repository has %s checked out, not %s; check out %s there to merge into it", cur, w.GetBase(), w.GetBase())
	}
	return run(ctx, w.GetRepo(), nil, "merge", "--no-ff", "--no-edit", w.GetBranch())
}

// PullRequest pushes a session's branch and opens a pull request of it into
// its base, through the GitHub CLI when the node has it; without it, it
// reports where to open one.
func PullRequest(ctx context.Context, w *protocol.Worktree) (string, error) {
	pushed, err := push(ctx, w.GetPath())
	if err != nil {
		return pushed, err
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return strings.TrimSpace(pushed + "\nPushed. Open a pull request of " + w.GetBranch() + " on its remote; the GitHub CLI (gh) is not installed here."), nil
	}
	args := []string{"pr", "create", "--fill", "--head", w.GetBranch()}
	if w.GetBase() != "" {
		args = append(args, "--base", w.GetBase())
	}
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Dir = w.GetPath()
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, cmp.Or(errors.New(text), err)
	}
	return text, nil
}

// mergeBase is where the work tree at root left base.
func mergeBase(ctx context.Context, root, base string) (string, error) {
	if strings.HasPrefix(base, "-") || base == "" {
		return "", fmt.Errorf("%q is not a branch or commit", base)
	}
	out, err := gitOut(ctx, root, nil, "merge-base", base, "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BaseStatus is the work tree's changes since it left base: its commits
// since and what it has not committed, untracked files included.
func BaseStatus(ctx context.Context, root, base string) (*protocol.GitChanges, error) {
	mb, err := mergeBase(ctx, root, base)
	if err != nil {
		return nil, err
	}
	names, err := gitOut(ctx, root, nil, "diff", "-M", "--name-status", "-z", mb)
	if err != nil {
		return nil, err
	}
	nums, err := gitOut(ctx, root, nil, "diff", "-M", "--numstat", "-z", mb)
	if err != nil {
		return nil, err
	}
	out := &protocol.GitChanges{Root: root, Branch: Branch(root), Files: treeChanges(string(names), string(nums))}
	untracked, err := gitOut(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err == nil {
		for _, p := range strings.Split(string(untracked), "\x00") {
			if p == "" {
				continue
			}
			f := &protocol.GitFile{Path: p, Status: protocol.GitAdded}
			if cur, ok := diskContent(root, p); !ok || Binary(cur) {
				f.Binary = true
			} else {
				f.Added = int64(lineCount(cur))
			}
			out.Files = append(out.Files, f)
		}
	}
	return out, nil
}

// BaseDiff is one file's changes since the work tree left base.
func BaseDiff(ctx context.Context, root, base, p, from string) (*protocol.Diff, error) {
	if err := cmp.Or(inWorkTree(p), inWorkTree(from)); err != nil {
		return nil, err
	}
	mb, err := mergeBase(ctx, root, base)
	if err != nil {
		return nil, err
	}
	old := blob(ctx, root, mb, cmp.Or(from, p))
	cur, _ := diskContent(root, p)
	out := &protocol.Diff{Path: p}
	if Binary(old) || Binary(cur) {
		out.Binary = true
		return out, nil
	}
	out.Lines, out.Truncated = unified(old, cur)
	return out, nil
}

// DiscardHunk undoes one hunk of a file's changes against HEAD: the lines
// the hunk added or changed go back to what HEAD has. The file must still
// read as the hunk says it does there, or nothing is written.
func DiscardHunk(root, p string, hunk []*protocol.DiffLine) error {
	if err := inWorkTree(p); err != nil || p == "" {
		return cmp.Or(err, errors.New("an empty path"))
	}
	var cur, old []string
	start := 0
	for _, l := range hunk {
		switch l.GetOp() {
		case " ", "+":
			if start == 0 {
				start = int(l.GetNew())
			}
			cur = append(cur, l.GetText())
			if l.GetOp() == " " {
				old = append(old, l.GetText())
			}
		case "-":
			old = append(old, l.GetText())
		}
	}
	full := filepath.Join(root, filepath.FromSlash(p))
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	text := string(data)
	trailing := strings.HasSuffix(text, "\n")
	lines := splitLines(text)
	if text == "" {
		lines = nil
	}
	if start == 0 {
		// A hunk that only removed lines keeps none here: they go back
		// after the line before them.
		for _, l := range hunk {
			if l.GetOp() == "-" && l.GetOld() > 0 {
				start = int(l.GetOld())
				break
			}
		}
		// Lines removed from the end, or the whole file, go back at the end.
		start = min(start, len(lines)+1)
	}
	at := start - 1
	if at < 0 || at+len(cur) > len(lines) {
		return errors.New("the file changed since its diff was read")
	}
	for i, l := range cur {
		if lines[at+i] != l {
			return errors.New("the file changed since its diff was read")
		}
	}
	next := append(append(append([]string{}, lines[:at]...), old...), lines[at+len(cur):]...)
	out := strings.Join(next, "\n")
	if trailing || (len(next) > 0 && len(lines) == 0) {
		out += "\n"
	}
	return os.WriteFile(full, []byte(out), info.Mode().Perm())
}
