// Package gitstat reads a work tree's changes, for the daemon to report to
// front-ends: which files differ from HEAD, by how many lines, and how.
package gitstat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/ironpark/sanbo/internal/protocol"
)

// maxCounted is the size past which a file's lines are not counted or
// diffed: it is shown as changed, as a binary file is.
const maxCounted = 4 << 20

// maxDiffLines caps a diff's lines; the rest is left out as truncated.
const maxDiffLines = 5000

// contextLines is how many unchanged lines a hunk keeps around its changes.
const contextLines = 3

// Status reports the changes of the work tree dir is in, against HEAD,
// staged or not. A dir in no repository reports NotRepo.
func Status(dir string) (*protocol.GitChanges, error) {
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
	st, track, err := status(wt, out.GetRoot())
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out.Stashes = stashes(ctx, out.GetRoot())
	var unmerged map[string]bool
	if out.Operation = Operation(ctx, out.GetRoot()); out.Operation != "" {
		unmerged = conflicted(ctx, out.GetRoot())
	}
	tree := headTree(repo)
	for p, fs := range st {
		f, ok := change(p, fs)
		if !ok {
			continue
		}
		old, oldOK := headContent(tree, cmp.Or(f.GetFrom(), f.GetPath()))
		cur, curOK := diskContent(out.GetRoot(), f.GetPath())
		if f.GetStatus() == protocol.GitAdded && !curOK {
			continue // added to the index and gone from the disk: nothing to show
		}
		if unmerged[f.GetPath()] {
			f.Status = protocol.GitConflicted
		}
		if Binary(old) || Binary(cur) || (!oldOK && !curOK) {
			f.Binary = true
		} else {
			added, removed := count(old, cur)
			f.Added, f.Removed = int64(added), int64(removed)
		}
		out.Files = append(out.Files, f)
	}
	slices.SortFunc(out.Files, func(a, b *protocol.GitFile) int { return strings.Compare(a.GetPath(), b.GetPath()) })
	return out, nil
}

// status is the work tree's status, and its branch's upstream. git reports
// it when it is on the PATH: it skips the folders .gitignore leaves out,
// where go-git walks every one (node_modules and the like, seconds a time),
// and it takes no lock an agent's own git would wait on. go-git's is the
// fallback, which knows no upstream.
func status(wt *git.Worktree, root string) (git.Status, tracking, error) {
	out, err := exec.Command("git", "-C", root, "--no-optional-locks",
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all").Output()
	if err != nil {
		st, err := wt.Status()
		return st, tracking{}, err
	}
	st, track := parsePorcelain(out)
	return st, track, nil
}

// tracking is a branch's upstream, and the commits each has that the
// other lacks.
type tracking struct {
	upstream      string
	ahead, behind int
}

// parsePorcelain reads git status --porcelain=v2 --branch -z as go-git's
// status, the same status codes but for '.' where a side is unchanged, and
// the branch's upstream from its headers.
func parsePorcelain(out []byte) (git.Status, tracking) {
	st := git.Status{}
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
			if r[0] == '2' && i+1 < len(records) { // a rename: the path it had follows
				i++
				f.Extra = records[i]
			}
		case r[0] == '?':
			file(r[2:]).Worktree = git.Untracked
		case strings.HasPrefix(r, "# branch.upstream "):
			t.upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			_, _ = fmt.Sscanf(strings.TrimPrefix(r, "# branch.ab "), "+%d -%d", &t.ahead, &t.behind)
		}
	}
	return st, t
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

// diskContent is a file as the work tree has it, and whether it has it.
func diskContent(root, p string) (string, bool) {
	full := filepath.Join(root, filepath.FromSlash(p))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return "", false
	}
	if info.Size() > maxCounted {
		return "\x00", true // too large to count: shown as binary
	}
	b, err := os.ReadFile(full)
	return string(b), err == nil
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
		if len(out) > maxDiffLines {
			return out[:maxDiffLines], true
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
