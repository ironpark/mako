package daemon

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/workspace"
)

// gitWatchFallbackInterval is how often daemonGitHost.Watch polls gitstat.Stamp
// when filesystem tree watching is unavailable or stops with an error. It is a
// var so unit tests can shorten it.
var gitWatchFallbackInterval = 5 * time.Second

func (d *Daemon) pluginGitHost() plugin.GitHost { return daemonGitHost{d} }

func (d *Daemon) pluginForgeHost() plugin.ForgeHost { return daemonForgeHost{d} }

type daemonGitHost struct{ d *Daemon }

var _ plugin.GitHost = daemonGitHost{}

type daemonForgeHost struct{ d *Daemon }

var _ plugin.ForgeHost = daemonForgeHost{}

func (d *Daemon) isLocalGitNode(node string) bool {
	return node == "" || node == protocol.LocalScopeNode || d.local(node)
}

func (d *Daemon) canonicalGitNode(node string) string {
	if d.isLocalGitNode(node) {
		return d.config().NodeID
	}
	return node
}

// localSessionSnapshots returns the daemon's live sessions followed by its
// saved (non-live) sessions without performing disk git HEAD reads.
func (d *Daemon) localSessionSnapshots() []*protocol.SessionInfo {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	var out []*protocol.SessionInfo
	live := map[string]bool{}
	if d.sessions != nil {
		out = d.sessions.list()
		for _, info := range out {
			live[info.GetId()] = true
		}
	}
	for id, entry := range d.saved {
		if !live[id] && entry.Info != nil {
			out = append(out, proto.CloneOf(entry.Info))
		}
	}
	return out
}

// localWorktreeAt returns a copy of the session worktree sitting at dir on this
// node, or nil if none matches.
func (d *Daemon) localWorktreeAt(dir string) *protocol.Worktree {
	if dir == "" {
		return nil
	}
	for _, info := range d.localSessionSnapshots() {
		if w := info.GetWorktree(); w != nil && w.GetPath() != "" && sameLocalPath(w.GetPath(), dir) {
			return proto.CloneOf(w)
		}
	}
	return nil
}

// remoteWorktreeAt returns a copy of the peer-reported session worktree sitting
// at dir on nodeID, or nil if none matches.
func (d *Daemon) remoteWorktreeAt(nodeID, dir string) *protocol.Worktree {
	if nodeID == "" || dir == "" {
		return nil
	}
	for _, info := range d.meshSessions(d.routing()) {
		if info.GetNodeId() != nodeID {
			continue
		}
		if w := info.GetWorktree(); w != nil && w.GetPath() != "" && sameRemotePath(w.GetPath(), dir) {
			return proto.CloneOf(w)
		}
	}
	return nil
}

func sameLocalPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	return cleanPath(a) == cleanPath(b)
}

func localPathWithin(target, base string) bool {
	if target == "" || base == "" {
		return false
	}
	if pathscope.Within(filepath.Clean(target), filepath.Clean(base)) {
		return true
	}
	return pathscope.Within(cleanPath(target), cleanPath(base))
}

func cleanRemotePath(p string) string {
	if p == "" {
		return ""
	}
	s := strings.ReplaceAll(p, `\`, "/")
	s = path.Clean(s)
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	return s
}

func hasDotDotPath(p string) bool {
	for seg := range strings.SplitSeq(strings.ReplaceAll(p, `\`, "/"), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func sameRemotePath(a, b string) bool {
	if a == "" || b == "" || hasDotDotPath(a) || hasDotDotPath(b) {
		return false
	}
	return cleanRemotePath(a) == cleanRemotePath(b)
}

func remotePathWithin(target, base string) bool {
	if target == "" || base == "" || hasDotDotPath(target) || hasDotDotPath(base) {
		return false
	}
	return pathscope.Within(cleanRemotePath(target), cleanRemotePath(base))
}

// Allowed reports whether t lies within a registered project location on t.Node
// or a live/saved/peer-reported session folder or worktree on t.Node.
func (g daemonGitHost) Allowed(t plugin.GitTarget) bool {
	d := g.d
	if t.Path == "" || strings.ContainsRune(t.Path, 0) || hasDotDotPath(t.Path) {
		return false
	}
	local := d.isLocalGitNode(t.Node)
	nodeID := d.canonicalGitNode(t.Node)
	st := d.workspace.get(false).GetState()

	if local {
		if !filepath.IsAbs(t.Path) {
			return false
		}
		for _, p := range d.projects.get() {
			for _, loc := range p.GetPaths() {
				if localPathWithin(t.Path, loc) {
					return true
				}
			}
		}
		if st != nil && nodeID != "" {
			for _, sc := range workspace.Scopes(st, nodeID) {
				for _, loc := range sc.GetPaths() {
					if localPathWithin(t.Path, loc) {
						return true
					}
				}
			}
		}
		for _, info := range d.localSessionSnapshots() {
			if p := info.GetProject(); p != "" && localPathWithin(t.Path, p) {
				return true
			}
			if w := info.GetWorktree(); w != nil {
				if w.GetPath() != "" && localPathWithin(t.Path, w.GetPath()) {
					return true
				}
				if w.GetRepo() != "" && localPathWithin(t.Path, w.GetRepo()) {
					return true
				}
			}
			if r := info.GetRepo(); r != "" && localPathWithin(t.Path, r) {
				return true
			}
		}
		return false
	}

	if st != nil && nodeID != "" {
		for _, sc := range workspace.Scopes(st, nodeID) {
			for _, loc := range sc.GetPaths() {
				if remotePathWithin(t.Path, loc) {
					return true
				}
			}
		}
	}
	for _, info := range d.meshSessions(d.routing()) {
		if info.GetNodeId() != nodeID {
			continue
		}
		if p := info.GetProject(); p != "" && remotePathWithin(t.Path, p) {
			return true
		}
		if w := info.GetWorktree(); w != nil {
			if w.GetPath() != "" && remotePathWithin(t.Path, w.GetPath()) {
				return true
			}
			if w.GetRepo() != "" && remotePathWithin(t.Path, w.GetRepo()) {
				return true
			}
		}
		if r := info.GetRepo(); r != "" && remotePathWithin(t.Path, r) {
			return true
		}
	}
	return false
}

func (g daemonGitHost) Status(ctx context.Context, t plugin.GitTarget, base string) (*protocol.GitChanges, error) {
	d := g.d
	req := &protocol.GitStatus{Path: t.Path, Base: base}
	if d.isLocalGitNode(t.Node) {
		return d.gitStatusLocal(ctx, req)
	}
	req.NodeId = t.Node
	return forward[*protocol.GitChanges](ctx, d, protocol.TypeGitStatus, req)
}

func (g daemonGitHost) Log(ctx context.Context, t plugin.GitTarget, max int, known string) (*protocol.GitHistory, error) {
	d := g.d
	req := &protocol.GitLog{Root: t.Path, Max: int64(max), Known: known}
	if d.isLocalGitNode(t.Node) {
		return d.gitLogLocal(ctx, req)
	}
	req.NodeId = t.Node
	return forward[*protocol.GitHistory](ctx, d, protocol.TypeGitLog, req)
}

func (g daemonGitHost) Show(ctx context.Context, t plugin.GitTarget, hash string) (*protocol.GitCommitInfo, error) {
	d := g.d
	req := &protocol.GitShow{Root: t.Path, Hash: hash}
	if d.isLocalGitNode(t.Node) {
		return d.gitShowLocal(ctx, req)
	}
	req.NodeId = t.Node
	return forward[*protocol.GitCommitInfo](ctx, d, protocol.TypeGitShow, req)
}

func (g daemonGitHost) Run(ctx context.Context, t plugin.GitTarget, r *protocol.GitRun, worktree bool) (string, error) {
	if r == nil {
		return "", errors.New("missing git run request")
	}
	d := g.d
	req := proto.CloneOf(r)
	req.Root = t.Path
	// Never trust a caller/plugin-supplied Worktree; resolve it daemon-side.
	req.Worktree = nil

	local := d.isLocalGitNode(t.Node)
	if worktree {
		var wt *protocol.Worktree
		if local {
			wt = d.localWorktreeAt(t.Path)
			if wt == nil {
				if top := gitWorkTreeTop(t.Path); top != "" {
					wt = d.localWorktreeAt(top)
				}
			}
		} else {
			wt = d.remoteWorktreeAt(t.Node, t.Path)
		}
		if wt == nil {
			return "", errors.New("no session worktree at target folder")
		}
		req.Worktree = wt
	}

	if local {
		res, err := d.gitRunLocal(ctx, req)
		return res.GetOutput(), err
	}
	req.NodeId = t.Node
	res, err := forward[*protocol.GitResult](ctx, d, protocol.TypeGitRun, req)
	return res.GetOutput(), err
}

func (g daemonGitHost) Watch(ctx context.Context, t plugin.GitTarget, changed func()) error {
	if g.d.isLocalGitNode(t.Node) {
		return g.watchLocal(ctx, t, changed)
	}
	return g.watchRemote(ctx, t, changed)
}

func (g daemonGitHost) watchLocal(ctx context.Context, t plugin.GitTarget, changed func()) error {
	d := g.d
	if err := absolute(t.Path); err != nil {
		return err
	}
	wctx, cancel := context.WithCancel(ctx)

	dir := filepath.Clean(mcp.ExpandHome(t.Path))
	names := newWatchNames(&protocol.WatchFiles{Trees: []string{dir}})

	w := d.watcher()
	files := w.NewPathWatcher(d.fswatchLog)
	trees := w.NewTreeWatcher(treeSettle)
	files.Set(localGitWatchPaths(dir, names))
	trees.Set(names.trees)

	changedFiles := make(chan []string)
	changedTrees := make(chan []string)
	filesDone := make(chan struct{})
	treesEnded := make(chan error, 1)
	treesRunning := false

	go func() {
		defer close(filesDone)
		files.Run(wctx, changedFiles)
	}()
	if len(names.trees) > 0 {
		treesRunning = true
		go func() { treesEnded <- trees.Run(wctx, changedTrees) }()
	}

	feed, stopChanges := d.changes.watch()
	defer stopChanges()

	defer func() {
		cancel()
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

	notify := func() {
		if changed != nil {
			changed()
		}
	}

	treeTop := ""
	if len(names.trees) > 0 {
		treeTop = names.trees[0]
	}
	lastRepoStamp := d.repoStampFor(t.Node, dir, treeTop)
	lastStamp := gitstat.Stamp(dir)

	var fallback *time.Ticker
	var fallbackC <-chan time.Time
	startFallback := func() {
		if fallback != nil {
			return
		}
		interval := gitWatchFallbackInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		fallback = time.NewTicker(interval)
		fallbackC = fallback.C
	}
	defer func() {
		if fallback != nil {
			fallback.Stop()
		}
	}()
	if !treesRunning {
		startFallback()
	}

	for {
		select {
		case <-wctx.Done():
			return nil
		case batch := <-changedFiles:
			if len(batch) > 0 {
				// Refresh watched ref path if HEAD moved to a different branch.
				for _, p := range batch {
					if filepath.Base(p) == "HEAD" {
						files.Set(localGitWatchPaths(dir, names))
						break
					}
				}
				lastStamp = gitstat.Stamp(dir)
				notify()
			}
		case batch := <-changedTrees:
			if len(batch) > 0 {
				lastStamp = gitstat.Stamp(dir)
				notify()
			}
		case err := <-treesEnded:
			treesRunning = false
			if err != nil && wctx.Err() == nil {
				d.log.Info("plugin git watch: falling back to stamp poll", "path", dir, "err", err)
				startFallback()
			}
		case c := <-feed:
			if !c.GetSessions() {
				continue
			}
			if cur := d.repoStampFor(t.Node, dir, treeTop); cur != lastRepoStamp {
				lastRepoStamp = cur
				lastStamp = gitstat.Stamp(dir)
				notify()
			}
		case <-fallbackC:
			if cur := gitstat.Stamp(dir); cur != lastStamp {
				lastStamp = cur
				notify()
			}
		}
	}
}

func (g daemonGitHost) watchRemote(ctx context.Context, t plugin.GitTarget, changed func()) error {
	d := g.d
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	feed, stopChanges := d.changes.watch()
	defer stopChanges()

	notify := func() {
		if changed != nil {
			changed()
		}
	}

	lastRepoStamp := d.repoStampFor(t.Node, t.Path, "")

	var fallback *time.Ticker
	var fallbackC <-chan time.Time
	startFallback := func() {
		if fallback != nil {
			return
		}
		interval := gitWatchFallbackInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		fallback = time.NewTicker(interval)
		fallbackC = fallback.C
	}
	defer func() {
		if fallback != nil {
			fallback.Stop()
		}
	}()

	remoteCh := make(chan *protocol.FilesChanged, 8)
	var remoteDone chan struct{}

	ack, stream, err := open[*protocol.FilesChanged](wctx, d, protocol.TypeWatchFiles, &protocol.WatchFiles{
		NodeId: t.Node,
		Trees:  []string{t.Path},
	})
	if err != nil {
		if wctx.Err() != nil {
			return nil
		}
		d.log.Info("plugin git watch: remote watch unavailable, falling back to poll", "node", t.Node, "path", t.Path, "err", err)
		startFallback()
	} else {
		if len(ack.GetTrees()) > 0 || len(ack.GetPaths()) > 0 {
			notify()
		}
		if len(ack.GetUnwatched()) > 0 {
			startFallback()
		}
		remoteDone = make(chan struct{})
		// Closing stream unblocks stream.Recv() when wctx ends.
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			<-wctx.Done()
			_ = stream.Close()
		}()
		go func() {
			defer close(remoteDone)
			defer stream.Close()
			for {
				env, err := stream.Recv()
				if err != nil {
					return
				}
				fc, err := protocol.Body[*protocol.FilesChanged](env)
				if err != nil {
					continue
				}
				select {
				case remoteCh <- fc:
				case <-wctx.Done():
					return
				}
			}
		}()
		defer func() {
			cancel()
			_ = stream.Close()
			<-remoteDone
			<-closeDone
		}()
	}

	for {
		select {
		case <-wctx.Done():
			return nil
		case fc := <-remoteCh:
			if len(fc.GetUnwatched()) > 0 {
				startFallback()
			}
			if len(fc.GetTrees()) > 0 || len(fc.GetPaths()) > 0 {
				notify()
			}
		case <-remoteDone:
			remoteDone = nil
			if wctx.Err() == nil {
				startFallback()
			}
		case c := <-feed:
			if !c.GetSessions() {
				continue
			}
			if cur := d.repoStampFor(t.Node, t.Path, ""); cur != lastRepoStamp {
				lastRepoStamp = cur
				notify()
			}
		case <-fallbackC:
			notify()
		}
	}
}

// localGitWatchPaths builds the path-watcher set for a local repository:
// index and HEAD (from newWatchNames), plus the common git dir's packed-refs,
// refs, refs/heads, and the currently checked-out branch ref file if HEAD is
// symbolic.
func localGitWatchPaths(dir string, names watchNames) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for p := range maps.Keys(names.gitFiles) {
		add(p)
	}
	if gitDir := gitstat.GitDir(dir); gitDir != "" {
		common := gitCommonDir(gitDir)
		add(filepath.Join(common, "packed-refs"))
		add(filepath.Join(common, "refs"))
		add(filepath.Join(common, "refs", "heads"))
		add(filepath.Join(common, "refs", "stash"))
		if b, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
			if ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: "); ok && ref != "" {
				add(filepath.Join(common, filepath.FromSlash(ref)))
			}
		}
	}
	return out
}

// gitCommonDir resolves a linked worktree's shared git directory via commondir,
// falling back to gitDir itself.
func gitCommonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	common := strings.TrimSpace(string(b))
	if common == "" {
		return gitDir
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common)
}

// gitWorkTreeTop walks up from dir to find the enclosing git worktree root.
func gitWorkTreeTop(dir string) string {
	cur := filepath.Clean(dir)
	for cur != "" {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		up := filepath.Dir(cur)
		if up == cur {
			return ""
		}
		cur = up
	}
	return ""
}

// repoStampFor reproduces the native Git panel's repoStamp: it collects the
// sorted "sessionID=gitStamp" entries for sessions on node that share the same
// repository as folder (including linked worktrees).
func (d *Daemon) repoStampFor(node, folder, top string) string {
	local := d.isLocalGitNode(node)
	nodeID := d.canonicalGitNode(node)

	var sessions []*protocol.SessionInfo
	if local {
		sessions = d.listSessions()
	} else {
		sessions = d.meshSessions(d.routing())
	}

	repos := map[string]bool{}
	if local {
		if r := gitstat.Repo(folder); r != "" {
			repos[cleanPath(r)] = true
		}
	}
	for _, s := range sessions {
		if s.GetNodeId() != nodeID || s.GetRepo() == "" {
			continue
		}
		p := s.GetProject()
		if p == "" {
			continue
		}
		var match bool
		if local {
			match = localPathWithin(p, folder) ||
				(top != "" && localPathWithin(p, top)) ||
				localPathWithin(folder, p)
		} else {
			match = remotePathWithin(p, folder) ||
				(top != "" && remotePathWithin(p, top)) ||
				remotePathWithin(folder, p)
		}
		if match {
			repoKey := s.GetRepo()
			if local {
				repoKey = cleanPath(repoKey)
			} else {
				repoKey = cleanRemotePath(repoKey)
			}
			repos[repoKey] = true
		}
	}
	if len(repos) == 0 {
		return ""
	}
	var stamps []string
	for _, s := range sessions {
		if s.GetNodeId() != nodeID || s.GetGitStamp() == "" || s.GetRepo() == "" {
			continue
		}
		repoKey := s.GetRepo()
		if local {
			repoKey = cleanPath(repoKey)
		} else {
			repoKey = cleanRemotePath(repoKey)
		}
		if repos[repoKey] {
			stamps = append(stamps, s.GetId()+"="+s.GetGitStamp())
		}
	}
	slices.Sort(stamps)
	return strings.Join(stamps, ",")
}

// BranchStatus reads the pull request and newest commit CI runs for branch (or
// the currently checked-out branch) on the repository's forge, locally or
// forwarded to a peer.
func (f daemonForgeHost) BranchStatus(ctx context.Context, t plugin.GitTarget, branch string) (*plugin.ForgeBranch, error) {
	d := f.d
	local := d.isLocalGitNode(t.Node)

	useCurrent := branch == ""
	if local && !useCurrent {
		if cur := gitstat.Branch(t.Path); cur != "" && cur == branch {
			useCurrent = true
		}
	}

	prReq := &protocol.ForgeList{
		Root:    t.Path,
		Kind:    protocol.ForgePR,
		Current: useCurrent,
	}
	runsReq := &protocol.ForgeList{
		Root:    t.Path,
		Kind:    protocol.ForgeRuns,
		Current: useCurrent,
		Max:     20,
	}
	if !useCurrent {
		prReq.Branch = branch
		prReq.State = "all"
		runsReq.Branch = branch
	}

	fetchList := func(req *protocol.ForgeList) (*protocol.ForgeItems, error) {
		if local {
			return d.forgeListLocal(ctx, req)
		}
		req.NodeId = t.Node
		return forward[*protocol.ForgeItems](ctx, d, protocol.TypeForgeList, req)
	}

	var (
		prRes, runsRes *protocol.ForgeItems
		prErr, runsErr error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		prRes, prErr = fetchList(prReq)
	}()
	runsRes, runsErr = fetchList(runsReq)
	<-done

	if err := cmp.Or(prErr, runsErr); err != nil {
		return nil, err
	}

	out := &plugin.ForgeBranch{
		Forge: cmp.Or(runsRes.GetForge(), prRes.GetForge()),
	}
	if items := prRes.GetItems(); len(items) > 0 {
		pr := items[0]
		out.HasPR = true
		out.PRNumber = int32(pr.GetNumber())
		out.PRTitle = pr.GetTitle()
		out.PRState = pr.GetState()
		out.PRChecks = pr.GetChecks()
	}
	if runs := runsRes.GetItems(); len(runs) > 0 {
		sha := runs[0].GetHeadOid()
		var latest []*protocol.ForgeItem
		for _, run := range runs {
			if run.GetHeadOid() == sha {
				latest = append(latest, run)
			}
		}
		if len(latest) > 0 {
			first := latest[0]
			for _, run := range latest {
				if run.GetState() != "completed" || protocol.Failed(run.GetChecks()) {
					first = run
					break
				}
			}
			out.Runs = int32(len(latest))
			out.RunsState = forgeRunsState(latest)
			out.RunNumber = int32(first.GetNumber())
			out.RunHead = first.GetHeadOid()
		}
	}
	return out, nil
}

func forgeRunsState(runs []*protocol.ForgeItem) string {
	if len(runs) == 0 {
		return ""
	}
	out := "success"
	for _, r := range runs {
		switch {
		case r.GetState() != "completed":
			return "pending"
		case protocol.Failed(r.GetChecks()):
			out = "failure"
		}
	}
	return out
}
