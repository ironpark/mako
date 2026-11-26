package gui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
)

// Importing git projects from a folder: scans a chosen directory (on this
// machine or a reachable remote node) up to a configurable depth, previews the
// discovered git repositories with their remote owners, and adds the selected
// ones to the sidebar.

const (
	defaultImportDepth = 3
	maxImportDepth     = 5
	maxRemoteScanDirs  = 300
	remoteScanWorkers  = 4
	maxGitConfigBytes  = 64 << 10
)

// importSkipDirs are common dependency, build, and OS directories skipped while
// scanning for git repositories.
var importSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	"venv":         true,
	"env":          true,
	"Pods":         true,
	"DerivedData":  true,
	"Library":      true,
	"Applications": true,
}

// discoveredRepo is a git repository found under a scanned folder.
type discoveredRepo struct {
	Path     string // canonical path to the repository root on the target node
	Rel      string // display path relative to the scanned root
	Name     string // repository folder name
	Owner    string // host/owner from origin remote, "" if none
	Remote   string // host/owner/repo from origin remote, "" if none
	Existing bool   // already listed in the sidebar on the target node
}

// importAsk is the state of the "Import git projects" dialog.
type importAsk struct {
	Open     *ggui.StateValue[bool]
	Node     *ggui.StateValue[string]
	Folder   *ggui.StateValue[string]
	Depth    *ggui.StateValue[int]
	Query    *ggui.StateValue[string]
	Scanning *ggui.StateValue[bool]
	Err      *ggui.StateValue[string]
	Repos    *ggui.StateValue[[]discoveredRepo]
	Selected *ggui.StateValue[map[string]bool]

	targetSpace string
	scanGen     uint64
	cancel      context.CancelFunc
}

func newImportAsk() *importAsk {
	return &importAsk{
		Open:     ggui.State(false),
		Node:     ggui.State(""),
		Folder:   ggui.State(""),
		Depth:    ggui.State(defaultImportDepth),
		Query:    ggui.State(""),
		Scanning: ggui.State(false),
		Err:      ggui.State(""),
		Repos:    ggui.State([]discoveredRepo(nil)).WithEqual(slices.Equal),
		Selected: ggui.State(map[string]bool{}).WithEqual(maps.Equal),
	}
}

// addProjectMenu is the sidebar header's + button: adds a single folder or
// imports git repositories discovered under a folder.
func addProjectMenu(m *model) ggui.Widget {
	return addProjectMenuFor(m, "")
}

func addProjectMenuFor(m *model, spaceID string) ggui.Widget {
	return iconMenu(ui.MenuOf(icon("plus"),
		ui.MenuItem(T("Add folder…"), m.addProjectFolder).Icon(icon("folder-plus").Size(14)),
		ui.MenuItem(T("Clone from Git URL…"), func() { m.openCloneProjectIn(spaceID) }).Icon(icon("git-branch").Size(14)),
		ui.MenuItem(T("Import git projects…"), func() { m.openImportProjectsIn(spaceID) }).Icon(icon("lang-git").Size(14)),
	).Name(T("Add a project")), 6)
}

// openImportProjects opens the dialog to scan a folder for git repositories.
func (m *model) openImportProjects() {
	m.openImportProjectsIn("")
}

func (m *model) openImportProjectsIn(spaceID string) {
	imp := m.Proj.Import
	imp.stopScan()
	imp.targetSpace = spaceID
	node := ggui.Peek(m.Conn).Self.GetId()
	if filtered := ggui.Peek(m.Prefs).ProjectMachine; filtered != "" {
		if n, ok := nodeIn(ggui.Peek(m.Nodes), filtered); ok && reachable(n) {
			node = filtered
		}
	}
	imp.Node.Set(node)
	imp.Folder.Set("")
	imp.Depth.Set(defaultImportDepth)
	imp.Query.Set("")
	imp.Scanning.Set(false)
	imp.Err.Set("")
	imp.Repos.Set(nil)
	imp.Selected.Set(map[string]bool{})
	imp.Open.Set(true)
}

func (imp *importAsk) stopScan() {
	imp.scanGen++
	if imp.cancel != nil {
		imp.cancel()
		imp.cancel = nil
	}
	imp.Scanning.Set(false)
}

// startImportScan scans the currently configured folder and node for git
// repositories in the background.
func (m *model) startImportScan() {
	imp := m.Proj.Import
	node := ggui.Peek(imp.Node)
	dir := strings.TrimSpace(ggui.Peek(imp.Folder))
	depth := min(max(ggui.Peek(imp.Depth), 1), maxImportDepth)
	imp.stopScan()
	imp.Err.Set("")
	imp.Repos.Set(nil)
	imp.Selected.Set(map[string]bool{})
	if dir == "" {
		return
	}
	gen := imp.scanGen
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	imp.cancel = cancel
	imp.Scanning.Set(true)
	local := m.onThisMachine(node)
	remoteID := m.remoteID(node)

	finish := func(repos []discoveredRepo, err error) {
		if imp.scanGen != gen {
			return
		}
		imp.Scanning.Set(false)
		imp.cancel = nil
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			imp.Err.Set(nodeErr(err))
			return
		}
		marked := m.markExistingRepos(node, repos)
		sel := make(map[string]bool, len(marked))
		for _, r := range marked {
			if !r.Existing {
				sel[r.Path] = true
			}
		}
		imp.Repos.Set(marked)
		imp.Selected.Set(sel)
	}

	if local {
		post := m.post
		go func() {
			defer cancel()
			repos, err := scanLocalGitRepos(ctx, dir, depth)
			if post != nil {
				post(func() { finish(repos, err) })
			} else {
				finish(repos, err)
			}
		}()
		return
	}

	post := m.post
	if post == nil {
		cancel()
		imp.Scanning.Set(false)
		return
	}
	go func() {
		defer cancel()
		c, err := m.link.client(ctx)
		var repos []discoveredRepo
		if err == nil {
			repos, err = scanRemoteGitRepos(ctx, c, remoteID, dir, depth)
		}
		post(func() { finish(repos, err) })
	}()
}

// markExistingRepos flags repositories that are already tracked in the sidebar
// on node (as a kept folder, a project root location, or an active session).
func (m *model) markExistingRepos(node string, repos []discoveredRepo) []discoveredRepo {
	if len(repos) == 0 {
		return nil
	}
	existing := map[string]bool{}
	for _, k := range ggui.Peek(m.Prefs).Projects {
		if k.Node == node {
			existing[k.Path] = true
		}
	}
	for _, p := range m.projectsNow() {
		for _, l := range p.locations() {
			if l.Node == node {
				existing[l.Path] = true
			}
		}
	}
	for _, s := range ggui.Peek(m.Sessions) {
		if s.GetNodeId() == node {
			existing[projectOf(s)] = true
		}
	}
	out := slices.Clone(repos)
	for i := range out {
		if existing[out[i].Path] {
			out[i].Existing = true
		}
	}
	return out
}

// importSelectedRepos adds every checked, non-existing repository from the
// import dialog to the sidebar and closes the dialog.
func (m *model) importSelectedRepos() {
	imp := m.Proj.Import
	node := ggui.Peek(imp.Node)
	targetSpace := imp.targetSpace
	sel := ggui.Peek(imp.Selected)
	var chosen []discoveredRepo
	for _, r := range ggui.Peek(imp.Repos) {
		if !r.Existing && sel[r.Path] {
			chosen = append(chosen, r)
		}
	}
	if len(chosen) == 0 {
		return
	}
	imp.stopScan()
	imp.Open.Set(false)
	m.importGitReposIn(node, chosen, targetSpace)
}

type importPlacement struct {
	MatchedDef     bool
	Join           bool
	JoinDef        string
	JoinRoot       int
	SpaceIdx       int // > 0 when claimed by an existing project, section, explicit space, or owner rule
	MigrateFromKey string
	Key            string
}

type importPlanContext struct {
	set            spaceSet
	targetSpace    space
	targetSpaceIdx int
	inTargetSpace  bool
	defs           []project // enriched with known remotes from sessions/kept projects
	existingGroups map[string]sessionGroup
}

func (m *model) newImportPlanContext(beforePrefs []keptProject, targetSpaceID string) importPlanContext {
	set := m.spacesNow()
	sessions := ggui.Peek(m.Sessions)
	nodes := ggui.Peek(m.Nodes)

	targetIdx := set.Space
	if targetSpaceID != "" {
		if idx, _, ok := spaceIndexInSet(set, targetSpaceID); ok {
			targetIdx = idx
		}
	}
	targetSp, inTarget := set.spaceOf(targetIdx)

	// Infer remote/owner for any project root whose Remote was never recorded
	// (e.g. added via "Add folder…") from existing sessions and kept projects.
	locMeta := map[fileRoot][2]string{}
	for _, kp := range beforePrefs {
		if kp.Remote != "" {
			locMeta[kp.fileRoot] = [2]string{kp.Remote, kp.Owner}
		}
	}
	for _, s := range sessions {
		if s.GetRemote() != "" {
			locMeta[fileRoot{Node: s.GetNodeId(), Path: projectOf(s)}] = [2]string{s.GetRemote(), s.GetOwner()}
		}
	}
	enriched := make([]project, len(set.Projects))
	for i, p := range set.Projects {
		cp := p.clone()
		for j := range cp.Roots {
			if cp.Roots[j].Remote != "" {
				continue
			}
			for _, l := range cp.Roots[j].Locations {
				if meta, ok := locMeta[l]; ok && meta[0] != "" {
					cp.Roots[j].Remote = meta[0]
					if cp.Roots[j].Owner == "" {
						cp.Roots[j].Owner = meta[1]
					}
					break
				}
			}
		}
		enriched[i] = cp
	}

	groups := groupProjects(sessions, nodes, beforePrefs, set.Projects, set.Names)
	existing := make(map[string]sessionGroup, len(groups))
	for _, g := range groups {
		existing[g.Key] = g
	}

	return importPlanContext{
		set:            set,
		targetSpace:    targetSp,
		targetSpaceIdx: targetIdx,
		inTargetSpace:  inTarget,
		defs:           enriched,
		existingGroups: existing,
	}
}

func projectOwners(p project) []string {
	var owners []string
	for _, r := range p.Roots {
		if r.Owner != "" && !slices.Contains(owners, r.Owner) {
			owners = append(owners, r.Owner)
		}
	}
	return owners
}

func (c importPlanContext) resolve(node string, kp keptProject) importPlacement {
	if pl := placeKept(c.defs, kp); pl.At >= 0 {
		p := c.defs[pl.At]
		owners := projectOwners(p)
		if kp.Owner != "" && !slices.Contains(owners, kp.Owner) {
			owners = append(owners, kp.Owner)
		}
		at, _ := c.set.placeOf(p.ID, owners)
		return importPlacement{
			MatchedDef: true,
			Join:       pl.Join,
			JoinDef:    p.ID,
			JoinRoot:   pl.Root,
			SpaceIdx:   at,
			Key:        p.ID,
		}
	}
	key := sectionKey(c.set.Projects, implicitKey(node, kp.Path, kp.Remote))
	var owners []string
	if kp.Owner != "" {
		owners = []string{kp.Owner}
	}
	if at, why := c.set.placeOf(key, owners); why != byDefault {
		return importPlacement{SpaceIdx: at, Key: key}
	}
	if dk := dirKey(node, kp.Path); dk != key {
		if dirAt, dirWhy := c.set.placeOf(dk, nil); dirWhy == byProject {
			return importPlacement{SpaceIdx: dirAt, MigrateFromKey: dk, Key: key}
		}
	}
	if g, exists := c.existingGroups[key]; exists {
		gOwners := groupOwners(g)
		if kp.Owner != "" && !slices.Contains(gOwners, kp.Owner) {
			gOwners = append(gOwners, kp.Owner)
		}
		existingAt, _ := c.set.placeOf(g.Key, gOwners)
		return importPlacement{SpaceIdx: existingAt, Key: key}
	}
	return importPlacement{Key: key}
}

// importGitRepos adds repos on node to the sidebar's kept folders (and joins
// matching project definitions), assigning looks and space placement, with an
// Undo notice.
func (m *model) importGitRepos(node string, repos []discoveredRepo) int {
	return m.importGitReposIn(node, repos, "")
}

func (m *model) importGitReposIn(node string, repos []discoveredRepo, targetSpaceID string) int {
	if len(repos) == 0 {
		return 0
	}
	beforePrefs := slices.Clone(ggui.Peek(m.Prefs).Projects)
	beforeSpaces := ggui.Peek(m.Spaces)

	var added []keptProject
	m.Prefs.Update(func(p prefs) prefs {
		next := slices.Clone(p.Projects)
		for _, r := range repos {
			kp := keptProject{fileRoot: fileRoot{Node: node, Path: r.Path}, Owner: r.Owner, Remote: r.Remote}
			idx := slices.IndexFunc(next, func(o keptProject) bool { return o.fileRoot == kp.fileRoot })
			if idx >= 0 {
				if next[idx].Owner == "" && kp.Owner != "" {
					next[idx].Owner = kp.Owner
				}
				if next[idx].Remote == "" && kp.Remote != "" {
					next[idx].Remote = kp.Remote
				}
				continue
			}
			next = append(next, kp)
			added = append(added, kp)
		}
		p.Projects = next
		return p
	})
	if len(added) == 0 {
		return 0
	}

	plan := m.newImportPlanContext(beforePrefs, targetSpaceID)
	set := plan.set

	var moveKeys []string
	var joinEdits []struct {
		id     string
		root   int
		at     fileRoot
		remote string
		owner  string
	}
	migrateKeys := map[string]string{}
	newLooks := map[string]projectLook{}

	for _, kp := range added {
		res := plan.resolve(node, kp)
		if res.MatchedDef {
			if res.Join {
				joinEdits = append(joinEdits, struct {
					id     string
					root   int
					at     fileRoot
					remote string
					owner  string
				}{res.JoinDef, res.JoinRoot, kp.fileRoot, kp.Remote, kp.Owner})
			}
			continue
		}
		key := res.Key
		if _, ok := set.Looks[key]; !ok {
			if _, ok := newLooks[key]; !ok {
				newLooks[key] = randomLook()
			}
		}
		if res.MigrateFromKey != "" {
			migrateKeys[res.MigrateFromKey] = key
			continue
		}
		if res.SpaceIdx == 0 && plan.inTargetSpace && !slices.Contains(moveKeys, key) {
			moveKeys = append(moveKeys, key)
		}
	}

	if len(newLooks) > 0 || len(moveKeys) > 0 || len(joinEdits) > 0 || len(migrateKeys) > 0 {
		m.Spaces.Update(func(l spaceList) spaceList {
			if len(newLooks) > 0 {
				l.Looks = maps.Clone(l.Looks)
				if l.Looks == nil {
					l.Looks = map[string]projectLook{}
				}
				for k, v := range newLooks {
					if _, taken := l.Looks[k]; !taken {
						l.Looks[k] = v
					}
				}
			}
			if len(joinEdits) > 0 {
				l.Projects = slices.Clone(l.Projects)
				for _, e := range joinEdits {
					if i := projectByID(l.Projects, e.id); i >= 0 && e.root < len(l.Projects[i].Roots) {
						p := l.Projects[i].clone()
						if !slices.Contains(p.Roots[e.root].Locations, e.at) {
							p.Roots[e.root].Locations = append(p.Roots[e.root].Locations, e.at)
						}
						if p.Roots[e.root].Remote == "" && e.remote != "" {
							p.Roots[e.root].Remote = e.remote
						}
						if p.Roots[e.root].Owner == "" && e.owner != "" {
							p.Roots[e.root].Owner = e.owner
						}
						l.Projects = setProject(l.Projects, p)
					}
				}
			}
			if len(migrateKeys) > 0 {
				spaces := slices.Clone(l.Spaces)
				for i := range spaces {
					for from, to := range migrateKeys {
						spaces[i].Projects = replaceID(spaces[i].Projects, from, to)
					}
				}
				l.Spaces = spaces
			}
			if len(moveKeys) > 0 {
				l.Spaces = placeSections(slices.Clone(l.Spaces), plan.targetSpace.ID, moveKeys...)
			}
			return l
		})
	}

	if m.toaster != nil {
		title := Tf("Imported %d projects", len(added))
		if len(added) == 1 {
			title = T("Imported 1 project")
		}
		m.toaster.Push(ui.Toast(title, "").Action(T("Undo"), func() {
			m.Prefs.Update(func(p prefs) prefs { p.Projects = beforePrefs; return p })
			m.Spaces.Update(func(now spaceList) spaceList {
				now.Projects, now.Looks = beforeSpaces.Projects, beforeSpaces.Looks
				spaces := slices.Clone(now.Spaces)
				for i := range spaces {
					if j := slices.IndexFunc(beforeSpaces.Spaces, func(sp space) bool { return sp.ID == spaces[i].ID }); j >= 0 {
						spaces[i].Projects, spaces[i].Hidden = beforeSpaces.Spaces[j].Projects, beforeSpaces.Spaces[j].Hidden
					}
				}
				now.Spaces = spaces
				return now
			})
		}))
	}
	return len(added)
}

// Local & remote git repository scanning --------------------------------------

func skipImportDir(name string) bool {
	return name == "" || strings.HasPrefix(name, ".") || importSkipDirs[name]
}

// scanLocalGitRepos walks root on the local filesystem up to maxDepth levels
// deep and returns every discovered git repository, stopping at each repository
// root.
func scanLocalGitRepos(ctx context.Context, root string, maxDepth int) ([]discoveredRepo, error) {
	root = filepath.Clean(mcp.ExpandHome(root))
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	visited := map[string]bool{}
	seenRepo := map[string]bool{}
	var out []discoveredRepo

	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			realDir = dir
		}
		if visited[realDir] {
			return nil
		}
		visited[realDir] = true

		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			_, repo, owner, remote := gitstat.Head(dir)
			repoPath := cmp.Or(repo, realDir)
			if !seenRepo[repoPath] {
				seenRepo[repoPath] = true
				rel := relativeRepoPath(realRoot, realDir)
				out = append(out, discoveredRepo{
					Path:   repoPath,
					Rel:    rel,
					Name:   projectName(repoPath),
					Owner:  owner,
					Remote: remote,
				})
			}
			return nil
		}
		if depth >= maxDepth {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if depth == 0 {
				return err
			}
			return nil // ignore unreadable subdirectories
		}
		for _, e := range entries {
			name := e.Name()
			if skipImportDir(name) {
				continue
			}
			isDir := e.IsDir()
			child := filepath.Join(dir, name)
			if !isDir && e.Type()&os.ModeSymlink != 0 {
				if fi, err := os.Stat(child); err == nil {
					isDir = fi.IsDir()
				}
			}
			if !isDir {
				continue
			}
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(realRoot, 0); err != nil {
		return nil, err
	}
	sortDiscoveredRepos(out)
	return out, nil
}

// scanRemoteGitRepos scans root on a remote node using ListDir and ReadFile
// RPCs up to maxDepth levels deep.
func scanRemoteGitRepos(ctx context.Context, c *client.Client, remoteID, root string, maxDepth int) ([]discoveredRepo, error) {
	first, err := c.ListDir(ctx, &protocol.ListDir{NodeId: remoteID, Path: root})
	if err != nil {
		return nil, err
	}
	base := cmp.Or(first.GetPath(), root)

	type task struct {
		dir   string
		depth int
		pre   *protocol.Dir // non-nil for root, already fetched
	}

	var (
		mu       sync.Mutex
		out      []discoveredRepo
		seenRepo = map[string]bool{}
		dirsSeen atomic.Int32
	)
	dirsSeen.Store(1)

	handleDir := func(t task) ([]task, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := t.pre
		if d == nil {
			var err error
			d, err = c.ListDir(ctx, &protocol.ListDir{NodeId: remoteID, Path: t.dir})
			if err != nil {
				return nil, nil // skip unreadable subdirectories
			}
		}
		dirPath := cmp.Or(d.GetPath(), t.dir)
		var gitEntry *protocol.DirEntry
		for _, e := range d.GetEntries() {
			if e.GetName() == ".git" {
				gitEntry = e
				break
			}
		}
		if gitEntry != nil {
			var owner, remote string
			if gitEntry.GetDir() {
				cfgPath := joinPath(dirPath, ".git/config")
				if f, err := c.ReadFile(ctx, &protocol.ReadFile{NodeId: remoteID, Path: cfgPath, Max: maxGitConfigBytes}); err == nil {
					owner, remote = gitstat.ConfigRemote(f.GetData())
				}
			} else {
				owner, remote = readRemoteGitFileConfig(ctx, c, remoteID, dirPath)
			}
			mu.Lock()
			if !seenRepo[dirPath] {
				seenRepo[dirPath] = true
				out = append(out, discoveredRepo{
					Path:   dirPath,
					Rel:    relativeRepoPath(base, dirPath),
					Name:   projectName(dirPath),
					Owner:  owner,
					Remote: remote,
				})
			}
			mu.Unlock()
			return nil, nil
		}
		if t.depth >= maxDepth {
			return nil, nil
		}
		var next []task
		for _, e := range d.GetEntries() {
			if !e.GetDir() || skipImportDir(e.GetName()) {
				continue
			}
			if dirsSeen.Add(1) > maxRemoteScanDirs {
				break
			}
			next = append(next, task{dir: joinPath(dirPath, e.GetName()), depth: t.depth + 1})
		}
		return next, nil
	}

	// Level-by-level BFS with bounded concurrency so higher-level repos are
	// always found before hitting maxRemoteScanDirs.
	frontier := []task{{dir: base, depth: 0, pre: first}}
	for len(frontier) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var (
			nextMu sync.Mutex
			next   []task
			wg     sync.WaitGroup
			jobs   = make(chan task, len(frontier))
		)
		for _, t := range frontier {
			jobs <- t
		}
		close(jobs)
		workers := min(remoteScanWorkers, len(frontier))
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range jobs {
					children, _ := handleDir(t)
					if len(children) > 0 {
						nextMu.Lock()
						next = append(next, children...)
						nextMu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
		frontier = next
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortDiscoveredRepos(out)
	return out, nil
}

func readRemoteGitFileConfig(ctx context.Context, c *client.Client, remoteID, dirPath string) (owner, remote string) {
	f, err := c.ReadFile(ctx, &protocol.ReadFile{NodeId: remoteID, Path: joinPath(dirPath, ".git"), Max: 4096})
	if err != nil {
		return "", ""
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(f.GetData())), "gitdir: ")
	if !ok || gitDir == "" {
		return "", ""
	}
	if !isRemoteAbsPath(gitDir) {
		gitDir = joinPath(dirPath, gitDir)
	}
	common := gitDir
	if cf, err := c.ReadFile(ctx, &protocol.ReadFile{NodeId: remoteID, Path: joinPath(gitDir, "commondir"), Max: 4096}); err == nil {
		if cd := strings.TrimSpace(string(cf.GetData())); cd != "" {
			if isRemoteAbsPath(cd) {
				common = cd
			} else {
				common = joinPath(gitDir, cd)
			}
		}
	}
	cfgFile, err := c.ReadFile(ctx, &protocol.ReadFile{NodeId: remoteID, Path: joinPath(common, "config"), Max: maxGitConfigBytes})
	if err != nil {
		return "", ""
	}
	return gitstat.ConfigRemote(cfgFile.GetData())
}

func isRemoteAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':')
}

func relativeRepoPath(base, dir string) string {
	b := strings.TrimRight(filepath.ToSlash(base), "/")
	d := strings.TrimRight(filepath.ToSlash(dir), "/")
	if b == d {
		return projectName(dir)
	}
	if strings.HasPrefix(d, b+"/") {
		return d[len(b)+1:]
	}
	return projectName(dir)
}

func sortDiscoveredRepos(repos []discoveredRepo) {
	slices.SortStableFunc(repos, func(a, b discoveredRepo) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.Rel), strings.ToLower(b.Rel)),
			cmp.Compare(a.Path, b.Path),
		)
	})
}

// Dialog UI -------------------------------------------------------------------

func importProjectsDialog(m *model) ggui.Widget {
	imp := m.Proj.Import
	was := false
	ggui.Watch(imp.Open, func(on bool) {
		if was && !on {
			imp.stopScan()
		}
		was = on
	})
	return ggui.If(imp.Open, func() ggui.Widget {
		return keyedDialog(imp.Open, importProjectsForm(m)).Title(T("Import git projects")).Width(560)
	})
}

func importProjectsForm(m *model) ggui.Widget {
	imp := m.Proj.Import
	nodeIDs := reachableNodeIDs(m)
	local := ggui.Derived(func() bool { return imp.Node.Get() == m.Conn.Get().Self.GetId() })
	multiNode := ggui.Map(nodeIDs, func(ids []string) bool { return len(ids) > 1 })

	nodeBind := ggui.Bind(imp.Node.Get, func(id string) {
		if id == ggui.Peek(imp.Node) {
			return
		}
		imp.Node.Set(id)
		imp.Folder.Set("")
		imp.stopScan()
		imp.Repos.Set(nil)
		imp.Selected.Set(map[string]bool{})
		imp.Err.Set("")
	})
	depthBind := ggui.Bind(imp.Depth.Get, func(d int) {
		if d == ggui.Peek(imp.Depth) {
			return
		}
		imp.Depth.Set(d)
		if strings.TrimSpace(ggui.Peek(imp.Folder)) != "" {
			m.startImportScan()
		}
	})
	depthLabel := func(d int) string {
		if d == 1 {
			return Tf("%d level", d)
		}
		return Tf("%d levels", d)
	}

	browseLocal := ui.Button(T("Browse…"), func() {
		browseInto(func(dir string) {
			imp.Folder.Set(dir)
			m.startImportScan()
		}, func(d runtime.Dialogs) (string, error) {
			return d.PickFolder(runtime.FileDialog{Title: T("Import git projects"), Directory: ggui.Peek(imp.Folder)})
		})
	}).Outline()

	browseRemote := ui.Button(T("Browse…"), func() {
		m.pickFolderFor(imp.Open, ggui.Peek(imp.Node), ggui.Peek(imp.Folder), imp.Err.Set, func(dir string) {
			if dir != "" {
				imp.Folder.Set(dir)
				m.startImportScan()
			}
		})
	}).Outline()

	folderRow := widget.Row(
		widget.Expanded(ui.TextField(imp.Folder).Placeholder(T("Choose a folder to scan")).Name(T("Folder")).
			OnSubmit(func(string) { m.startImportScan() })),
		ggui.If(local, func() ggui.Widget { return browseLocal }).Else(func() ggui.Widget { return browseRemote }),
		widget.Box(ui.Select(depthBind).Options([]int{1, 2, 3, 4, 5}).Format(depthLabel).Name(T("Scan depth"))).Width(108),
	).Gap(8).Align(widget.AlignCenter)

	results := ggui.Reactive(func() ggui.Widget {
		scanning := imp.Scanning.Get()
		errMsg := imp.Err.Get()
		repos := imp.Repos.Get()
		folder := strings.TrimSpace(imp.Folder.Get())
		query := strings.TrimSpace(strings.ToLower(imp.Query.Get()))
		sel := imp.Selected.Get()

		switch {
		case scanning:
			return widget.Box(widget.Center(muted(widget.Text(T("Scanning for git repositories…")).Size(13)))).Height(260)
		case errMsg != "":
			return widget.Box(widget.Center(widget.Text(errMsg).Size(12.5).Color(skin.DotDown))).Pad(16).Height(260)
		case folder == "":
			return widget.Box(widget.Center(muted(widget.Text(T("Choose a folder to scan for git repositories.")).Size(13)))).Pad(16).Height(260)
		case len(repos) == 0:
			return widget.Box(widget.Center(muted(widget.Text(T("No git repositories found in this folder.")).Size(13)))).Pad(16).Height(260)
		}

		var visible []discoveredRepo
		var selectable []discoveredRepo
		selectedCount := 0
		for _, r := range repos {
			if !r.Existing && sel[r.Path] {
				selectedCount++
			}
			if query != "" &&
				!strings.Contains(strings.ToLower(r.Name), query) &&
				!strings.Contains(strings.ToLower(r.Rel), query) &&
				!strings.Contains(strings.ToLower(r.Owner), query) &&
				!strings.Contains(strings.ToLower(r.Remote), query) {
				continue
			}
			visible = append(visible, r)
			if !r.Existing {
				selectable = append(selectable, r)
			}
		}
		allSelected := len(selectable) > 0
		for _, r := range selectable {
			if !sel[r.Path] {
				allSelected = false
				break
			}
		}

		toggleAllLabel := pick(allSelected, T("Deselect all"), T("Select all"))
		toggleAll := ui.ButtonOf(
			widget.Text(toggleAllLabel).Size(12).Color(skin.Muted).NoWrap(),
			func() {
				next := maps.Clone(ggui.Peek(imp.Selected))
				if next == nil {
					next = map[string]bool{}
				}
				for _, r := range selectable {
					next[r.Path] = !allSelected
				}
				imp.Selected.Set(next)
			},
		).Ghost().Pad(4, 8).Radius(6).Disabled(len(selectable) == 0).Name(toggleAllLabel)

		barItems := []ggui.Widget{
			muted(widget.Text(Tf("%d found · %d selected", len(repos), selectedCount)).Size(12).NoWrap()),
			widget.Spacer(),
			toggleAll,
		}
		var topParts []ggui.Widget
		if len(repos) > 6 {
			topParts = append(topParts, widget.Box(
				ui.TextField(imp.Query).Placeholder(T("Filter repositories…")).Name(T("Filter")),
			).Pad(8, 8, 4, 8))
		}
		topParts = append(topParts,
			widget.Box(widget.Row(barItems...).Gap(8).Align(widget.AlignCenter)).Pad(4, 8, 4, 10),
			hline(),
		)

		node := ggui.Peek(imp.Node)
		plan := m.newImportPlanContext(ggui.Peek(m.Prefs).Projects, imp.targetSpace)
		rows := make([]ggui.Widget, 0, len(visible))
		for _, r := range visible {
			repo := r
			checked := !repo.Existing && sel[repo.Path]
			bind := ggui.Bind(
				func() bool { return checked },
				func(on bool) {
					if repo.Existing {
						return
					}
					next := maps.Clone(ggui.Peek(imp.Selected))
					if next == nil {
						next = map[string]bool{}
					}
					next[repo.Path] = on
					imp.Selected.Set(next)
				},
			)
			nameCol := []ggui.Widget{
				widget.Text(repo.Name).Size(13).Weight(ggui.WeightMedium).
					Color(pick[color.Color](repo.Existing, skin.Muted, skin.Fg)).NoWrap().Ellipsis(),
			}
			if repo.Rel != "" && repo.Rel != repo.Name {
				nameCol = append(nameCol, widget.Text(repo.Rel).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis())
			}
			rowCells := []ggui.Widget{
				ui.Checkbox(bind, "").Disabled(repo.Existing).Name(repo.Name),
				widget.Expanded(widget.Column(nameCol...).Gap(1).Stretch()),
			}
			switch {
			case repo.Existing:
				rowCells = append(rowCells, widget.Box(
					widget.Text(T("Already added")).Size(11).Color(skin.ExtraMuted).NoWrap(),
				).Pad(2, 6).Radius(5).Fill(skin.Surface2))
			default:
				if repo.Owner != "" {
					ownerLabel, host := ownerName(repo.Owner)
					rowCells = append(rowCells, widget.Row(
						icon(ownerGlyph(host)).Size(12).Color(skin.ExtraMuted),
						widget.Text(ownerLabel).Size(11.5).Color(skin.Muted).NoWrap(),
					).Gap(4).Align(widget.AlignCenter))
				}
				kp := keptProject{fileRoot: fileRoot{Node: node, Path: repo.Path}, Owner: repo.Owner, Remote: repo.Remote}
				if res := plan.resolve(node, kp); res.SpaceIdx > 0 && res.SpaceIdx != plan.targetSpaceIdx {
					if sp, ok := plan.set.spaceOf(res.SpaceIdx); ok {
						rowCells = append(rowCells, widget.Box(widget.Row(
							spaceMark(sp),
							widget.Text(plan.set.spaceName(res.SpaceIdx)).Size(11).Color(skin.Muted).NoWrap(),
						).Gap(4).Align(widget.AlignCenter)).Pad(2, 6).Radius(5).Fill(skin.Surface2))
					}
				}
			}
			rows = append(rows, widget.Box(widget.Row(rowCells...).Gap(10).Align(widget.AlignCenter)).Pad(6, 10))
		}
		if len(rows) == 0 {
			rows = append(rows, widget.Box(widget.Center(muted(widget.Text(T("No results")).Size(12.5)))).Pad(24))
		}

		list := widget.Scroll(widget.Column(rows...).Gap(1).Stretch()).Bar(skin.Surface3)
		return widget.Box(widget.Column(append(topParts, widget.Expanded(list))...).Stretch()).Height(260)
	})

	footer := ggui.Reactive(func() ggui.Widget {
		sel := imp.Selected.Get()
		scanning := imp.Scanning.Get()
		count := 0
		for _, r := range imp.Repos.Get() {
			if !r.Existing && sel[r.Path] {
				count++
			}
		}
		label := T("Import")
		if count > 0 {
			label = Tf("Import %d", count)
		}
		return widget.Row(
			ui.Button(T("Cancel"), func() { imp.Open.Set(false) }).Outline(),
			ui.Button(label, m.importSelectedRepos).Disabled(count == 0 || scanning),
		).Gap(8).Justify(widget.JustifyEnd)
	})

	quickPicks := projectDirQuickPicks(m, local, imp.Folder, func(dir string) {
		imp.Folder.Set(dir)
		m.startImportScan()
	})

	return widget.Column(
		muted(widget.Text(T("Scan a folder for git repositories and add them to the sidebar. Existing repositories are skipped.")).Size(12)),
		ggui.If(multiNode, func() ggui.Widget {
			return ui.Field(T("Node"), ui.Select(nodeBind).BindOptions(nodeIDs).Format(func(id string) string {
				return nodeName(ggui.Peek(m.Nodes), id)
			}).Name(T("Node")))
		}),
		ui.Field(T("Folder"), widget.Column(folderRow, quickPicks).Gap(6).Stretch()),
		widget.Box(results).Border(1, skin.Border).Radius(8),
		footer,
	).Gap(12).Stretch()
}
