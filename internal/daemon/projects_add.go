package daemon

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/workspace"
)

// agentProjectSummary describes a workspace project returned by `mako-project list`.
type agentProjectSummary struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Paths  []string `json:"paths"`
	Remote string   `json:"remote,omitempty"`
	Owner  string   `json:"owner,omitempty"`
	Spaces []string `json:"spaces,omitempty"`
}

// agentProjectAddResult describes the result of `mako-project add`.
type agentProjectAddResult struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Remote         string `json:"remote,omitempty"`
	Owner          string `json:"owner,omitempty"`
	SpaceID        string `json:"space_id,omitempty"`
	Cloned         bool   `json:"cloned,omitempty"`
	AlreadyExisted bool   `json:"already_existed,omitempty"`
}

// listWorkspaceProjects returns visible projects from the workspace CRDT (falling
// back to the local project store when the workspace holds none yet).
func (d *Daemon) listWorkspaceProjects() []agentProjectSummary {
	nodeID := d.config().NodeID
	st := d.workspace.get(false).GetState()
	projs := workspace.Projects(st)
	if len(projs) == 0 {
		scopes := d.projects.get()
		out := make([]agentProjectSummary, 0, len(scopes))
		for _, s := range scopes {
			name := s.GetName()
			if name == "" && len(s.GetPaths()) > 0 {
				name = filepath.Base(s.GetPaths()[0])
			}
			out = append(out, agentProjectSummary{
				ID:    s.GetId(),
				Name:  name,
				Paths: slices.Clone(s.GetPaths()),
			})
		}
		return out
	}

	spaceByProject := map[string][]string{}
	for _, sp := range workspace.Spaces(st) {
		label := cmp.Or(sp.GetName().GetValue(), sp.GetId())
		for _, pid := range workspace.SpaceProjects(st, sp) {
			if !slices.Contains(spaceByProject[pid], label) {
				spaceByProject[pid] = append(spaceByProject[pid], label)
			}
		}
	}

	out := make([]agentProjectSummary, 0, len(projs))
	for _, p := range projs {
		var localPaths, allPaths []string
		var remote, owner string
		for _, r := range workspace.Roots(p) {
			if remote == "" && r.GetRemote().GetValue() != "" {
				remote = r.GetRemote().GetValue()
			}
			if owner == "" && r.GetOwner().GetValue() != "" {
				owner = r.GetOwner().GetValue()
			}
			for _, key := range workspace.Members(r.GetLocations()) {
				n, path, ok := workspace.SplitLocation(key)
				if !ok || path == "" {
					continue
				}
				if !slices.Contains(allPaths, path) {
					allPaths = append(allPaths, path)
				}
				if (n == nodeID || n == "") && !slices.Contains(localPaths, path) {
					localPaths = append(localPaths, path)
				}
			}
		}
		paths := localPaths
		if len(paths) == 0 {
			paths = allPaths
		}
		name := p.GetName().GetValue()
		if name == "" && len(paths) > 0 {
			name = filepath.Base(paths[0])
		}
		if name == "" && remote != "" {
			name = filepath.Base(remote)
		}
		out = append(out, agentProjectSummary{
			ID:     p.GetId(),
			Name:   name,
			Paths:  paths,
			Remote: remote,
			Owner:  owner,
			Spaces: spaceByProject[p.GetId()],
		})
	}
	return out
}

// addProjectFromCLI clones (when target is a git URL) or validates (when target
// is a local folder) and registers the project into the daemon's workspace CRDT.
func (d *Daemon) addProjectFromCLI(ctx context.Context, callerCwd, target, name, baseDir, folder, spaceRef string) (agentProjectAddResult, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return agentProjectAddResult{}, errors.New("a local folder path or git URL is required")
	}

	var absPath, owner, remote string
	var cloned bool

	if gitstat.IsGitURL(target) {
		repoName, _, _, err := gitstat.ParseGitURL(target)
		if err != nil {
			return agentProjectAddResult{}, err
		}
		folderName := strings.TrimSpace(folder)
		if folderName == "" {
			folderName = repoName
		} else if folderName == "." || folderName == ".." || strings.ContainsAny(folderName, `/\`) {
			return agentProjectAddResult{}, fmt.Errorf("invalid folder name %q", folderName)
		}

		parent := strings.TrimSpace(baseDir)
		if parent == "" {
			dirs := d.projectStorageDirs()
			if len(dirs) == 0 {
				return agentProjectAddResult{}, errors.New("no default project storage directory is configured; pass --dir <directory> or add one in Settings ▸ General (or `mako projects dirs add <path>`)")
			}
			parent = dirs[0]
		}
		parent = mcp.ExpandHome(parent)
		if !filepath.IsAbs(parent) {
			if callerCwd != "" {
				parent = filepath.Join(callerCwd, parent)
			} else if abs, err := filepath.Abs(parent); err == nil {
				parent = abs
			}
		}
		parent = filepath.Clean(parent)
		dest := filepath.Join(parent, folderName)

		cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		res, err := gitstat.CloneWithInfo(cloneCtx, target, dest)
		if err != nil {
			return agentProjectAddResult{}, err
		}
		absPath, owner, remote = res.Path, res.Owner, res.Remote
		cloned = !res.AlreadyExisted
	} else {
		p := mcp.ExpandHome(target)
		if !filepath.IsAbs(p) {
			if callerCwd != "" {
				p = filepath.Join(callerCwd, p)
			} else if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		p = filepath.Clean(p)
		fi, err := os.Stat(p)
		if err != nil {
			return agentProjectAddResult{}, err
		}
		if !fi.IsDir() {
			return agentProjectAddResult{}, fmt.Errorf("%s is not a directory", p)
		}
		absPath = cleanPath(p)
		_, _, owner, remote = gitstat.Head(absPath)
	}

	res, err := d.registerWorkspaceProject(absPath, strings.TrimSpace(name), owner, remote, strings.TrimSpace(spaceRef))
	if err != nil {
		return agentProjectAddResult{}, err
	}
	res.Cloned = cloned
	return res, nil
}

// registerWorkspaceProject adds (nodeID, absPath) to the daemon's workspace CRDT
// and triggers workspaceChanged so GUI windows, peers, and plugins update live.
func (d *Daemon) registerWorkspaceProject(absPath, customName, owner, remote, spaceRef string) (agentProjectAddResult, error) {
	nodeID := d.config().NodeID
	cleanTarget := cleanPath(absPath)
	locKey := workspace.LocationKey(nodeID, absPath)
	mark := &protocol.HLC{WallTime: 1}

	st := d.workspace.get(false).GetState()

	// Resolve optional target space by ID or case-insensitive name.
	var targetSpaceID string
	if spaceRef != "" {
		for _, sp := range workspace.Spaces(st) {
			if sp.GetId() == spaceRef || strings.EqualFold(sp.GetName().GetValue(), spaceRef) {
				targetSpaceID = sp.GetId()
				break
			}
		}
		if targetSpaceID == "" {
			return agentProjectAddResult{}, fmt.Errorf("unknown space %q", spaceRef)
		}
	}

	delta := &protocol.WorkspaceState{}
	var projectID, displayName string
	var alreadyExisted bool

	// 1. Check if this exact location already belongs to a visible project.
	for _, p := range workspace.Projects(st) {
		for _, r := range workspace.Roots(p) {
			for _, key := range workspace.Members(r.GetLocations()) {
				n, path, ok := workspace.SplitLocation(key)
				if ok && (n == nodeID || n == "") && cleanPath(path) == cleanTarget {
					projectID = p.GetId()
					displayName = cmp.Or(p.GetName().GetValue(), filepath.Base(absPath))
					alreadyExisted = true
					break
				}
			}
			if alreadyExisted {
				break
			}
		}
		if alreadyExisted {
			break
		}
	}

	// 2. If not already listed by path, join a unique project root sharing the same git remote.
	if !alreadyExisted && remote != "" {
		var matchProj *protocol.ProjectEntry
		var matchRoot *protocol.ProjectRootEntry
		claims := 0
		for _, p := range workspace.Projects(st) {
			for _, r := range workspace.Roots(p) {
				if strings.EqualFold(r.GetRemote().GetValue(), remote) {
					claims++
					if !p.GetNoJoin().GetValue() {
						matchProj, matchRoot = p, r
					}
				}
			}
		}
		if claims == 1 && matchProj != nil && matchRoot != nil {
			projectID = matchProj.GetId()
			displayName = cmp.Or(matchProj.GetName().GetValue(), filepath.Base(absPath))
			rootDelta := &protocol.ProjectRootEntry{
				Id: matchRoot.GetId(),
				Locations: map[string]*protocol.ElementMeta{
					locKey: {AddedAt: mark},
				},
			}
			if matchRoot.GetOwner().GetValue() == "" && owner != "" {
				rootDelta.Owner = &protocol.StringReg{Value: owner}
			}
			delta.Projects = map[string]*protocol.ProjectEntry{
				projectID: {
					Id:    projectID,
					Roots: map[string]*protocol.ProjectRootEntry{matchRoot.GetId(): rootDelta},
				},
			}
		}
	}

	// 3. Otherwise create a brand new project entry.
	if !alreadyExisted && projectID == "" {
		if remote != "" {
			candidate := "repo:" + remote
			if _, taken := st.GetProjects()[candidate]; !taken {
				projectID = candidate
			}
		}
		if projectID == "" {
			var b [6]byte
			_, _ = rand.Read(b[:])
			projectID = "p" + hex.EncodeToString(b[:])
		}
		rootID := cmp.Or(remote, locKey)
		rootEntry := &protocol.ProjectRootEntry{
			Id:        rootID,
			CreatedAt: mark,
			Locations: map[string]*protocol.ElementMeta{
				locKey: {AddedAt: mark},
			},
		}
		if remote != "" {
			rootEntry.Remote = &protocol.StringReg{Value: remote}
		}
		if owner != "" {
			rootEntry.Owner = &protocol.StringReg{Value: owner}
		}
		projEntry := &protocol.ProjectEntry{
			Id:        projectID,
			CreatedAt: mark,
			Roots:     map[string]*protocol.ProjectRootEntry{rootID: rootEntry},
		}
		if customName != "" {
			projEntry.Name = &protocol.StringReg{Value: customName}
		}
		delta.Projects = map[string]*protocol.ProjectEntry{projectID: projEntry}
		displayName = cmp.Or(customName, filepath.Base(absPath))
	} else if alreadyExisted && customName != "" {
		delta.Projects = map[string]*protocol.ProjectEntry{
			projectID: {
				Id:   projectID,
				Name: &protocol.StringReg{Value: customName},
			},
		}
		displayName = customName
	}

	if targetSpaceID != "" {
		delta.Spaces = map[string]*protocol.SpaceEntry{
			targetSpaceID: {
				Id: targetSpaceID,
				Projects: map[string]*protocol.ElementMeta{
					projectID: {AddedAt: mark},
				},
			},
		}
	}

	if len(delta.Projects) > 0 || len(delta.Spaces) > 0 {
		if d.workspace.edit(delta) {
			d.workspaceChanged()
		}
	}

	return agentProjectAddResult{
		ID:             projectID,
		Name:           displayName,
		Path:           absPath,
		Remote:         remote,
		Owner:          owner,
		SpaceID:        targetSpaceID,
		AlreadyExisted: alreadyExisted,
	}, nil
}
