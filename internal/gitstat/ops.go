package gitstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// The operations a front-end runs on a work tree beyond committing and
// syncing: stashes, discarding and ignoring files, managing branches, and
// what a commit in the history offers. Each runs git itself, so hooks,
// credentials and config apply as they do in a terminal.

// commitArg checks a commit an operation names.
func commitArg(c string) (string, error) {
	if !hashPattern.MatchString(c) {
		return "", fmt.Errorf("%q is not a commit", c)
	}
	return c, nil
}

// branchArg checks a branch name an operation is given.
func branchArg(ctx context.Context, root, name string) (string, error) {
	if _, err := gitOut(ctx, root, nil, "check-ref-format", "--branch", name); err != nil || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%q is not a branch name", name)
	}
	return name, nil
}

// stashArg is the stash an operation is of.
func stashArg(i int64) (string, error) {
	if i < 0 {
		return "", errors.New("no stash named")
	}
	return fmt.Sprintf("stash@{%d}", i), nil
}

// runOp runs the operations of this file, reporting false for one that is
// not.
func runOp(ctx context.Context, r *protocol.GitRun) (string, bool, error) {
	root := r.GetRoot()
	var out string
	var err error
	switch r.GetOp() {
	case protocol.GitOpDiscardFiles:
		err = discardFiles(ctx, root, r.GetPaths())
	case protocol.GitOpIgnore:
		err = ignore(root, r.GetPaths())
	case protocol.GitOpStash:
		args := []string{"stash", "push", "--include-untracked"}
		if msg := strings.TrimSpace(r.GetMessage()); msg != "" {
			args = append(args, "-m", msg)
		}
		out, err = run(ctx, root, nil, args...)
	case protocol.GitOpStashApply, protocol.GitOpStashPop, protocol.GitOpStashDrop:
		var ref string
		if ref, err = stashArg(r.GetStash()); err == nil {
			out, err = run(ctx, root, nil, "stash", strings.TrimPrefix(r.GetOp(), "stash_"), ref)
		}
	case protocol.GitOpTrack:
		var b string
		if b, err = branchArg(ctx, root, r.GetBranch()); err == nil {
			out, err = run(ctx, root, nil, "switch", "--track", b)
		}
	case protocol.GitOpDeleteBranch:
		var b string
		if b, err = branchArg(ctx, root, r.GetBranch()); err == nil {
			out, err = run(ctx, root, nil, "branch", pick(r.GetForce(), "-D", "-d"), "--", b)
		}
	case protocol.GitOpMergeBranch:
		var b string
		if b, err = branchArg(ctx, root, r.GetBranch()); err == nil {
			out, err = run(ctx, root, nil, "merge", "--no-edit", b)
		}
	case protocol.GitOpRebase:
		var b string
		if b, err = branchArg(ctx, root, r.GetBranch()); err == nil {
			out, err = run(ctx, root, nil, "rebase", b)
		}
	case protocol.GitOpDetach, protocol.GitOpCherryPick, protocol.GitOpRevert:
		var c string
		if c, err = commitArg(r.GetCommit()); err == nil {
			switch r.GetOp() {
			case protocol.GitOpDetach:
				out, err = run(ctx, root, nil, "switch", "--detach", c)
			case protocol.GitOpCherryPick:
				out, err = run(ctx, root, nil, "cherry-pick", c)
			default:
				out, err = run(ctx, root, nil, "revert", "--no-edit", c)
			}
		}
	case protocol.GitOpTag:
		out, err = tag(ctx, r)
	case protocol.GitOpContinue, protocol.GitOpAbort:
		out, err = resume(ctx, root, r.GetOp() == protocol.GitOpAbort)
	default:
		return "", false, nil
	}
	return out, true, err
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

// discardFiles throws away the changes of paths: a tracked file goes back
// to HEAD's, from the index too, and one HEAD does not have is removed.
func discardFiles(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no files named")
	}
	var tracked, untracked []string
	for _, p := range paths {
		if err := inWorkTree(p); err != nil || p == "" {
			return orErr(err, errors.New("an empty path"))
		}
		if out, _ := gitOut(ctx, root, nil, "ls-tree", "--name-only", "HEAD", "--", p); len(bytes.TrimSpace(out)) > 0 {
			tracked = append(tracked, p)
		} else {
			untracked = append(untracked, p)
		}
	}
	if len(tracked) > 0 {
		if _, err := gitOut(ctx, root, nil, append([]string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}, tracked...)...); err != nil {
			return err
		}
	}
	for _, p := range untracked {
		// New to the index, or never added: out of both.
		_, _ = gitOut(ctx, root, nil, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", p)
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	return nil
}

func orErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// ignore adds paths to the .gitignore at the root, each anchored there.
func ignore(root string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no files named")
	}
	file := filepath.Join(root, ".gitignore")
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(old), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var b strings.Builder
	b.Write(old)
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		b.WriteByte('\n')
	}
	for _, p := range paths {
		if err := inWorkTree(p); err != nil || p == "" || strings.ContainsAny(p, "\n\r") {
			return orErr(err, fmt.Errorf("%q cannot be ignored", p))
		}
		line := "/" + strings.TrimPrefix(p, "/")
		if !have[line] {
			b.WriteString(line + "\n")
			have[line] = true
		}
	}
	return os.WriteFile(file, []byte(b.String()), 0o644)
}

// tag makes Tag at Commit, annotated when there is a message.
func tag(ctx context.Context, r *protocol.GitRun) (string, error) {
	name := r.GetTag()
	if _, err := gitOut(ctx, r.GetRoot(), nil, "check-ref-format", "refs/tags/"+name); err != nil || name == "" || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%q is not a tag name", name)
	}
	c, err := commitArg(r.GetCommit())
	if err != nil {
		return "", err
	}
	if msg := strings.TrimSpace(r.GetMessage()); msg != "" {
		return run(ctx, r.GetRoot(), []byte(msg+"\n"), "tag", "-a", "-F", "-", "--", name, c)
	}
	return run(ctx, r.GetRoot(), nil, "tag", "--", name, c)
}

// resume carries on with the operation under way, or abandons it.
func resume(ctx context.Context, root string, abort bool) (string, error) {
	op := Operation(ctx, root)
	switch {
	case op == "":
		return "", errors.New("no merge, rebase, cherry-pick or revert is under way")
	case abort:
		return run(ctx, root, nil, op, "--abort")
	case op == "merge":
		// A merge goes on as a commit of what was resolved.
		if _, err := gitOut(ctx, root, nil, "add", "-u"); err != nil {
			return "", err
		}
		return run(ctx, root, nil, "commit", "--no-edit")
	}
	if _, err := gitOut(ctx, root, nil, "add", "-u"); err != nil {
		return "", err
	}
	return run(ctx, root, nil, op, "--continue")
}

// Operation is the merge, rebase, cherry-pick or revert under way in the
// work tree at root, "" for none.
func Operation(ctx context.Context, root string) string {
	out, err := gitOut(ctx, root, nil, "rev-parse", "--git-path", "MERGE_HEAD", "--git-path", "rebase-merge",
		"--git-path", "rebase-apply", "--git-path", "CHERRY_PICK_HEAD", "--git-path", "REVERT_HEAD")
	if err != nil {
		return ""
	}
	ops := []string{"merge", "rebase", "rebase", "cherry-pick", "revert"}
	for i, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i >= len(ops) {
			break
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		if _, err := os.Stat(p); err == nil {
			return ops[i]
		}
	}
	return ""
}

// conflicted are the files the operation under way left unmerged.
func conflicted(ctx context.Context, root string) map[string]bool {
	out, err := gitOut(ctx, root, nil, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil
	}
	files := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files[p] = true
		}
	}
	return files
}

// stashes lists the work tree's stashes, the newest first.
func stashes(ctx context.Context, root string) []*protocol.GitStash {
	out, err := gitOut(ctx, root, nil, "stash", "list", "--format=%gd%x00%ct%x00%gs")
	if err != nil {
		return nil
	}
	var list []*protocol.GitStash
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(l, "\x00", 3)
		if len(f) < 3 {
			continue
		}
		i, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(f[0], "stash@{"), "}"), 10, 64)
		if err != nil {
			continue
		}
		at, _ := strconv.ParseInt(f[1], 10, 64)
		list = append(list, &protocol.GitStash{Index: i, Message: f[2], Time: protocol.Time(time.Unix(at, 0))})
	}
	return list
}
