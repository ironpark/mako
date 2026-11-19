package daemon

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/ggui/pkg/fswatch"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// A window follows the files it shows with watch_files: the folders it
// lists, the files open in its editor, the work trees whose changes it
// shows. What decides that a path changed is ggui's fswatch, which stats
// what it watches and is woken by the system's notifications; the window
// then reads what changed again, as it would have on its next poll.
//
// A work tree is followed with its git directory's index and HEAD, which
// what is staged and what is checked out are in: a file staged changes
// nothing in the tree. A git status may rewrite the index too, which only
// has the window read the changes once more; the window's own runs with
// --no-optional-locks, which never writes it.

// handleWatchFiles follows the files and folders a front-end names, until
// it hangs up, telling it of each batch of changes. A WatchFiles it sends
// later on the stream takes the place of what was followed.
func (d *Daemon) handleWatchFiles(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.WatchFiles](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeWatchFiles, req, stream)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	files := fswatch.New(fswatch.Options{Logf: d.fswatchLog})
	trees := fswatch.New(fswatch.Options{
		Recursive: true,
		Ignore:    fswatch.NewIgnore(fswatch.IgnoreConfig{Patterns: fswatch.DefaultPatterns, Git: true}).Match,
		Logf:      d.fswatchLog,
	})
	var names watchNames
	follow := func(r *protocol.WatchFiles) {
		names = newWatchNames(r)
		files.Set(slices.Concat(names.expand(r.GetPaths()), slices.Collect(maps.Keys(names.gitFiles))))
		trees.Set(names.expand(r.GetTrees()))
	}
	follow(req)

	changedFiles, changedTrees := make(chan fswatch.Batch), make(chan fswatch.Batch)
	go files.Run(ctx, changedFiles)
	go trees.Run(ctx, changedTrees)
	// What the front-end sends after: what to follow now, until it hangs up.
	next, gone := make(chan *protocol.WatchFiles), make(chan error, 1)
	go func() {
		for {
			env, err := stream.Recv()
			if err != nil {
				gone <- err
				return
			}
			if r, err := protocol.Body[*protocol.WatchFiles](env); err == nil {
				select {
				case next <- r:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// The first says it watches.
	if err := stream.Send(protocol.TypeFilesChanged, env.GetId(), &protocol.FilesChanged{}); err != nil {
		return err
	}
	for {
		var out *protocol.FilesChanged
		select {
		case b := <-changedFiles:
			paths, trees := names.split(b.Paths())
			out = &protocol.FilesChanged{Paths: names.shown(paths), Trees: trees}
		case b := <-changedTrees:
			out = &protocol.FilesChanged{Trees: names.treesOf(b.Paths())}
		case r := <-next:
			follow(r)
			continue
		case err := <-gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		if len(out.GetPaths()) == 0 && len(out.GetTrees()) == 0 {
			continue
		}
		if err := stream.Send(protocol.TypeFilesChanged, env.GetId(), out); err != nil {
			return err
		}
	}
}

// fswatchLog passes on what a watcher says that is the user's to know,
// as running out of the system's watches.
func (d *Daemon) fswatchLog(format string, args ...any) {
	d.log.Warn("watching files: " + strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// watchNames are the paths a front-end gave, as it wrote them, by the
// paths they name: "~/x" as the home's, which is what is watched and
// what changes are found under.
type watchNames struct {
	written  map[string]string // by the path watched
	trees    []string          // the trees watched
	gitFiles map[string]string // the trees' index and HEAD, to the tree as written
}

func newWatchNames(r *protocol.WatchFiles) watchNames {
	n := watchNames{written: map[string]string{}, gitFiles: map[string]string{}}
	for _, p := range slices.Concat(r.GetPaths(), r.GetTrees()) {
		if e := filepath.Clean(mcp.ExpandHome(p)); e != p {
			n.written[e] = p
		}
	}
	for _, t := range r.GetTrees() {
		tree := filepath.Clean(mcp.ExpandHome(t))
		n.trees = append(n.trees, tree)
		if gitDir := gitstat.GitDir(tree); gitDir != "" {
			n.gitFiles[filepath.Join(gitDir, "index")] = t
			n.gitFiles[filepath.Join(gitDir, "HEAD")] = t
		}
	}
	return n
}

// split is paths without the trees' git files, and the trees those that
// changed are of, as written, each once.
func (n watchNames) split(paths []string) (rest, trees []string) {
	for _, p := range paths {
		if t, ok := n.gitFiles[p]; ok {
			if !slices.Contains(trees, t) {
				trees = append(trees, t)
			}
			continue
		}
		rest = append(rest, p)
	}
	return rest, trees
}

// expand is paths as watched.
func (n watchNames) expand(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			out = append(out, filepath.Clean(mcp.ExpandHome(p)))
		}
	}
	return out
}

// shown is paths as the front-end wrote the paths they are, or are under.
func (n watchNames) shown(paths []string) []string {
	if len(n.written) == 0 {
		return paths
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		for watched, written := range n.written {
			if rest, ok := strings.CutPrefix(p, watched); ok && (rest == "" || rest[0] == filepath.Separator) {
				out[i] = written + filepath.ToSlash(rest)
				break
			}
		}
	}
	return out
}

// treesOf is the trees paths are under, as the front-end wrote them,
// each once.
func (n watchNames) treesOf(paths []string) []string {
	var out []string
	for _, t := range n.trees {
		if slices.ContainsFunc(paths, func(p string) bool { return pathscope.Within(p, t) }) {
			out = append(out, n.shown([]string{t})[0])
		}
	}
	return out
}
