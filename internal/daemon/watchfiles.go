package daemon

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// PathWatcher watches a dynamic set of file and directory paths and emits
// changed path batches on out until ctx is canceled.
type PathWatcher interface {
	Set(paths []string)
	Run(ctx context.Context, out chan<- []string)
}

// TreeWatcher watches a dynamic set of directory trees and emits changed
// tree roots on out until ctx is canceled or watching fails.
type TreeWatcher interface {
	Set(trees []string)
	Run(ctx context.Context, out chan<- []string) error
}

// FileWatcher constructs path and tree watchers for handleWatchFiles.
type FileWatcher interface {
	NewPathWatcher(logf func(string, ...any)) PathWatcher
	NewTreeWatcher(settle time.Duration) TreeWatcher
}

func (d *Daemon) watcher() FileWatcher {
	if d.fileWatcher != nil {
		return d.fileWatcher
	}
	return defaultFileWatcher()
}

// A window follows the files it shows with watch_files: the folders it
// lists, the files open in its editor, the work trees whose changes it
// shows. What decides that a path changed is the configured FileWatcher
// (ggui's fswatch on desktop/CLI, which stats what it watches and is woken
// by the system's notifications); the window then reads what changed again,
// as it would have on its next poll.
//
// A work tree is followed with its git directory's index and HEAD, which
// what is staged and what is checked out are in: a file staged changes
// nothing in the tree. A git status may rewrite the index too, which only
// has the window read the changes once more; the window's own runs with
// --no-optional-locks, which never writes it.

// handleWatchFiles follows the files and folders a front-end names, until
// it hangs up, telling it of each batch of changes. A WatchFiles it sends
// later on the stream takes the place of what was followed.
func (d *Daemon) handleWatchFiles(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) (err error) {
	req, err := protocol.Body[*protocol.WatchFiles](env)
	if err != nil {
		return err
	}
	if d.isDirectSSH(req.GetNodeId()) {
		return d.handleDirectSSHWatchFiles(ctx, stream, env, req)
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeWatchFiles, req, stream)
	}
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)

	w := d.watcher()
	files := w.NewPathWatcher(d.fswatchLog)
	// A tree is followed as the system tells of changes below it, with
	// nothing kept of each file: what it costs does not grow with the
	// tree, as a home folder followed as a work tree once had it hold
	// millions of paths.
	trees := w.NewTreeWatcher(treeSettle)
	changedFiles, changedTrees := make(chan []string), make(chan []string)
	filesDone := make(chan struct{})
	// treesEnded says the trees' Run returned; nil while it runs.
	treesEnded := make(chan error, 1)
	treesRunning := false
	runTrees := func() {
		treesRunning = true
		go func() { treesEnded <- trees.Run(ctx, changedTrees) }()
	}
	var names watchNames
	follow := func(r *protocol.WatchFiles) {
		names = newWatchNames(r)
		files.Set(slices.Concat(names.expand(r.GetPaths()), slices.Collect(maps.Keys(names.gitFiles))))
		trees.Set(names.trees)
		if !treesRunning && len(names.trees) > 0 {
			runTrees() // once more, as what is followed changed
		}
	}
	follow(req)
	go func() {
		defer close(filesDone)
		files.Run(ctx, changedFiles)
	}()
	// What the front-end sends after: what to follow now, until it hangs up.
	next := make(chan *protocol.WatchFiles)
	h := watchStreamFrames(stream, func(env *protocol.Envelope) bool {
		if r, err := protocol.Body[*protocol.WatchFiles](env); err == nil {
			select {
			case next <- r:
			case <-ctx.Done():
				return false
			}
		}
		return true
	})
	defer func() {
		cancel()
		err = h.finish(parentCtx, env.GetId(), err)
		for filesDone != nil || treesRunning {
			select {
			case <-changedFiles:
			case <-changedTrees:
			case <-filesDone:
				filesDone = nil
			case <-treesEnded:
				treesRunning = false
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
		case batch := <-changedFiles:
			paths, trees := names.split(batch)
			out = &protocol.FilesChanged{Paths: names.shown(paths), Trees: trees}
		case changed := <-changedTrees:
			out = &protocol.FilesChanged{Trees: names.shown(changed)}
		case err := <-treesEnded:
			treesRunning = false
			if err == nil {
				continue // ended with the stream
			}
			d.log.Info("watching files: work trees left to the window", "err", err)
			out = &protocol.FilesChanged{Unwatched: names.shown(names.trees)}
		case r := <-next:
			follow(r)
			continue
		case err := <-h.gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		if len(out.GetPaths()) == 0 && len(out.GetTrees()) == 0 && len(out.GetUnwatched()) == 0 {
			continue
		}
		if err := stream.Send(protocol.TypeFilesChanged, env.GetId(), out); err != nil {
			return err
		}
	}
}

// treeSettle is how long a tree's changes are gathered before they are
// told: a build or a checkout writing thousands of files is told of once,
// and a tree that never rests, as a home folder's caches, has its status
// read at most a few times a second.
const treeSettle = 300 * time.Millisecond

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
	// A tree outside a work tree has no status to read again.
	for _, t := range r.GetTrees() {
		tree := filepath.Clean(mcp.ExpandHome(t))
		gitDir := gitstat.GitDir(tree)
		if gitDir == "" {
			continue
		}
		n.trees = append(n.trees, tree)
		n.gitFiles[filepath.Join(gitDir, "index")] = t
		n.gitFiles[filepath.Join(gitDir, "HEAD")] = t
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
