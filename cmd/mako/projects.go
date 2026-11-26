package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/workspace"
)

// projectsDirs handles `mako projects dirs [add <path>|remove <path>|default <path>]`
// directly on config.json (no daemon required).
func projectsDirs(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--json" || args[0] == "-json")) {
		dirs := config.NormalizeProjectDirs(cfg.ProjectDirs)
		if len(args) == 1 {
			if dirs == nil {
				dirs = []string{}
			}
			out, _ := json.MarshalIndent(dirs, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		if len(dirs) == 0 {
			fmt.Println("No default project storage directories configured.")
			fmt.Println("Add one with: mako projects dirs add ~/Projects")
			return nil
		}
		for i, d := range dirs {
			if i == 0 {
				fmt.Printf("%s (default)\n", d)
			} else {
				fmt.Println(d)
			}
		}
		return nil
	}

	op := args[0]
	rest := args[1:]
	switch op {
	case "add":
		if len(rest) < 1 {
			return errors.New("projects dirs add takes one or more folder paths")
		}
		next := append(slices.Clone(cfg.ProjectDirs), rest...)
		cfg.ProjectDirs = config.NormalizeProjectDirs(next)
		if err := config.Save(cfg); err != nil {
			return err
		}
		for i, d := range cfg.ProjectDirs {
			if i == 0 {
				fmt.Printf("%s (default)\n", d)
			} else {
				fmt.Println(d)
			}
		}
		return nil

	case "remove", "rm":
		if len(rest) != 1 {
			return errors.New("projects dirs remove takes a folder path")
		}
		target := filepath.Clean(mcp.ExpandHome(strings.TrimSpace(rest[0])))
		var kept []string
		removed := false
		for _, d := range config.NormalizeProjectDirs(cfg.ProjectDirs) {
			if filepath.Clean(mcp.ExpandHome(d)) == target || d == strings.TrimSpace(rest[0]) {
				removed = true
				continue
			}
			kept = append(kept, d)
		}
		if !removed {
			return fmt.Errorf("project storage directory %q not found", rest[0])
		}
		cfg.ProjectDirs = kept
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Removed %s\n", rest[0])
		return nil

	case "default":
		if len(rest) != 1 {
			return errors.New("projects dirs default takes a folder path")
		}
		want := strings.TrimSpace(rest[0])
		next := append([]string{want}, cfg.ProjectDirs...)
		cfg.ProjectDirs = config.NormalizeProjectDirs(next)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Default project storage directory set to %s\n", cfg.ProjectDirs[0])
		return nil

	default:
		return fmt.Errorf("unknown projects dirs command %q (expected add, remove, or default)", op)
	}
}

func projectsCmd(ctx context.Context, c *client.Client, cfg *config.Config, nodeID string, args []string) error {
	if len(args) == 0 {
		return listProjectsCmd(ctx, c, nil)
	}
	switch args[0] {
	case "list", "ls":
		return listProjectsCmd(ctx, c, args[1:])
	case "add":
		return addProjectCmd(ctx, c, cfg, nodeID, args[1:])
	case "dirs":
		return projectsDirs(args[1:])
	default:
		return fmt.Errorf("unknown projects command %q (expected list, add, or dirs)", args[0])
	}
}

func listProjectsCmd(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("projects list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	if _, err := parseInterleaved(fs, args); err != nil {
		return err
	}
	ws, err := c.Workspace(ctx)
	if err != nil {
		return err
	}
	st := ws.GetState()
	selfID := c.Node().GetId()

	type item struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Paths  []string `json:"paths"`
		Remote string   `json:"remote,omitempty"`
		Owner  string   `json:"owner,omitempty"`
	}
	projs := workspace.Projects(st)
	items := make([]item, 0, len(projs))
	for _, p := range projs {
		var localPaths, allPaths []string
		var remote, owner string
		for _, r := range workspace.Roots(p) {
			if remote == "" {
				remote = r.GetRemote().GetValue()
			}
			if owner == "" {
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
				if (n == selfID || n == "") && !slices.Contains(localPaths, path) {
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
		items = append(items, item{
			ID:     p.GetId(),
			Name:   name,
			Paths:  paths,
			Remote: remote,
			Owner:  owner,
		})
	}
	if *asJSON {
		out, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	if len(items) == 0 {
		fmt.Println("No projects registered.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPATHS\tREMOTE")
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.ID, it.Name, strings.Join(it.Paths, ", "), it.Remote)
	}
	return w.Flush()
}

func addProjectCmd(ctx context.Context, c *client.Client, cfg *config.Config, nodeID string, args []string) error {
	fs := flag.NewFlagSet("projects add", flag.ContinueOnError)
	name := fs.String("name", "", "optional project display name")
	baseDir := fs.String("dir", "", "base directory to clone into (for git URLs; defaults to first configured project_dirs)")
	folder := fs.String("folder", "", "optional subfolder name when cloning a git URL")
	space := fs.String("space", "", "optional target space ID or name")
	asJSON := fs.Bool("json", false, "output JSON")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("projects add takes a local folder path or git URL")
	}
	target := strings.TrimSpace(pos[0])
	if target == "" {
		return errors.New("projects add takes a local folder path or git URL")
	}

	targetNodeID := c.Node().GetId()
	remoteNode := ""
	if nodeID != "" {
		resolved, err := findNode(ctx, c, nodeID)
		if err != nil {
			return err
		}
		targetNodeID = resolved
		if resolved != c.Node().GetId() {
			remoteNode = resolved
		}
	}

	var absPath, owner, remote string
	var cloned bool

	if gitstat.IsGitURL(target) {
		repoName, parsedOwner, parsedRemote, err := gitstat.ParseGitURL(target)
		if err != nil {
			return err
		}
		folderName := strings.TrimSpace(*folder)
		if folderName == "" {
			folderName = repoName
		} else if folderName == "." || folderName == ".." || strings.ContainsAny(folderName, `/\`) {
			return fmt.Errorf("invalid folder name %q", folderName)
		}

		parent := strings.TrimSpace(*baseDir)
		if parent == "" {
			if remoteNode != "" {
				return errors.New("when cloning on a remote node, pass -dir <directory>")
			}
			dirs := config.NormalizeProjectDirs(cfg.ProjectDirs)
			if len(dirs) == 0 {
				return errors.New("no default project storage directory configured; pass -dir <directory> or run `mako projects dirs add <path>`")
			}
			parent = dirs[0]
		}
		if remoteNode == "" {
			parent = mcp.ExpandHome(parent)
			if abs, err := filepath.Abs(parent); err == nil {
				parent = abs
			}
			parent = filepath.Clean(parent)
		}
		dest := filepath.Join(parent, folderName)
		if remoteNode != "" {
			dest = strings.TrimRight(parent, "/") + "/" + folderName
		}

		cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		res, err := c.GitRun(cloneCtx, &protocol.GitRun{
			NodeId:  remoteNode,
			Root:    dest,
			Op:      protocol.GitOpClone,
			Message: target,
		})
		if err != nil {
			return err
		}
		absPath = cmp.Or(strings.TrimSpace(res.GetOutput()), dest)
		owner, remote = parsedOwner, parsedRemote
		if remoteNode == "" {
			_, _, hOwner, hRemote := gitstat.Head(absPath)
			owner = cmp.Or(hOwner, owner)
			remote = cmp.Or(hRemote, remote)
		}
		cloned = true
	} else {
		p := target
		if remoteNode == "" {
			p = mcp.ExpandHome(target)
			abs, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			fi, err := os.Stat(abs)
			if err != nil {
				return err
			}
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", abs)
			}
			if real, err := filepath.EvalSymlinks(abs); err == nil {
				abs = real
			}
			absPath = filepath.Clean(abs)
			_, _, owner, remote = gitstat.Head(absPath)
		} else {
			absPath = p
		}
	}

	ws, err := c.Workspace(ctx)
	if err != nil {
		return err
	}
	st := ws.GetState()
	locKey := workspace.LocationKey(targetNodeID, absPath)
	mark := &protocol.HLC{WallTime: 1}

	var targetSpaceID string
	if spRef := strings.TrimSpace(*space); spRef != "" {
		for _, sp := range workspace.Spaces(st) {
			if sp.GetId() == spRef || strings.EqualFold(sp.GetName().GetValue(), spRef) {
				targetSpaceID = sp.GetId()
				break
			}
		}
		if targetSpaceID == "" {
			return fmt.Errorf("unknown space %q", spRef)
		}
	}

	delta := &protocol.WorkspaceState{}
	var projectID, displayName string
	alreadyExisted := false

	for _, p := range workspace.Projects(st) {
		for _, r := range workspace.Roots(p) {
			for _, key := range workspace.Members(r.GetLocations()) {
				n, path, ok := workspace.SplitLocation(key)
				if ok && (n == targetNodeID || (n == "" && remoteNode == "")) && filepath.Clean(path) == absPath {
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

	customName := strings.TrimSpace(*name)
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
		if _, err := c.SetWorkspace(ctx, &protocol.Workspace{State: delta}); err != nil {
			return err
		}
	}

	if *asJSON {
		out, _ := json.MarshalIndent(struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Path           string `json:"path"`
			Remote         string `json:"remote,omitempty"`
			Owner          string `json:"owner,omitempty"`
			SpaceID        string `json:"space_id,omitempty"`
			Cloned         bool   `json:"cloned,omitempty"`
			AlreadyExisted bool   `json:"already_existed,omitempty"`
		}{
			ID:             projectID,
			Name:           displayName,
			Path:           absPath,
			Remote:         remote,
			Owner:          owner,
			SpaceID:        targetSpaceID,
			Cloned:         cloned,
			AlreadyExisted: alreadyExisted,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	action := "Added"
	if cloned {
		action = "Cloned and added"
	} else if alreadyExisted {
		action = "Already registered"
	}
	fmt.Printf("%s project %s (%s) at %s\n", action, displayName, projectID, absPath)
	return nil
}
