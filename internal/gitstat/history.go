package gitstat

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The history, a commit's changes, and the operations that change the
// repository go through the git program: it writes what the user's own git
// would, runs their hooks, and reaches remotes with their credentials.

// maxLog caps the commits a history lists.
const maxLog = 500

// emptyTree is the tree a root commit's changes are read against.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var hashPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// gitOut runs git in root and returns what it wrote to stdout; a failure's
// error carries what it wrote to stderr.
func gitOut(ctx context.Context, root string, stdin []byte, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, root, stdin, args)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, gitErr(err, stderr.String()+"\n"+string(out))
}

// run runs git in root for what it does, returning all it printed, which
// for a push or a pull is on stderr.
func run(ctx context.Context, root string, stdin []byte, args ...string) (string, error) {
	out, err := gitCommand(ctx, root, stdin, args).CombinedOutput()
	text := strings.TrimSpace(string(out))
	return text, gitErr(err, text)
}

// gitErr is a failed git's error, saying what it printed.
func gitErr(err error, printed string) error {
	switch printed = strings.TrimSpace(printed); {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return errors.New("git is not installed on this node")
	case printed != "":
		return errors.New(printed)
	}
	return err
}

// gitCommand is git run in root. Remotes are never asked for a password,
// nor an editor opened: there is no terminal to type in.
func gitCommand(ctx context.Context, root string, stdin []byte, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_EDITOR=true")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd
}

// checkHash refuses what is not a commit's hash, which git would read as an
// option or a revision expression.
func checkHash(hash string) error {
	if !hashPattern.MatchString(hash) {
		return fmt.Errorf("%q is not a commit hash", hash)
	}
	return nil
}

// inWorkTree refuses a path that leaves the work tree, as a GitFile path
// never does; "" is no path, and passes.
func inWorkTree(p string) error {
	if path.IsAbs(p) || strings.HasPrefix(path.Clean(p), "..") {
		return fmt.Errorf("%q is not in the work tree", p)
	}
	return nil
}

// logFormat is a commit as Log and Show read it, its fields apart by unit
// separators.
const logFormat = "%H%x1f%P%x1f%an%x1f%at%x1f%D%x1f%s"

// parseCommit reads a commit's fields as logFormat writes them.
func parseCommit(f []string) (*protocol.GitCommit, bool) {
	if len(f) < 6 {
		return &protocol.GitCommit{}, false
	}
	at, _ := strconv.ParseInt(f[3], 10, 64)
	c := &protocol.GitCommit{Hash: f[0], Parents: strings.Fields(f[1]), Author: f[2], Time: timestamppb.New(time.Unix(at, 0)), Subject: f[5]}
	for _, ref := range strings.Split(f[4], ", ") {
		if ref != "" {
			c.Refs = append(c.Refs, ref)
		}
	}
	return c, true
}

// Log lists the newest commits of the repository at root: those on its
// branches, remote branches and tags, children before their parents. The
// history is fingerprinted by its refs and HEAD: when it matches known, the
// commits are not read again and Unchanged is set.
func Log(ctx context.Context, root string, max int, known string) (*protocol.GitHistory, error) {
	if max <= 0 || max > maxLog {
		max = maxLog
	}
	out := &protocol.GitHistory{}
	if cur, err := gitOut(ctx, root, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		out.Current = strings.TrimSpace(string(cur))
	}
	// show-ref fails, printing nothing, before the first commit.
	refs, _ := gitOut(ctx, root, nil, "show-ref", "--head")
	sum := sha256.Sum256(append(refs, out.GetCurrent()...))
	out.Fingerprint = hex.EncodeToString(sum[:8])
	if out.GetFingerprint() == known {
		out.Unchanged = true
		return out, nil
	}
	if !bytes.Contains(refs, []byte(" HEAD\n")) {
		return out, nil // nothing committed yet
	}
	log, err := gitOut(ctx, root, nil, "log", "--branches", "--remotes", "--tags", "HEAD",
		"--topo-order", "-z", "--format="+logFormat, "-n", strconv.Itoa(max+1))
	if err != nil {
		return out, err
	}
	for _, r := range strings.Split(string(log), "\x00") {
		if c, ok := parseCommit(strings.Split(strings.TrimPrefix(r, "\n"), "\x1f")); ok {
			out.Commits = append(out.Commits, c)
		}
	}
	if len(out.GetCommits()) > max {
		out.Commits, out.More = out.GetCommits()[:max], true
	}
	return out, nil
}

// Show is what one commit changed against its first parent.
func Show(ctx context.Context, root, hash string) (*protocol.GitCommitInfo, error) {
	out := &protocol.GitCommitInfo{}
	if err := checkHash(hash); err != nil {
		return out, err
	}
	meta, err := gitOut(ctx, root, nil, "show", "-s", "--format="+logFormat+"%x1f%ae%x1f%b", hash)
	if err != nil {
		return out, err
	}
	fields := strings.SplitN(strings.TrimSuffix(string(meta), "\n"), "\x1f", 8)
	c, ok := parseCommit(fields)
	if !ok || len(fields) < 8 {
		return out, fmt.Errorf("cannot read commit %s", hash)
	}
	out.Commit, out.Email, out.Body = c, fields[6], strings.TrimSpace(fields[7])
	base := emptyTree
	if len(c.GetParents()) > 0 {
		base = c.GetParents()[0]
	}
	// The kinds of change and the lines changed come from two listings of
	// the same trees, read side by side.
	diffTree := func(format string) ([]byte, error) {
		return gitOut(ctx, root, nil, "diff-tree", "-r", "-M", "-z", format, base, c.GetHash())
	}
	var nums []byte
	var numsErr error
	done := make(chan struct{})
	go func() { defer close(done); nums, numsErr = diffTree("--numstat") }()
	names, err := diffTree("--name-status")
	<-done
	if err = cmp.Or(err, numsErr); err != nil {
		return out, err
	}
	out.Files = treeChanges(string(names), string(nums))
	return out, nil
}

// treeChanges reads diff-tree's -z --name-status and --numstat of the same
// two trees as their changed files.
func treeChanges(names, nums string) []*protocol.GitFile {
	var files []*protocol.GitFile
	at := map[string]int{}
	f := strings.Split(names, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		file := &protocol.GitFile{Path: f[i+1]}
		switch f[i][0] {
		case 'A':
			file.Status = protocol.GitAdded
		case 'D':
			file.Status = protocol.GitDeleted
		case 'R', 'C': // a rename or copy: the path it came from, then its own
			if i+2 >= len(f) {
				continue
			}
			file.Status, file.From, file.Path = protocol.GitRenamed, f[i+1], f[i+2]
			if f[i][0] == 'C' {
				file.Status, file.From = protocol.GitAdded, ""
			}
			i++
		default:
			file.Status = protocol.GitModified
		}
		at[file.GetPath()] = len(files)
		files = append(files, file)
	}
	// numstat: "added\tremoved\tpath", or for a rename "added\tremoved\t"
	// then the two paths as records of their own; "-" counts are binary.
	n := strings.Split(nums, "\x00")
	for i := 0; i < len(n); i++ {
		parts := strings.SplitN(n[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		p := parts[2]
		if p == "" && i+2 < len(n) {
			p = n[i+2]
			i += 2
		}
		j, ok := at[p]
		if !ok {
			continue
		}
		if parts[0] == "-" {
			files[j].Binary = true
			continue
		}
		files[j].Added, _ = strconv.ParseInt(parts[0], 10, 64)
		files[j].Removed, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	slices.SortFunc(files, func(a, b *protocol.GitFile) int { return strings.Compare(a.GetPath(), b.GetPath()) })
	return files
}

// CommitDiff is one file's changes in a commit, against its first parent.
// from is the path the parent has it at, for a rename, or "".
func CommitDiff(ctx context.Context, root, hash, p, from string) (*protocol.Diff, error) {
	if err := cmp.Or(checkHash(hash), inWorkTree(p), inWorkTree(from)); err != nil {
		return nil, err
	}
	// A root commit has no parent, whose blob then reads as none.
	old := blob(ctx, root, hash+"^", cmp.Or(from, p))
	cur := blob(ctx, root, hash, p)
	out := &protocol.Diff{Path: p}
	if Binary(old) || Binary(cur) {
		out.Binary = true
		return out, nil
	}
	out.Lines, out.Truncated = unified(old, cur)
	return out, nil
}

// blob is a file as a commit has it, "" where it has none, and a NUL for
// one too large to diff, which then shows as binary.
func blob(ctx context.Context, root, rev, p string) string {
	size, err := gitOut(ctx, root, nil, "cat-file", "-s", rev+":"+p)
	if err != nil {
		return ""
	}
	if n, _ := strconv.Atoi(strings.TrimSpace(string(size))); n > maxCounted {
		return "\x00"
	}
	b, err := gitOut(ctx, root, nil, "cat-file", "blob", rev+":"+p)
	if err != nil {
		return ""
	}
	return string(b)
}

// Run runs a git operation on the work tree at root and returns what git
// printed.
func Run(ctx context.Context, r *protocol.GitRun) (string, error) {
	switch r.GetOp() {
	case protocol.GitOpCommit:
		return commit(ctx, r)
	case protocol.GitOpPush:
		return push(ctx, r.GetRoot())
	case protocol.GitOpPull:
		return run(ctx, r.GetRoot(), nil, "pull", "--ff-only")
	case protocol.GitOpFetch:
		return run(ctx, r.GetRoot(), nil, "fetch", "--all", "--prune")
	case protocol.GitOpCheckout, protocol.GitOpBranch:
		if _, err := gitOut(ctx, r.GetRoot(), nil, "check-ref-format", "--branch", r.GetBranch()); err != nil || strings.HasPrefix(r.GetBranch(), "-") {
			return "", fmt.Errorf("%q is not a branch name", r.GetBranch())
		}
		if r.GetOp() == protocol.GitOpBranch {
			return run(ctx, r.GetRoot(), nil, "switch", "-c", r.GetBranch())
		}
		return run(ctx, r.GetRoot(), nil, "switch", r.GetBranch())
	case protocol.GitOpDiscard:
		if len(r.GetPaths()) != 1 {
			return "", errors.New("a discard is of one file")
		}
		return "", DiscardHunk(r.GetRoot(), r.GetPaths()[0], r.GetHunk())
	case protocol.GitOpMerge, protocol.GitOpPR, protocol.GitOpRemoveWT:
		if r.GetWorktree() == nil {
			return "", errors.New("no worktree named")
		}
		switch r.GetOp() {
		case protocol.GitOpMerge:
			return Merge(ctx, r.GetWorktree())
		case protocol.GitOpPR:
			return PullRequest(ctx, r.GetWorktree())
		}
		return RemoveWorktree(ctx, r.GetWorktree())
	}
	return "", fmt.Errorf("unknown git operation %q", r.GetOp())
}

// commit commits the paths named, or every change, with the message.
func commit(ctx context.Context, r *protocol.GitRun) (string, error) {
	msg := strings.TrimSpace(r.GetMessage())
	if msg == "" && !r.GetAmend() {
		return "", errors.New("a commit needs a message")
	}
	for _, p := range r.GetPaths() {
		if err := inWorkTree(p); err != nil || p == "" {
			return "", cmp.Or(err, errors.New("an empty path"))
		}
	}
	add := []string{"add", "-A"}
	args := []string{"commit"}
	if r.GetAmend() {
		args = append(args, "--amend")
	}
	var stdin []byte
	if msg != "" {
		args, stdin = append(args, "-F", "-"), []byte(msg+"\n")
	} else {
		args = append(args, "--no-edit")
	}
	if len(r.GetPaths()) > 0 {
		add = append(append(add, "--"), r.GetPaths()...)
		args = append(append(args, "--"), r.GetPaths()...)
	}
	if _, err := gitOut(ctx, r.GetRoot(), nil, add...); err != nil {
		return "", err
	}
	return run(ctx, r.GetRoot(), stdin, args...)
}

// push pushes the branch to its upstream, setting one on the remote first
// when the branch has none and the repository has one remote to push to.
func push(ctx context.Context, root string) (string, error) {
	if _, err := gitOut(ctx, root, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		return run(ctx, root, nil, "push")
	}
	remotes, err := gitOut(ctx, root, nil, "remote")
	if err != nil {
		return "", err
	}
	names := strings.Fields(string(remotes))
	remote := "origin"
	switch {
	case len(names) == 0:
		return "", errors.New("the repository has no remote to push to")
	case !slices.Contains(names, remote):
		if len(names) > 1 {
			return "", errors.New("the branch has no upstream, and there is more than one remote to choose from")
		}
		remote = names[0]
	}
	return run(ctx, root, nil, "push", "--set-upstream", remote, "HEAD")
}
