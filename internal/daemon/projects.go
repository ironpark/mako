package daemon

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Projects: the folders the user put together, as far as they are on this
// node, as a front-end last gave them (SetProjects). The agents in one
// project's folders are one project's, for the agents' MCP tools and the
// Agents panel, whatever folder each works in; outside them an agent's
// project is its own folder, as before. A project may give an agent
// started in one of its folders the others too.

// projectStore keeps the projects, and keeps them on disk, so a daemon
// started again knows them before a window does.
type projectStore struct {
	mu   sync.Mutex
	list []*protocol.ProjectScope
	// real are the folders of each project with their links followed, as
	// a session's folder is compared with them.
	real [][]string
	path string
}

// resolveLocked sets list as the projects kept, and their folders followed.
func (p *projectStore) resolveLocked(list []*protocol.ProjectScope) {
	p.list = list
	p.real = make([][]string, len(list))
	for i, s := range list {
		for _, path := range s.GetPaths() {
			p.real[i] = append(p.real[i], cleanPath(path))
		}
	}
}

// load reads the projects kept in dir, if there are any.
func (p *projectStore) load(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = filepath.Join(dir, "projects.pb")
	data, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	var kept protocol.Projects
	if proto.Unmarshal(data, &kept) == nil {
		p.resolveLocked(kept.GetProjects())
	}
}

// set keeps list, cleaned: absolute folders only, and projects with some.
func (p *projectStore) set(list []*protocol.ProjectScope) *protocol.Projects {
	var clean []*protocol.ProjectScope
	for _, s := range list {
		s = proto.CloneOf(s)
		s.Paths = slices.DeleteFunc(s.Paths, func(path string) bool { return !filepath.IsAbs(path) })
		for i, path := range s.Paths {
			s.Paths[i] = filepath.Clean(path)
		}
		if s.GetId() != "" && len(s.Paths) > 0 {
			clean = append(clean, s)
		}
	}
	out := &protocol.Projects{Projects: clean}
	data, _ := proto.Marshal(out)
	p.mu.Lock()
	p.resolveLocked(clean)
	path := p.path
	p.mu.Unlock()
	if path != "" {
		_ = writeAtomic(path, data)
	}
	return out
}

// get is the projects kept.
func (p *projectStore) get() []*protocol.ProjectScope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.list
}

// projectOf is the project holding a session working in dir, of
// repository repo, both with their links followed, and its folders as
// they are compared; nil for none. The one with the deepest folder that
// holds it wins.
func (p *projectStore) projectOf(dir, repo string) (*protocol.ProjectScope, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	best, depth := -1, -1
	for i := range p.list {
		for _, loc := range p.real[i] {
			if pathscope.Covers(loc, dir, repo) && len(loc) > depth {
				best, depth = i, len(loc)
			}
		}
	}
	if best < 0 {
		return nil, nil
	}
	return p.list[best], p.real[best]
}

// projectFor is the project holding a session working in dir, of the
// repository repo, or of dir's when repo is "", and that project's folders
// with their links followed; nil for none.
func (d *Daemon) projectFor(dir, repo string) (*protocol.ProjectScope, []string) {
	if repo == "" {
		repo = d.heads.of(dir).repo
	}
	if repo != "" {
		repo = cleanPath(repo)
	}
	return d.projects.projectOf(cleanPath(dir), repo)
}

// projectScope is the project a session is in, as its agent's MCP tools
// and the Agents panel know it: its sharedScope, else its folderScope.
func (d *Daemon) projectScope(info *protocol.SessionInfo) string {
	return cmp.Or(d.sharedScope(info), folderScope(info))
}

// sharedScope is the scope of a session in a project of several folders,
// which spans nodes: "project:" and the ID of the project holding its
// folder; "" for none.
func (d *Daemon) sharedScope(info *protocol.SessionInfo) string {
	if len(d.projects.get()) == 0 {
		return "" // nothing to follow the folders' links for
	}
	if s, _ := d.projectFor(info.GetProject(), cmp.Or(info.GetWorktree().GetRepo(), info.GetRepo())); s != nil {
		return projectScopePrefix + s.GetId()
	}
	return ""
}

// folderScope is the project of a session no project holds: its folder,
// or the repository of its worktree.
func folderScope(info *protocol.SessionInfo) string {
	return cleanPath(cmp.Or(info.GetWorktree().GetRepo(), info.GetProject()))
}

// cleanPath is path absolute, its links followed, and clean.
func cleanPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

// projectDirs are the folders an agent started in dir is given besides its
// own, as its project gives them: the project's others, those not inside
// dir and that dir is not inside.
func (d *Daemon) projectDirs(dir string) []string {
	s, real := d.projectFor(dir, "")
	if s == nil || !s.GetGiveDirs() {
		return nil
	}
	here := cleanPath(dir)
	var out []string
	for i, loc := range real {
		if pathscope.Within(here, loc) || pathscope.Within(loc, here) {
			continue
		}
		if given := s.GetPaths()[i]; dirExists(given) {
			out = append(out, given) // as the user gave it
		}
	}
	return out
}

// dirExists reports whether path is a folder there is.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// withProjectDirs is args with the arguments that give the agent dirs, as
// far as the agent takes folders besides its own.
func withProjectDirs(agentName string, args, dirs []string) []string {
	if len(dirs) == 0 {
		return args
	}
	entry, ok := agents.Lookup(agentName)
	if !ok {
		return args
	}
	takes, ok := entry.Driver.(agent.DirTaker)
	if !ok {
		return args
	}
	return append(slices.Clone(args), takes.DirArgs(dirs)...)
}

func (d *Daemon) handleSetProjects(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeProjects, func(req *protocol.SetProjects) (*protocol.Projects, error) {
		out := d.projects.set(req.GetProjects())
		// The Agents panels and list_agents follow at once.
		d.changes.fire(false, true)
		return out, nil
	})
}
