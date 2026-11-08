// Package gitstat reads a work tree's changes, for the daemon to report to
// front-ends: which files differ from HEAD, by how many lines, and how.
package gitstat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/ironpark/mako/internal/protocol"
)

// maxCounted is the size past which a file's lines are not counted or
// diffed: it is shown as changed, as a binary file is.
const maxCounted = 4 << 20

// MaxDiffLines caps a diff's lines; the rest is left out as truncated.
const MaxDiffLines = 5000

// contextLines is how many unchanged lines a hunk keeps around its changes.
const contextLines = 3

// Status reports the changes of the work tree dir is in, against HEAD,
// staged or not. A dir in no repository reports NotRepo. git is given
// ten seconds, within ctx, to answer.
func Status(ctx context.Context, dir string) (*protocol.GitChanges, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return &protocol.GitChanges{NotRepo: true}, nil
	}
	if err != nil {
		return nil, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	out := &protocol.GitChanges{Root: wt.Filesystem.Root(), Branch: branch(repo)}
	st, track, heads, err := status(ctx, wt, out.GetRoot())
	if err != nil {
		return nil, err
	}
	out.Upstream, out.Ahead, out.Behind = track.upstream, int64(track.ahead), int64(track.behind)
	if refs, err := repo.Branches(); err == nil {
		_ = refs.ForEach(func(r *plumbing.Reference) error {
			out.Branches = append(out.Branches, r.Name().Short())
			return nil
		})
		slices.Sort(out.Branches)
	}
	if refs, err := repo.References(); err == nil {
		_ = refs.ForEach(func(r *plumbing.Reference) error {
			if r.Name().IsRemote() && !strings.HasSuffix(r.Name().String(), "/HEAD") {
				out.RemoteBranches = append(out.RemoteBranches, r.Name().Short())
			}
			return nil
		})
		slices.Sort(out.RemoteBranches)
	}
	// What go-git does not read, git is asked: stashes, and a merge or
	// rebase under way with the files it left in conflict.
	out.Stashes = stashes(ctx, out.GetRoot())
	var unmerged map[string]bool
	if out.Operation = Operation(ctx, out.GetRoot()); out.Operation != "" {
		unmerged = conflicted(ctx, out.GetRoot())
	}
	// HEAD's tree is read for a file whose lines are not counted already.
	var tree *object.Tree
	treeRead := false
	for p, fs := range st {
		f, ok := change(p, fs)
		if !ok {
			continue
		}
		full := filepath.Join(out.GetRoot(), filepath.FromSlash(f.GetPath()))
		info, err := os.Stat(full)
		curOK := err == nil && !info.IsDir()
		if f.GetStatus() == protocol.GitAdded && !curOK {
			continue // added to the index and gone from the disk: nothing to show
		}
		if unmerged[f.GetPath()] {
			f.Status = protocol.GitConflicted
		}
		head, known := heads[p]
		key := countKey{path: full, head: head, cur: curOK}
		if curOK {
			key.size, key.mod = info.Size(), info.ModTime().UnixNano()
		}
		c, ok := counts.get(key)
		if !known || !ok {
			if !treeRead {
				tree, treeRead = headTree(repo), true
			}
			c = countFile(tree, cmp.Or(f.GetFrom(), f.GetPath()), out.GetRoot(), f.GetPath())
			if known {
				counts.put(key, c)
			}
		}
		f.Binary, f.Added, f.Removed = c.binary, int64(c.added), int64(c.removed)
		out.Files = append(out.Files, f)
	}
	slices.SortFunc(out.Files, func(a, b *protocol.GitFile) int { return strings.Compare(a.GetPath(), b.GetPath()) })
	return out, nil
}

// countFile is the lines a file's change adds and removes: from as HEAD
// has it to p as the work tree does.
func countFile(tree *object.Tree, from, root, p string) counted {
	old, oldOK := headContent(tree, from)
	cur, curOK := diskContent(root, p)
	if Binary(old) || Binary(cur) || (!oldOK && !curOK) {
		return counted{binary: true}
	}
	added, removed := count(old, cur)
	return counted{added: added, removed: removed}
}

// counts are the lines files' changes were counted at, so a poll reads and
// diffs only the files that changed since: a file's count holds while
// HEAD has the same object for it and the disk the same size and time.
var counts countCache

// countKey is what a file's count depends on: its path on the disk, the
// object HEAD has for it, "" or zeros for none, and the file on the disk.
type countKey struct {
	path, head string
	cur        bool
	size, mod  int64
}

type counted struct {
	added, removed int
	binary         bool
}

type countCache struct {
	sync.Mutex
	m map[countKey]counted
}

func (c *countCache) get(k countKey) (counted, bool) {
	c.Lock()
	defer c.Unlock()
	v, ok := c.m[k]
	return v, ok
}

// put keeps a count, starting afresh past mostCounts: an entry outlives
// its file's next change, and nothing else removes it.
func (c *countCache) put(k countKey, v counted) {
	c.Lock()
	defer c.Unlock()
	if c.m == nil || len(c.m) >= mostCounts {
		c.m = make(map[countKey]counted)
	}
	c.m[k] = v
}

const mostCounts = 4096

// status is the work tree's status, its branch's upstream and, by path,
// the object HEAD has for each changed file. git reports them when it is
// on the PATH: it skips the folders .gitignore leaves out, where go-git
// walks every one (node_modules and the like, seconds a time), and it
// takes no lock an agent's own git would wait on. go-git's is the
// fallback, which knows no upstream, nor the objects; but not for a git
// that ran out of time, as go-git would take longer still.
func status(ctx context.Context, wt *git.Worktree, root string) (git.Status, tracking, map[string]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "--no-optional-locks",
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all").Output()
	if err != nil && ctx.Err() != nil {
		return nil, tracking{}, nil, ctx.Err()
	}
	if err != nil {
		st, err := wt.Status()
		return st, tracking{}, nil, err
	}
	st, track, heads := parsePorcelain(out)
	return st, track, heads, nil
}

// tracking is a branch's upstream, and the commits each has that the
// other lacks.
type tracking struct {
	upstream      string
	ahead, behind int
}

// parsePorcelain reads git status --porcelain=v2 --branch -z as go-git's
// status, the same status codes but for '.' where a side is unchanged, the
// branch's upstream from its headers, and the object HEAD has for each
// file, "" for an untracked one; a file in conflict has none.
func parsePorcelain(out []byte) (git.Status, tracking, map[string]string) {
	st := git.Status{}
	heads := map[string]string{}
	var t tracking
	file := func(p string) *git.FileStatus {
		if st[p] == nil {
			st[p] = &git.FileStatus{Staging: git.Unmodified, Worktree: git.Unmodified}
		}
		return st[p]
	}
	code := func(c byte) git.StatusCode {
		if c == '.' {
			return git.Unmodified
		}
		return git.StatusCode(c)
	}
	// Each entry's fields, the path last and free to hold spaces.
	fields := map[byte]int{'1': 9, '2': 10, 'u': 11}
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		r := records[i]
		if len(r) < 3 {
			continue
		}
		switch n, tracked := fields[r[0]]; {
		case tracked:
			parts := strings.SplitN(r, " ", n)
			if len(parts) < n || len(parts[1]) != 2 {
				continue
			}
			f := file(parts[n-1])
			f.Staging, f.Worktree = code(parts[1][0]), code(parts[1][1])
			if r[0] != 'u' {
				heads[parts[n-1]] = parts[6]
			}
			if r[0] == '2' && i+1 < len(records) { // a rename: the path it had follows
				i++
				f.Extra = records[i]
			}
		case r[0] == '?':
			file(r[2:]).Worktree = git.Untracked
			heads[r[2:]] = ""
		case strings.HasPrefix(r, "# branch.upstream "):
			t.upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			_, _ = fmt.Sscanf(strings.TrimPrefix(r, "# branch.ab "), "+%d -%d", &t.ahead, &t.behind)
		}
	}
	return st, t, heads
}

// change reads a file's status codes as one change, or reports none.
func change(p string, fs *git.FileStatus) (*protocol.GitFile, bool) {
	f := &protocol.GitFile{Path: p}
	switch {
	case fs.Staging == git.Unmodified && fs.Worktree == git.Unmodified:
		return f, false
	case fs.Worktree == git.Untracked || fs.Staging == git.Added:
		f.Status = protocol.GitAdded
	case fs.Staging == git.Deleted || fs.Worktree == git.Deleted:
		f.Status = protocol.GitDeleted
	case fs.Staging == git.Renamed:
		f.Status, f.From = protocol.GitRenamed, fs.Extra
	default:
		f.Status = protocol.GitModified
	}
	return f, true
}

// branch names what HEAD is on: a branch, unborn or not, or the commit.
func branch(repo *git.Repository) string {
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return ""
	}
	if head.Type() == plumbing.SymbolicReference {
		return head.Target().Short()
	}
	return head.Hash().String()[:7]
}

func headTree(repo *git.Repository) *object.Tree {
	ref, err := repo.Head()
	if err != nil {
		return nil // no commit yet
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil
	}
	return tree
}

// headContent is a file as HEAD has it, and whether it has it.
func headContent(tree *object.Tree, p string) (string, bool) {
	if tree == nil {
		return "", false
	}
	f, err := tree.File(p)
	if err != nil || f.Size > maxCounted {
		return "", err == nil
	}
	s, err := f.Contents()
	return s, err == nil
}

// diskContent is a file as the work tree has it, and whether it has it. A
// symbolic link is the path it holds, as git has it, not what it points
// to, which may be a device or a pipe with no end.
func diskContent(root, p string) (string, bool) {
	full := filepath.Join(root, filepath.FromSlash(p))
	info, err := os.Lstat(full)
	if err != nil || info.IsDir() {
		return "", false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		return filepath.ToSlash(target), err == nil
	}
	if !info.Mode().IsRegular() || info.Size() > maxCounted {
		return "\x00", true // not to be counted: shown as binary
	}
	b, err := readRegular(full, maxCounted)
	if errors.Is(err, errTooLarge) {
		return "\x00", true
	}
	return string(b), err == nil
}

var errTooLarge = errors.New("too large")

// readRegular reads a regular file, which it is still when it is opened,
// through no more than max bytes of it: a file grown past its stat is
// errTooLarge.
func readRegular(full string, max int64) ([]byte, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, cmp.Or(err, errors.New("not a regular file"))
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, err
}

// Binary reports a NUL in a file's first 8000 bytes, as git decides.
func Binary[T string | []byte](s T) bool {
	for _, c := range []byte(s[:min(len(s), 8000)]) {
		if c == 0 {
			return true
		}
	}
	return false
}

// count is the lines added and removed between two texts.
func count(old, cur string) (added, removed int) {
	for _, d := range diff.Do(old, cur) {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			added += lineCount(d.Text)
		case diffmatchpatch.DiffDelete:
			removed += lineCount(d.Text)
		}
	}
	return added, removed
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// Diff is one file's changes against HEAD as unified diff hunks. from is
// the path HEAD has it at, for a rename, or "".
func Diff(root, p, from string) (*protocol.Diff, error) {
	if err := cmp.Or(inWorkTree(p), inWorkTree(from)); err != nil {
		return nil, err
	}
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, err
	}
	old, _ := headContent(headTree(repo), cmp.Or(from, p))
	cur, _ := diskContent(root, p)
	out := &protocol.Diff{Path: p}
	if Binary(old) || Binary(cur) {
		out.Binary = true
		return out, nil
	}
	out.Lines, out.Truncated = unified(old, cur)
	return out, nil
}

// TextDiff is the change from old to cur as unified diff hunks, as Diff
// gives a file's: for texts that are not files, such as an agent's edit.
func TextDiff(old, cur string) []*protocol.DiffLine {
	lines, _ := unified(old, cur)
	return lines
}

// line is one line of a diff before it is cut into hunks.
type line struct {
	op       byte
	text     string
	old, cur int // its numbers on each side, 0 where it has none
}

// unified cuts the line diff of two texts into hunks with context lines
// around each change, as git diff writes them.
func unified(old, cur string) ([]*protocol.DiffLine, bool) {
	var lines []line
	o, n := 1, 1
	for _, d := range diff.Do(old, cur) {
		for _, t := range splitLines(d.Text) {
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				lines = append(lines, line{' ', t, o, n})
				o, n = o+1, n+1
			case diffmatchpatch.DiffDelete:
				lines = append(lines, line{'-', t, o, 0})
				o++
			case diffmatchpatch.DiffInsert:
				lines = append(lines, line{'+', t, 0, n})
				n++
			}
		}
	}
	var out []*protocol.DiffLine
	var at, oldBefore, newBefore int // the lines ahead of a hunk, on each side
	for i := 0; i < len(lines); {
		// The next change, and the run of changes no more than two
		// contexts apart that it starts.
		first := i
		for first < len(lines) && lines[first].op == ' ' {
			first++
		}
		if first == len(lines) {
			break
		}
		last := first
		for j := first; j < len(lines) && j-last <= 2*contextLines; j++ {
			if lines[j].op != ' ' {
				last = j
			}
		}
		start, end := max(first-contextLines, i), min(last+contextLines+1, len(lines))
		for _, l := range lines[at:start] {
			if l.op != '+' {
				oldBefore = l.old
			}
			if l.op != '-' {
				newBefore = l.cur
			}
		}
		at = start
		out = append(out, header(lines[start:end], oldBefore, newBefore))
		for _, l := range lines[start:end] {
			out = append(out, &protocol.DiffLine{Op: string(l.op), Text: l.text, Old: int64(l.old), New: int64(l.cur)})
		}
		if len(out) > MaxDiffLines {
			return out[:MaxDiffLines], true
		}
		i = end
	}
	return out, false
}

// header is a hunk's @@ line: where it starts on each side and how many
// lines it spans there. oldStart and newStart number the last line ahead
// of the hunk on each side.
func header(hunk []line, oldStart, newStart int) *protocol.DiffLine {
	var oldN, newN int
	for _, l := range hunk {
		if l.op != '+' {
			oldN++
		}
		if l.op != '-' {
			newN++
		}
	}
	// A side the hunk has lines on starts at its first; one it has none on
	// is named by the line before, as git does.
	if oldN > 0 {
		oldStart++
	}
	if newN > 0 {
		newStart++
	}
	return &protocol.DiffLine{Op: "@", Text: fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldN, newStart, newN)}
}

// splitLines splits text at its line breaks, without them.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}
