package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// A project's files are listed for quick open, and searched for a text,
// through the node's daemon: with ripgrep or git where the node has them,
// which leave out what .gitignore does, and walking the folder otherwise.

const (
	maxSearchFiles   = 50000
	maxSearchMatches = 1000
	maxMatchText     = 300 // bytes of a matched line kept
	searchTimeout    = 20 * time.Second
)

func (d *Daemon) handleSearch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSearchResult,
		func(r *protocol.Search) (*protocol.SearchResult, error) {
			if err := absolute(r.GetRoot()); err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, searchTimeout)
			defer cancel()
			if r.GetFiles() {
				return listFiles(ctx, r.GetRoot())
			}
			if strings.TrimSpace(r.GetQuery()) == "" {
				return nil, errors.New("nothing to search for")
			}
			return searchText(ctx, r.GetRoot(), r.GetQuery())
		})
}

// listFiles lists a project's files, slash-separated from root.
func listFiles(ctx context.Context, root string) (*protocol.SearchResult, error) {
	out := &protocol.SearchResult{}
	if git, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z").Output(); err == nil {
		for _, p := range strings.Split(string(git), "\x00") {
			if p == "" {
				continue
			}
			if len(out.GetFiles()) == maxSearchFiles {
				out.Truncated = true
				break
			}
			out.Files = append(out.Files, p)
		}
		return out, nil
	}
	err := walkProject(ctx, root, func(rel string, _ fs.DirEntry) bool {
		if len(out.GetFiles()) == maxSearchFiles {
			out.Truncated = true
			return false
		}
		out.Files = append(out.Files, rel)
		return true
	})
	return out, err
}

// walkProject visits a folder's files, leaving out hidden folders and the
// dependency folders tools fill, until visit returns false.
func walkProject(ctx context.Context, root string, visit func(rel string, e fs.DirEntry) bool) error {
	stop := errors.New("stop")
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable folder is passed over
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.IsDir() {
			if p != root && (strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" || e.Name() == "target" || e.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if !visit(filepath.ToSlash(rel), e) {
			return stop
		}
		return nil
	})
	if errors.Is(err, stop) {
		return nil
	}
	return err
}

// smartCase reports whether a search matches the case: only when the
// query has a capital, as ripgrep's --smart-case does.
func smartCase(q string) bool { return strings.IndexFunc(q, unicode.IsUpper) >= 0 }

// searchText finds the lines holding query in a project's files.
func searchText(ctx context.Context, root, query string) (*protocol.SearchResult, error) {
	if rg, err := exec.LookPath("rg"); err == nil {
		cmd := exec.CommandContext(ctx, rg, "--null", "--no-heading", "--line-number", "--column", "--color", "never",
			"--fixed-strings", "--smart-case", "--no-require-git", "--glob", "!node_modules/", "--max-count", "100", "--max-columns", strconv.Itoa(maxMatchText),
			"--max-columns-preview", "--", query, ".")
		cmd.Dir = root
		return grepOutput(cmd)
	}
	if exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run() == nil {
		args := []string{"-C", root, "grep", "--null", "-n", "--column", "-I", "--untracked", "--fixed-strings", "-e", query}
		if !smartCase(query) {
			args = append(args[:3], append([]string{"-i"}, args[3:]...)...)
		}
		return grepOutput(exec.CommandContext(ctx, "git", args...))
	}
	return walkSearch(ctx, root, query)
}

// grepOutput reads matches as ripgrep and git grep print them with
// --null: the path, a NUL, then line:column:text. A search that found
// nothing exits 1, which is no failure.
func grepOutput(cmd *exec.Cmd) (*protocol.SearchResult, error) {
	out := &protocol.SearchResult{}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return out, err
	}
	if err := cmd.Start(); err != nil {
		return out, err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		path, rest, ok := strings.Cut(sc.Text(), "\x00")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, ":", 3)
		if len(f) != 3 {
			continue
		}
		line, _ := strconv.Atoi(f[0])
		col, _ := strconv.Atoi(f[1])
		if len(out.GetMatches()) == maxSearchMatches {
			out.Truncated = true
			_ = cmd.Process.Kill()
			break
		}
		out.Matches = append(out.Matches, &protocol.SearchMatch{Path: strings.TrimPrefix(path, "./"), Line: int64(line), Col: int64(col), Text: clip(f[2])})
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if err != nil && (out.GetTruncated() || errors.As(err, &exit) && exit.ExitCode() == 1) {
		err = nil
	}
	return out, err
}

// walkSearch searches a folder without a search program to do it.
func walkSearch(ctx context.Context, root, query string) (*protocol.SearchResult, error) {
	out := &protocol.SearchResult{}
	fold := !smartCase(query)
	needle := []byte(query)
	if fold {
		needle = bytes.ToLower(needle)
	}
	err := walkProject(ctx, root, func(rel string, e fs.DirEntry) bool {
		if info, err := e.Info(); err != nil || info.Size() > 4<<20 {
			return true
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return true // unreadable, or binary
		}
		for n, line := range bytes.Split(data, []byte("\n")) {
			hay := line
			if fold {
				hay = bytes.ToLower(line)
			}
			if i := bytes.Index(hay, needle); i >= 0 {
				if len(out.GetMatches()) == maxSearchMatches {
					out.Truncated = true
					return false
				}
				out.Matches = append(out.Matches, &protocol.SearchMatch{Path: rel, Line: int64(n + 1), Col: int64(i + 1), Text: clip(string(line))})
			}
		}
		return true
	})
	return out, err
}

// clip is a matched line as sent: its end cut at a character's edge.
func clip(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) <= maxMatchText {
		return s
	}
	n := maxMatchText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
