package gitstat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A session can work in a git worktree of its own, on a branch made for it,
// so agents working on one repository at once do not write over each
// other's files. The worktree sits beside the repository, in a folder
// named after it: ../<repo>.worktrees/<branch>.

// WorktreeTimeout is how long making a worktree can take, the
// repository's setup_cmd included, which itself has two minutes: a
// front-end waits at least this long for a session that starts in one.
const WorktreeTimeout = 3 * time.Minute

// rollbackTimeout is how long undoing a worktree that failed to bootstrap
// can take, even after the request it was made for ended.
const rollbackTimeout = 30 * time.Second

// NewWorktree is a worktree for AddWorktree to make.
type NewWorktree struct {
	// Branch is the new branch; "" names one after the repository and a
	// short random ID, such as mako-k3x9q2.
	Branch string
	// Start is the branch or commit it starts at; "" for the repository's
	// base_ref, or without one dir's HEAD.
	Start string
	// Trusted says whether the repository's setup_cmd may run; nil runs
	// none.
	Trusted HookTrust
}

// MadeWorktree is the worktree AddWorktree made.
type MadeWorktree struct {
	Repo   string // the repository's main work tree
	Path   string
	Branch string
	// Start is the branch or commit it started at, as NewWorktree or the
	// repository's base_ref named it; "" for dir's HEAD.
	Start string
	// Base is the local branch it came off, which it merges into and
	// opens its pull request against: Start's, origin/main's being main;
	// or, for a commit or tag, or no Start, the branch dir has checked
	// out. "" when there is none.
	Base string
	// SkippedHooks is the repository's command file, as an absolute path,
	// whose setup_cmd did not run because the user does not trust it as it
	// reads; "" when none was skipped.
	SkippedHooks string
}

// AddWorktree makes a worktree on a new branch in the repository dir is
// in, beside its main work tree, and bootstraps it as the repository's
// .mako/worktree.toml says.
func AddWorktree(ctx context.Context, dir string, w NewWorktree) (MadeWorktree, error) {
	branch, base := w.Branch, w.Start
	if branch != "" {
		if err := checkBranch(ctx, dir, branch); err != nil {
			return MadeWorktree{}, err
		}
	}
	if base != "" && strings.HasPrefix(base, "-") {
		return MadeWorktree{}, fmt.Errorf("%q is not a branch or commit", base)
	}
	// The main work tree, even from inside another worktree.
	common, err := gitOut(ctx, dir, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return MadeWorktree{}, err
	}
	repo := filepath.Dir(strings.TrimSpace(string(common)))
	cfg, err := LoadWorktreeConfig(repo)
	if err != nil {
		return MadeWorktree{}, err
	}
	// What the caller names wins over what the repository's config says.
	if base == "" {
		base = strings.TrimSpace(cfg.BaseRef)
	}
	parent := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+".worktrees")
	if branch == "" {
		if branch, err = freeBranch(ctx, repo, parent); err != nil {
			return MadeWorktree{}, err
		}
	}
	path := filepath.Join(parent, strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(path); err == nil {
		return MadeWorktree{}, fmt.Errorf("%s already exists", path)
	}
	args := []string{"worktree", "add", "-b", branch, path}
	if base != "" {
		args = append(args, base)
	}
	// From dir, so that without a start the worktree starts at dir's HEAD,
	// the branch it records as its base, even when dir is a worktree.
	if _, err := run(ctx, dir, nil, args...); err != nil {
		return MadeWorktree{}, err
	}
	from := cmp.Or(localBranch(ctx, repo, base), Branch(dir))
	skipped, err := applyWorktreeBootstrap(ctx, repo, path, branch, base, cfg, w.Trusted)
	if err != nil {
		// Roll back the newly created worktree and branch so failed setup
		// hooks do not leave orphaned broken worktrees behind; on a context
		// of its own, since ctx ending is often why the setup failed.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_, _ = run(rctx, repo, nil, "worktree", "remove", "--force", path)
		_, _ = run(rctx, repo, nil, "branch", "-D", branch)
		return MadeWorktree{}, err
	}
	made := MadeWorktree{Repo: repo, Path: path, Branch: branch, Start: base, Base: from}
	if skipped {
		made.SkippedHooks = filepath.Join(repo, filepath.FromSlash(WorktreeConfigFile))
	}
	return made, nil
}

// localBranch is the local branch name ref is, or follows on a remote:
// main for main or origin/main; "" for a commit, a tag, or "".
func localBranch(ctx context.Context, repo, ref string) string {
	if ref == "" {
		return ""
	}
	out, err := gitOut(ctx, repo, nil, "rev-parse", "--symbolic-full-name", ref)
	if err != nil {
		return ""
	}
	full := strings.TrimSpace(string(out))
	if name, ok := strings.CutPrefix(full, "refs/heads/"); ok {
		return name
	}
	rest, ok := strings.CutPrefix(full, "refs/remotes/")
	if !ok {
		return ""
	}
	remotes, err := gitOut(ctx, repo, nil, "remote")
	if err != nil {
		return ""
	}
	// The longest remote it is under, as a remote's name can have a slash.
	name := ""
	for r := range strings.Lines(string(remotes)) {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		if n, ok := strings.CutPrefix(rest, r+"/"); ok && (name == "" || len(n) < len(name)) {
			name = n
		}
	}
	if name == "HEAD" {
		return ""
	}
	return name
}

// idAlphabet is what a worktree's random ID is made of, as nanoid makes
// its IDs of an alphabet: lowercase letters and digits, which git takes in
// a branch and every file system in a folder's name, told apart even where
// case is not.
const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// idLen is how long a worktree's random ID is: 36^6, about two billion
// IDs, which a repository's worktrees do not run out of.
const idLen = 6

// shortID is a random ID of idLen characters of idAlphabet.
func shortID() string {
	b := make([]byte, idLen)
	for i := range b {
		b[i] = idAlphabet[rand.IntN(len(idAlphabet))]
	}
	return string(b)
}

// freeBranch names a new worktree's branch after repo and a short random
// ID, one no branch and no folder in parent has yet.
func freeBranch(ctx context.Context, repo, parent string) (string, error) {
	name := branchStem(filepath.Base(repo))
	for range 8 {
		b := name + "-" + shortID()
		if _, err := os.Stat(filepath.Join(parent, b)); err == nil {
			continue
		}
		if _, err := gitOut(ctx, repo, nil, "show-ref", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			continue
		}
		return b, nil
	}
	return "", errors.New("no free name for the worktree's branch")
}

// nonBranch is a run of what a worktree's branch does not take of its
// repository's folder name.
var nonBranch = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// branchStem is a repository's folder name as a branch begins with it:
// each run of what is not a letter, a digit or _ becomes one -, and a name
// with nothing left is "worktree".
func branchStem(name string) string {
	return cmp.Or(strings.Trim(nonBranch.ReplaceAllString(name, "-"), "-"), "worktree")
}

// checkBranch refuses what is not a name a new branch can have.
func checkBranch(ctx context.Context, dir, branch string) error {
	if _, err := gitOut(ctx, dir, nil, "check-ref-format", "--branch", branch); err != nil || branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%q is not a branch name", branch)
	}
	return nil
}

// RemoveWorktree removes a session's worktree, which must have no changes
// left, and its branch once merged. It refuses a worktree with changes
// before anything else, so one it refuses is left as it was. Then the
// repository's teardown_cmd runs if trusted says so, and what the bootstrap
// made is taken out for git to remove the worktree; it is put back if git
// does not. The output ends saying so when the teardown_cmd did not run or
// failed, which does not stop the removal.
func RemoveWorktree(ctx context.Context, w *protocol.Worktree, trusted HookTrust) (string, error) {
	repo, path := w.GetRepo(), w.GetPath()
	if path == "" || repo == "" {
		return "", errors.New("no worktree to remove")
	}
	// A malformed config does not stop the removal: it bootstraps nothing.
	cfg, err := LoadWorktreeConfig(repo)
	if err != nil {
		cfg = WorktreeConfig{}
	}
	made, changed, err := bootstrapLeft(ctx, repo, path, cfg)
	if err != nil {
		return "", err
	}
	if len(changed) > 0 {
		shown := changed[:min(len(changed), 5)]
		more := ""
		if len(changed) > len(shown) {
			more = fmt.Sprintf(" and %d more", len(changed)-len(shown))
		}
		return "", fmt.Errorf("%s has changes not committed or not tracked, %s%s: commit or remove them first", path, strings.Join(shown, ", "), more)
	}
	notice := teardownWorktree(ctx, repo, path, w.GetBranch(), w.GetBase(), cfg, trusted)
	if err := removeMade(path, made); err != nil {
		_ = bootstrapPaths(repo, path, cfg)
		return "", err
	}
	out, err := run(ctx, repo, nil, "worktree", "remove", path)
	if err != nil {
		// Still there, with what its bootstrap made back as it was.
		_ = bootstrapPaths(repo, path, cfg)
		return out, err
	}
	// A branch with work not merged anywhere stays: -d refuses it.
	if more, err := run(ctx, repo, nil, "branch", "-d", w.GetBranch()); err == nil {
		out = strings.TrimSpace(out + "\n" + more)
	}
	if notice != "" {
		out = strings.TrimSpace(out + "\n" + notice)
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
		for p := range strings.SplitSeq(string(untracked), "\x00") {
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
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", p)
	}
	data, err := readRegular(full, maxCounted)
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
