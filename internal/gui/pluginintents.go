package gui

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// handlePluginDone applies gesture-gated window effects returned by the daemon
// in answer to a PluginAction or BlockEvent: external web links, toasts,
// clipboard text, and window intents (diff, file, forge PR/runs, create PR).
func (m *model) handlePluginDone(done *protocol.PluginDone) {
	if done == nil {
		return
	}
	openPluginLink(done.GetOpenUrl())
	m.showPluginToast(done.GetToast())
	if text := done.GetCopyText(); text != "" {
		ggui.CurrentClipboard().Write(text)
	}
	for _, o := range done.GetOpen() {
		m.dispatchPluginOpen(o)
	}
}

// pluginIntentNode normalizes a plugin intent's node field ("", "local", or a
// node ID) into the GUI's fileRoot.Node representation.
func (m *model) pluginIntentNode(node string) string {
	if node == "" || node == protocol.LocalScopeNode {
		return ggui.Peek(m.Conn).Self.GetId()
	}
	return node
}

// dispatchPluginOpen runs one PluginOpen window intent against the window's
// native tabs and dialogs.
func (m *model) dispatchPluginOpen(o *protocol.PluginOpen) {
	if o == nil {
		return
	}
	node := m.pluginIntentNode(o.GetNode())
	folder := o.GetFolder()
	repo := o.GetRepo()

	switch o.GetKind() {
	case "diff":
		if repo == "" {
			repo = folder
		}
		if folder == "" {
			folder = repo
		}
		if o.GetPath() == "" || repo == "" {
			return
		}
		f := &protocol.GitFile{
			Path:    o.GetPath(),
			From:    o.GetFrom(),
			Status:  o.GetStatus(),
			Added:   o.GetAdded(),
			Removed: o.GetRemoved(),
			Binary:  o.GetBinary(),
		}
		m.openDiffTab(fileRoot{Node: node, Path: folder}, repo, o.GetCommit(), o.GetBase(), f, o.GetPreview())

	case "file":
		p := o.GetPath()
		if p == "" {
			return
		}
		if folder != "" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") && !isWindowsAbsPath(p) {
			p = joinPath(folder, p)
		}
		name := path.Base(strings.ReplaceAll(p, "\\", "/"))
		it := fileItem{
			Key:  dirKey(node, p),
			Node: node,
			Path: p,
			Name: name,
		}
		if folder != "" {
			m.ensureDoc(it, fileRoot{Node: node, Path: folder})
		}
		switch {
		case o.GetLine() > 0:
			m.openFileAt(it, int(o.GetLine()))
		case o.GetPreview():
			m.previewFile(it)
		default:
			m.openFile(it)
		}

	case "pr":
		if folder == "" {
			folder = repo
		}
		if folder == "" {
			folder = ggui.Untrack(m.repoFolder).Path
		}
		if folder == "" {
			return
		}
		m.openForge(fileRoot{Node: node, Path: folder}, protocol.ForgePR, o.GetNumber())

	case "runs", "run":
		if folder == "" {
			folder = repo
		}
		if folder == "" {
			folder = ggui.Untrack(m.repoFolder).Path
		}
		if folder == "" {
			return
		}
		m.openForge(fileRoot{Node: node, Path: folder}, protocol.ForgeRuns, o.GetNumber())

	case "create_pr":
		if folder == "" {
			folder = repo
		}
		if folder != "" {
			target := fileRoot{Node: node, Path: folder}
			if ggui.Untrack(m.repoFolder) != target {
				m.RepoPick.Set(repoPick{For: ggui.Peek(m.Roots).own(), At: target})
			}
		}
		m.askPR()
	}
}

func isWindowsAbsPath(p string) bool {
	return len(p) >= 3 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
