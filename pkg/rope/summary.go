package rope

import (
	"strings"
)

// Summary holds aggregated metrics of a text subtree: total bytes, line
// breaks, length of the first and last line segments, and the maximum line
// length. It forms a monoid under concatenation.
type Summary struct {
	Bytes        int
	Lines        int // count of '\n'
	FirstLineLen int
	LastLineLen  int
	LongestLine  int
}

// Add combines two summaries into one, preserving monoid associativity.
func (a Summary) Add(b Summary) Summary {
	res := Summary{
		Bytes: a.Bytes + b.Bytes,
		Lines: a.Lines + b.Lines,
	}
	midLine := a.LastLineLen + b.FirstLineLen
	if a.Lines == 0 {
		res.FirstLineLen = midLine
	} else {
		res.FirstLineLen = a.FirstLineLen
	}
	if b.Lines == 0 {
		res.LastLineLen = midLine
	} else {
		res.LastLineLen = b.LastLineLen
	}
	res.LongestLine = max(a.LongestLine, b.LongestLine, midLine)
	return res
}

// computeSummary computes the metrics for a single leaf chunk.
func computeSummary(s string) Summary {
	n := len(s)
	if n == 0 {
		return Summary{}
	}
	firstNL := strings.IndexByte(s, '\n')
	if firstNL < 0 {
		return Summary{
			Bytes:        n,
			Lines:        0,
			FirstLineLen: n,
			LastLineLen:  n,
			LongestLine:  n,
		}
	}
	lastNL := strings.LastIndexByte(s, '\n')
	lines := 0
	longest := firstNL
	cur := 0
	for i := firstNL + 1; i <= lastNL; i++ {
		if s[i] == '\n' {
			lines++
			if cur > longest {
				longest = cur
			}
			cur = 0
		} else {
			cur++
		}
	}
	lines++ // count the first newline
	lastLen := n - 1 - lastNL
	if lastLen > longest {
		longest = lastLen
	}
	return Summary{
		Bytes:        n,
		Lines:        lines,
		FirstLineLen: firstNL,
		LastLineLen:  lastLen,
		LongestLine:  longest,
	}
}
