package gui

import (
	"cmp"
	"context"
	"errors"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
)

// cloneAsk is the state of the "Clone from Git URL" dialog.
type cloneAsk struct {
	Open    *ggui.StateValue[bool]
	Node    *ggui.StateValue[string]
	URL     *ggui.StateValue[string]
	BaseDir *ggui.StateValue[string]
	Folder  *ggui.StateValue[string]
	Cloning *ggui.StateValue[bool]
	Err     *ggui.StateValue[string]

	targetSpace string
	gen         uint64
	cancel      context.CancelFunc
}

func newCloneAsk() *cloneAsk {
	return &cloneAsk{
		Open:    ggui.State(false),
		Node:    ggui.State(""),
		URL:     ggui.State(""),
		BaseDir: ggui.State(""),
		Folder:  ggui.State(""),
		Cloning: ggui.State(false),
		Err:     ggui.State(""),
	}
}

func (c *cloneAsk) stop() {
	c.gen++
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.Cloning.Set(false)
}

// openCloneProject opens the dialog to clone a repository by Git URL.
func (m *model) openCloneProject() {
	m.openCloneProjectIn("")
}

func (m *model) openCloneProjectIn(spaceID string) {
	cl := m.Proj.Clone
	cl.stop()
	cl.targetSpace = spaceID
	node := ggui.Peek(m.Conn).Self.GetId()
	if filtered := ggui.Peek(m.Prefs).ProjectMachine; filtered != "" {
		if n, ok := nodeIn(ggui.Peek(m.Nodes), filtered); ok && reachable(n) {
			node = filtered
		}
	}
	cl.Node.Set(node)
	cl.URL.Set("")
	cl.Folder.Set("")
	cl.Err.Set("")
	if m.onThisMachine(node) {
		if dirs := ggui.Peek(m.ProjectDirs); len(dirs) > 0 {
			cl.BaseDir.Set(dirs[0])
		} else {
			cl.BaseDir.Set(ggui.Peek(m.Prefs).DefaultProject)
		}
	} else {
		cl.BaseDir.Set("")
	}
	cl.Open.Set(true)
}

func cloneDestinationPath(local bool, baseDir, folderOverride, rawURL string) (dest, folderName string, err error) {
	repoName, _, _, parseErr := gitstat.ParseGitURL(rawURL)
	if parseErr != nil {
		return "", "", parseErr
	}
	folderName = strings.TrimSpace(folderOverride)
	if folderName == "" {
		folderName = repoName
	} else if folderName == "." || folderName == ".." || strings.ContainsAny(folderName, `/\`) {
		return "", "", errors.New(T("Invalid folder name"))
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return "", folderName, errors.New(T("Choose a destination folder"))
	}
	if local {
		expanded := mcp.ExpandHome(base)
		if !filepath.IsAbs(expanded) {
			if abs, absErr := filepath.Abs(expanded); absErr == nil {
				expanded = abs
			}
		}
		return filepath.Join(filepath.Clean(expanded), folderName), folderName, nil
	}
	return strings.TrimRight(base, "/") + "/" + folderName, folderName, nil
}

func (m *model) submitCloneProject() {
	cl := m.Proj.Clone
	if ggui.Peek(cl.Cloning) {
		return
	}
	node := ggui.Peek(cl.Node)
	rawURL := strings.TrimSpace(ggui.Peek(cl.URL))
	baseDir := strings.TrimSpace(ggui.Peek(cl.BaseDir))
	folderOverride := strings.TrimSpace(ggui.Peek(cl.Folder))
	local := m.onThisMachine(node)
	remoteID := m.remoteID(node)
	targetSpace := cl.targetSpace

	_, parsedOwner, parsedRemote, parseErr := gitstat.ParseGitURL(rawURL)
	if parseErr != nil {
		cl.Err.Set(T("Enter a valid Git repository URL (https://, ssh://, or git@host:owner/repo.git)."))
		return
	}
	dest, folderName, destErr := cloneDestinationPath(local, baseDir, folderOverride, rawURL)
	if destErr != nil {
		cl.Err.Set(destErr.Error())
		return
	}

	cl.stop()
	cl.Err.Set("")
	cl.Cloning.Set(true)
	gen := cl.gen
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cl.cancel = cancel

	finish := func(clonedPath, owner, remote string, err error) {
		if cl.gen != gen {
			return
		}
		cl.Cloning.Set(false)
		cl.cancel = nil
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			cl.Err.Set(nodeErr(err))
			return
		}
		cl.Open.Set(false)
		repo := discoveredRepo{
			Path:   clonedPath,
			Name:   cmp.Or(projectName(clonedPath), folderName),
			Owner:  cmp.Or(owner, parsedOwner),
			Remote: cmp.Or(remote, parsedRemote),
		}
		m.importGitReposIn(node, []discoveredRepo{repo}, targetSpace)
		// Focus the added project in the sidebar.
		defs := m.projectsNow()
		kp := keptProject{fileRoot: fileRoot{Node: node, Path: clonedPath}, Owner: repo.Owner, Remote: repo.Remote}
		if pl := placeKept(defs, kp); pl.At >= 0 {
			m.setScope(scopeKey{Section: defs[pl.At].ID})
		} else {
			key := sectionKey(defs, implicitKey(node, clonedPath, repo.Remote))
			m.setScope(scopeKey{Section: key})
		}
	}

	post := m.post
	go func() {
		defer cancel()
		var clonedPath, owner, remote string
		var err error
		if !local {
			if m.link == nil {
				err = errors.New("daemon unavailable")
			} else {
				c, dialErr := m.link.client(ctx)
				if dialErr != nil {
					err = dialErr
				} else {
					res, runErr := c.GitRun(ctx, &protocol.GitRun{
						NodeId:  remoteID,
						Root:    dest,
						Op:      protocol.GitOpClone,
						Message: rawURL,
					})
					if runErr != nil {
						err = runErr
					} else {
						clonedPath = cmp.Or(strings.TrimSpace(res.GetOutput()), dest)
						owner, remote = parsedOwner, parsedRemote
					}
				}
			}
		} else {
			res, cloneErr := gitstat.CloneWithInfo(ctx, rawURL, dest)
			if cloneErr != nil {
				err = cloneErr
			} else {
				clonedPath, owner, remote = res.Path, res.Owner, res.Remote
			}
		}
		if post != nil {
			post(func() { finish(clonedPath, owner, remote, err) })
		} else {
			finish(clonedPath, owner, remote, err)
		}
	}()
}

// Dialog UI -------------------------------------------------------------------

func cloneProjectDialog(m *model) ggui.Widget {
	cl := m.Proj.Clone
	was := false
	ggui.Watch(cl.Open, func(on bool) {
		if was && !on {
			cl.stop()
		}
		was = on
	})
	return ggui.If(cl.Open, func() ggui.Widget {
		return keyedDialog(cl.Open, cloneProjectForm(m)).Title(T("Clone from Git URL")).Width(540)
	})
}

func cloneProjectForm(m *model) ggui.Widget {
	cl := m.Proj.Clone
	nodeIDs := reachableNodeIDs(m)
	local := ggui.Derived(func() bool { return cl.Node.Get() == m.Conn.Get().Self.GetId() })
	multiNode := ggui.Map(nodeIDs, func(ids []string) bool { return len(ids) > 1 })

	nodeBind := ggui.Bind(cl.Node.Get, func(id string) {
		if id == ggui.Peek(cl.Node) {
			return
		}
		cl.Node.Set(id)
		cl.Err.Set("")
		if m.onThisMachine(id) {
			if dirs := ggui.Peek(m.ProjectDirs); len(dirs) > 0 {
				cl.BaseDir.Set(dirs[0])
			} else {
				cl.BaseDir.Set(ggui.Peek(m.Prefs).DefaultProject)
			}
		} else {
			cl.BaseDir.Set("")
		}
	})

	browseLocal := ui.Button(T("Browse…"), func() {
		browseInto(func(dir string) {
			cl.BaseDir.Set(dir)
			cl.Err.Set("")
		}, func(d runtime.Dialogs) (string, error) {
			return d.PickFolder(runtime.FileDialog{Title: T("Save location"), Directory: ggui.Peek(cl.BaseDir)})
		})
	}).Outline()

	browseRemote := ui.Button(T("Browse…"), func() {
		m.pickFolderFor(cl.Open, ggui.Peek(cl.Node), ggui.Peek(cl.BaseDir), cl.Err.Set, func(dir string) {
			if dir != "" {
				cl.BaseDir.Set(dir)
				cl.Err.Set("")
			}
		})
	}).Outline()

	savedLocations := ggui.Reactive(func() ggui.Widget {
		if !local.Get() {
			return widget.Box()
		}
		dirs := m.ProjectDirs.Get()
		cur := strings.TrimSpace(cl.BaseDir.Get())
		if len(dirs) == 0 {
			return widget.Row(
				muted(widget.Text(T("Tip: configure default project locations in Settings ▸ General.")).Size(11.5)),
				widget.Spacer(),
				ui.Button(T("Manage…"), func() {
					cl.Open.Set(false)
					m.openSettings("general")
				}).Ghost().Pad(2, 6).Radius(5),
			).Gap(8).Align(widget.AlignCenter)
		}
		chips := make([]ggui.Widget, 0, len(dirs)+1)
		for _, d := range dirs {
			dir := d
			active := cur == dir || (cur != "" && filepath.Clean(mcp.ExpandHome(cur)) == filepath.Clean(mcp.ExpandHome(dir)))
			borderCol := pick[color.Color](active, skin.BorderAccent, skin.Border)
			fgCol := pick[color.Color](active, skin.AccentBright, skin.Muted)
			chip := ui.ButtonOf(
				widget.Row(
					icon("folder").Size(12).Color(fgCol),
					widget.Text(prettyPath(dir)).Size(11.5).Color(fgCol).NoWrap(),
				).Gap(5).Align(widget.AlignCenter),
				func() {
					cl.BaseDir.Set(dir)
					cl.Err.Set("")
				},
			).Ghost().Pad(4, 8).Radius(6).Name(prettyPath(dir))
			chips = append(chips, widget.Box(chip).Border(1, borderCol).Radius(6))
		}
		chips = append(chips, ui.Button(T("Manage…"), func() {
			cl.Open.Set(false)
			m.openSettings("general")
		}).Ghost().Pad(4, 8).Radius(6))
		return widget.Wrap(chips...).Gap(6)
	})

	baseRow := widget.Row(
		widget.Expanded(ui.TextField(cl.BaseDir).Placeholder("~/Projects").Name(T("Save location")).
			OnSubmit(func(string) { m.submitCloneProject() })),
		ggui.If(local, func() ggui.Widget { return browseLocal }).Else(func() ggui.Widget { return browseRemote }),
	).Gap(8).Align(widget.AlignCenter)

	folderField := ggui.Reactive(func() ggui.Widget {
		ph := T("Derived from repository URL")
		if name, _, _, err := gitstat.ParseGitURL(cl.URL.Get()); err == nil && name != "" {
			ph = name
		}
		return ui.TextField(cl.Folder).Placeholder(ph).Name(T("Folder name")).
			OnSubmit(func(string) { m.submitCloneProject() })
	})

	preview := ggui.Reactive(func() ggui.Widget {
		if cl.Cloning.Get() {
			return muted(widget.Text(T("Cloning repository…")).Size(12))
		}
		if errMsg := cl.Err.Get(); errMsg != "" {
			return widget.Text(errMsg).Size(12).Color(skin.DotDown)
		}
		dest, _, err := cloneDestinationPath(local.Get(), cl.BaseDir.Get(), cl.Folder.Get(), cl.URL.Get())
		if err != nil {
			return widget.Box()
		}
		return widget.Row(
			icon("folder-plus").Size(13).Color(skin.ExtraMuted),
			widget.Expanded(widget.Text(Tf("Clone into %s", prettyPath(dest))).Size(12).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap().Ellipsis()),
		).Gap(6).Align(widget.AlignCenter)
	})

	footer := ggui.Reactive(func() ggui.Widget {
		cloning := cl.Cloning.Get()
		canSubmit := !cloning && strings.TrimSpace(cl.URL.Get()) != "" && strings.TrimSpace(cl.BaseDir.Get()) != ""
		label := pick(cloning, T("Cloning…"), T("Clone & Add"))
		return widget.Row(
			ui.Button(T("Cancel"), func() { cl.Open.Set(false) }).Outline(),
			ui.Button(label, m.submitCloneProject).Disabled(!canSubmit),
		).Gap(8).Justify(widget.JustifyEnd)
	})

	return widget.Column(
		muted(widget.Text(T("Clone a Git repository into a local folder and add it to the sidebar.")).Size(12)),
		ggui.If(multiNode, func() ggui.Widget {
			return ui.Field(T("Node"), ui.Select(nodeBind).BindOptions(nodeIDs).Format(func(id string) string {
				return nodeName(ggui.Peek(m.Nodes), id)
			}).Name(T("Node")))
		}),
		ui.Field(T("Git URL"), ui.TextField(cl.URL).Placeholder("https://github.com/owner/repo.git").Name(T("Git URL")).
			OnSubmit(func(string) { m.submitCloneProject() })),
		ui.Field(T("Save location"), widget.Column(baseRow, savedLocations).Gap(6).Stretch()),
		ui.Field(T("Folder name (optional)"), folderField),
		preview,
		footer,
	).Gap(12).Stretch()
}

// projectDirQuickPicks renders clickable chips for configured ProjectDirs
// inside project dialogs (e.g. Import git projects).
func projectDirQuickPicks(m *model, local ggui.Readable[bool], current ggui.Readable[string], onPick func(string)) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		if !local.Get() {
			return widget.Box()
		}
		dirs := m.ProjectDirs.Get()
		if len(dirs) == 0 {
			return widget.Box()
		}
		cur := strings.TrimSpace(current.Get())
		chips := make([]ggui.Widget, 0, len(dirs))
		for _, d := range dirs {
			dir := d
			active := cur == dir || (cur != "" && filepath.Clean(mcp.ExpandHome(cur)) == filepath.Clean(mcp.ExpandHome(dir)))
			borderCol := pick[color.Color](active, skin.BorderAccent, skin.Border)
			fgCol := pick[color.Color](active, skin.AccentBright, skin.Muted)
			chip := ui.ButtonOf(
				widget.Row(
					icon("folder").Size(12).Color(fgCol),
					widget.Text(prettyPath(dir)).Size(11.5).Color(fgCol).NoWrap(),
				).Gap(5).Align(widget.AlignCenter),
				func() { onPick(dir) },
			).Ghost().Pad(4, 8).Radius(6).Name(prettyPath(dir))
			chips = append(chips, widget.Box(chip).Border(1, borderCol).Radius(6))
		}
		return widget.Wrap(chips...).Gap(6)
	})
}
