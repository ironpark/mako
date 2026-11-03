package gitstat

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Stamp changes whenever the history a source control panel shows of the
// repository dir is in moves: a commit, a reset or a checkout, a branch
// made or deleted, a stash, a fetch. It is read from the files git keeps
// those in, by their size and time and HEAD's text, so it is cheap to ask
// for often, and says nothing of what they hold. Not the index: a git
// status rewrites it, as agents run one often, and what is staged is read
// every few seconds anyway. "" outside a repository.
func Stamp(dir string) string {
	gitDir, _ := findGitDir(dir)
	if gitDir == "" {
		return ""
	}
	common := commonDir(gitDir)
	h := fnv.New64a()
	head, _ := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	h.Write(head)
	files := []string{
		filepath.Join(common, "packed-refs"),
		filepath.Join(common, "refs", "heads"),
		filepath.Join(common, "refs", "stash"),
		filepath.Join(common, "logs", "refs", "stash"),
		filepath.Join(common, "FETCH_HEAD"),
	}
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		files = append(files, filepath.Join(common, filepath.FromSlash(ref)))
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			h.Write([]byte{0})
			continue
		}
		h.Write([]byte(strconv.FormatInt(fi.ModTime().UnixNano(), 36)))
		h.Write([]byte(strconv.FormatInt(fi.Size(), 36)))
	}
	return strconv.FormatUint(h.Sum64(), 36)
}
