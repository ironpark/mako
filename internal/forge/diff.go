package forge

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// A pull request's change is read from its forge whole, as a unified diff,
// and kept: a front-end asks for it a file at a time. It is kept by
// the head it was read at, so it never goes stale; a push to the pull
// request reads it again.

// keptDiffs is how many pull requests' changes are kept, the last read.
const keptDiffs = 8

type keptDiff struct {
	key   string
	files map[string]*protocol.Diff
}

var diffs struct {
	sync.Mutex
	kept []keptDiff // the last read last
}

// PullDiff is one file's diff in pull request n of the repository at root,
// at head, the commit the asker saw the pull request at.
func PullDiff(ctx context.Context, root string, n int64, head, path string) (*protocol.Diff, error) {
	key := root + "\x00" + strconv.FormatInt(n, 10) + "\x00" + head
	diffs.Lock()
	i := slices.IndexFunc(diffs.kept, func(k keptDiff) bool { return k.key == key })
	var files map[string]*protocol.Diff
	if i >= 0 {
		files = diffs.kept[i].files
	}
	diffs.Unlock()
	if files == nil {
		b, err := of(ctx, root)
		if err != nil {
			return nil, err
		}
		text, err := b.diff(ctx, root, n)
		if err != nil {
			return nil, err
		}
		files = ParseDiff(text)
		diffs.Lock()
		diffs.kept = append(diffs.kept, keptDiff{key, files})
		if len(diffs.kept) > keptDiffs {
			diffs.kept = slices.Delete(diffs.kept, 0, len(diffs.kept)-keptDiffs)
		}
		diffs.Unlock()
	}
	if d, ok := files[path]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("pull request #%d does not change %s", n, path)
}

func (github) diff(ctx context.Context, root string, n int64) (string, error) {
	out, err := gh(ctx, root, nil, "pr", "diff", strconv.FormatInt(n, 10), "--color", "never")
	return string(out), err
}

// ParseDiff reads a unified diff of many files, as git diff writes it,
// into each file's diff by its new path, a deleted file's by its old.
func ParseDiff(text string) map[string]*protocol.Diff {
	out := map[string]*protocol.Diff{}
	var cur *protocol.Diff
	var oldN, newN int64
	inHunk := false
	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &protocol.Diff{Path: gitPath(line)}
			out[cur.Path] = cur
			inHunk = false
			continue
		case cur == nil:
			continue
		case strings.HasPrefix(line, "@@"):
			oldN, newN = hunkStart(line)
			inHunk = true
			cur.Lines = append(cur.Lines, &protocol.DiffLine{Op: "@", Text: line})
			continue
		case !inHunk:
			switch {
			case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
				cur.Binary = true
			case strings.HasPrefix(line, "+++ ") && line != "+++ /dev/null":
				rename(out, cur, strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/"))
			case strings.HasPrefix(line, "rename to "):
				rename(out, cur, strings.TrimPrefix(line, "rename to "))
			}
			continue
		}
		if len(cur.Lines) >= gitstat.MaxDiffLines {
			cur.Truncated = true
			continue
		}
		op, body := " ", line
		if line != "" {
			op, body = line[:1], line[1:]
		}
		switch op {
		case "+":
			cur.Lines = append(cur.Lines, &protocol.DiffLine{Op: "+", Text: body, New: newN})
			newN++
		case "-":
			cur.Lines = append(cur.Lines, &protocol.DiffLine{Op: "-", Text: body, Old: oldN})
			oldN++
		case " ":
			cur.Lines = append(cur.Lines, &protocol.DiffLine{Op: " ", Text: body, Old: oldN, New: newN})
			oldN, newN = oldN+1, newN+1
		case `\`: // no newline at the end of the file
		default:
			inHunk = false
		}
	}
	return out
}

// gitPath is the path a diff --git line names on its new side.
func gitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return strings.TrimPrefix(rest, "a/")
}

// rename files a diff under the path its headers name, which the diff
// --git line can leave unclear when a path has spaces.
func rename(out map[string]*protocol.Diff, d *protocol.Diff, path string) {
	if path == "" || path == d.Path {
		return
	}
	if out[d.Path] == d {
		delete(out, d.Path)
	}
	d.Path = path
	out[path] = d
}

// hunkStart is where a hunk starts on each side, from its @@ line.
func hunkStart(line string) (old, cur int64) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return 0, 0
	}
	num := func(s string) int64 {
		s, _, _ = strings.Cut(s[1:], ",")
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	return num(f[1]), num(f[2])
}
