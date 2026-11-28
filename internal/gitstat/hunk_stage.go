package gitstat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// StageHunk stages (or when unstage is true, unstages) a single hunk of file p
// against the git index using `git apply --cached --unidiff-zero`.
func StageHunk(ctx context.Context, root, p string, hunk []*protocol.DiffLine, unstage bool) error {
	if err := inWorkTree(p); err != nil || p == "" {
		return cmp.Or(err, errors.New("an empty path"))
	}
	patch, err := buildHunkPatch(p, hunk)
	if err != nil {
		return err
	}
	args := []string{"apply", "--cached", "--unidiff-zero", "--whitespace=nowarn"}
	if unstage {
		args = append(args, "-R")
	}
	args = append(args, "-")
	_, err = run(ctx, root, []byte(patch), args...)
	return err
}

func buildHunkPatch(p string, hunk []*protocol.DiffLine) (string, error) {
	if len(hunk) == 0 {
		return "", errors.New("empty hunk")
	}
	var oldStart, newStart int64
	var oldCount, newCount int
	var body strings.Builder
	for _, l := range hunk {
		op := l.GetOp()
		if op == "@" {
			continue
		}
		switch op {
		case " ":
			if oldStart == 0 && l.GetOld() > 0 {
				oldStart = l.GetOld()
			}
			if newStart == 0 && l.GetNew() > 0 {
				newStart = l.GetNew()
			}
			oldCount++
			newCount++
		case "-":
			if oldStart == 0 && l.GetOld() > 0 {
				oldStart = l.GetOld()
			}
			oldCount++
		case "+":
			if newStart == 0 && l.GetNew() > 0 {
				newStart = l.GetNew()
			}
			newCount++
		default:
			continue
		}
		body.WriteString(op)
		body.WriteString(l.GetText())
		body.WriteByte('\n')
	}
	if oldCount == 0 && newCount == 0 {
		return "", errors.New("hunk contains no diff lines")
	}
	if oldStart == 0 {
		if newStart > 0 {
			oldStart = newStart - 1
		}
		if oldCount > 0 && oldStart == 0 {
			oldStart = 1
		}
	}
	if newStart == 0 {
		if oldStart > 0 {
			newStart = oldStart - 1
		}
		if newCount > 0 && newStart == 0 {
			newStart = 1
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", p, p, oldStart, oldCount, newStart, newCount)
	b.WriteString(body.String())
	return b.String(), nil
}
