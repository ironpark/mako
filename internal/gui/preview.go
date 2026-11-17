package gui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/fonts/gofont"
	"github.com/ironpark/ggui/widget"
	"github.com/ironpark/ggwd"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// writeGradient writes a diagonal gradient from a to b as a PNG, a
// stand-in background image for the preview.
func writeGradient(path string, a, b color.RGBA) error {
	const w, h = 640, 400
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			t := float64(x+y) / float64(w+h)
			mix := func(p, q uint8) uint8 { return uint8(float64(p)*(1-t) + float64(q)*t) }
			img.SetRGBA(x, y, color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff})
		}
	}
	return writePNG(path, img)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// Preview renders the window against a live daemon into PNGs in dir,
// without showing it, for reviewing layout: the workspace with a session
// attached, the dialogs, the settings pages, and the light theme. It is a
// development aid; the daemon should be running a session or two. The theme
// setting it switches through is put back when it is done.
func Preview(cfg *config.Config, addr, dir string, width, height int) error {
	agentsOnly := os.Getenv("MAKO_PREVIEW_AGENTS") != ""
	// MAKO_PREVIEW_RAIL renders a long conversation's turn rail, with no
	// daemon: at a prompt part way up, and with a tick hovered.
	railOnly := os.Getenv("MAKO_PREVIEW_RAIL") != ""
	// MAKO_PREVIEW_SIDEBAR renders the sidebar alone, with no daemon: its
	// projects over its agents, a project picked, and the agents grouped
	// by machine and in compact rows.
	sidebarOnly := os.Getenv("MAKO_PREVIEW_SIDEBAR") != ""
	// MAKO_PREVIEW_SETTINGS renders every settings page, with no daemon,
	// in a wide window and then a narrow one.
	settingsOnly := os.Getenv("MAKO_PREVIEW_SETTINGS") != ""
	if settingsOnly {
		railOnly = true // no daemon either
		width, height = 1440, 1400
	}
	if sidebarOnly {
		railOnly = true // no daemon either
		width, height = sidebarWidth+8, 900
	}
	var rail *turnRail
	if agentsOnly {
		width = 360
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		m      *model
		p      *ggui.Probe
		img    *ggfx.Image
		window *ggwd.Window
		steps  []previewStep
		since  time.Time
	)
	shot := func(name string) previewStep {
		return func() (bool, error) {
			img.Clear()
			p.Draw(img)
			return true, writePNG(filepath.Join(dir, name+".png"), img)
		}
	}
	do := func(fn func()) previewStep { return func() (bool, error) { fn(); return true, nil } }
	frames := func(n int) previewStep {
		left := n
		return func() (bool, error) {
			time.Sleep(20 * time.Millisecond)
			left--
			return left <= 0, nil
		}
	}
	until := func(what string, cond func() bool) previewStep {
		return func() (bool, error) {
			if since.IsZero() {
				since = time.Now()
			}
			if cond() {
				since = time.Time{}
				return true, nil
			}
			if time.Since(since) > 10*time.Second {
				return false, fmt.Errorf("preview: timed out waiting for %s", what)
			}
			return false, nil
		}
	}
	setTheme := func(theme string) func() {
		return func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = theme; return p }) }
	}

	return ggwd.Run(ggwd.HandlerFunc(func(ev ggwd.Event) error {
		switch ev.(type) {
		case ggwd.StartEvent:
			w, err := ggwd.NewWindow(&ggwd.WindowOptions{Title: "mako preview", Width: 64, Height: 64, Hidden: true})
			if err != nil {
				return err
			}
			window = w
			// A probe draws in Go Regular; Go Mono stands in for the
			// platform's monospaced font so the grid lines up as it will.
			ggui.SetDefaultMonoFont(gofont.Mono())
			m = newModel(cfg, addr)
			saved := ggui.Peek(m.Prefs)
			agentsRoot := fileRoot{Node: "preview", Path: "/workspace/mako"}
			if agentsOnly {
				i18n.Set(i18n.Resolve(saved.Language))
				m.DetailTab.Set(agentsTab)
				m.ProjectAgents.Set(projectAgentsView{Root: agentsRoot, Data: projectAgentsPreviewData(agentsRoot), Loaded: true, Updated: time.Now()})
			}
			window, session := filepath.Join(dir, "backdrop-window.png"), filepath.Join(dir, "backdrop-session.png")
			// MAKO_PREVIEW_BROWSER names a folder of this machine, which a
			// node's file browser shows in place of the window.
			browse := os.Getenv("MAKO_PREVIEW_BROWSER")
			// MAKO_PREVIEW_MARKDOWN names a Markdown file rendered in place
			// of the window, then with its first details opened.
			var mdSrc string
			if path := os.Getenv("MAKO_PREVIEW_MARKDOWN"); path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				mdSrc = string(data)
			}
			var b *browser
			p = ggui.ProbeBuilder(func() ggui.Widget {
				if sidebarOnly {
					sidebarPreview(m)
					return ggui.Provide(ggui.ReducedMotionKey, true, sidebar(m))
				}
				if settingsOnly {
					sidebarPreview(m) // projects and spaces for the pages to list
					return ggui.Provide(ggui.ReducedMotionKey, true, settingsPage(m))
				}
				if railOnly {
					var feed ggui.Widget
					feed, rail = railPreview(m)
					return feed
				}
				if agentsOnly {
					tabs := panelTabs(ggui.Const(2), func(int) {}, T("Files"), T("Git"), T("Agents"), "GitHub")
					return widget.Box(widget.Column(tabs, hline(), widget.Expanded(projectAgentsBody(m, agentsRoot))).Stretch()).Fill(skin.Surface0)
				}
				if mdSrc != "" {
					return ggui.Provide(ggui.ReducedMotionKey, true, markdownView(m, "", mdSrc))
				}
				if browse != "" {
					b = newBrowser(m, "", browse)
					return ggui.Provide(ggui.ReducedMotionKey, true, browserWindow(b))
				}
				return ggui.Provide(ggui.ReducedMotionKey, true, build(m))
			},
				ggui.Sz(float64(width), float64(height)))
			p.Setup(func() {
				setup(m)
				if !agentsOnly && !railOnly {
					m.start(ctx, p.Post)
				} else {
					m.post = func(func()) {} // the panel's refreshes, with no daemon, keep the fixed data
				}
			})
			img = ggfx.NewImage(width, height)
			steps = []previewStep{
				do(setTheme("dark")),
				until("the daemon", func() bool { return ggui.Peek(m.Conn).OK || ggui.Peek(m.Conn).Err != "" }),
				frames(2),
				shot("01-empty"),
				do(func() {
					if len(ggui.Peek(m.Sessions)) == 0 { // a fresh daemon, as the app's own is
						home, _ := os.UserHomeDir()
						m.launch("shell", "", home)
					}
				}),
				until("a session", func() bool { return len(ggui.Peek(m.Sessions)) > 0 }),
				do(func() {
					sessions := ggui.Peek(m.Sessions)
					for i := len(sessions) - 1; i >= 0; i-- {
						m.selectSession(sessions[i].GetId())
					}
				}),
				until("a terminal", func() bool { return slices.Contains(ggui.Peek(m.Attached), ggui.Peek(m.Selected)) }),
				frames(30), // the resize reaches the program and its redraw comes back
				shot("02-session"),
				// A fixed transcript makes chat layout review independent of
				// installed agents and personal conversation logs.
				do(func() {
					id := ggui.Peek(m.Selected)
					chat := m.chatState(id)
					chat.loading = true
					chat.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{
						Supported: true, Conversation: "preview",
						Messages: []*protocol.ChatMessage{
							{Role: "user", Text: "Add a chat view while keeping the terminal session running."},
							{Role: "assistant", Text: "The session now has **Chat** and **Terminal** views.\n\n- Switch views without restarting the agent.\n- Read the conversation and send a reply here.\n- Use the terminal for approvals and interactive commands."},
							{Role: "tool", Text: "go test ./internal/transcript\nok   github.com/ironpark/mako/internal/transcript"},
							{Role: "assistant", Text: "Ready to try. Use **⌘⇧J** to switch views."},
						},
					}})
					m.setChatMode(id, true)
				}),
				frames(5), shot("02-chat"),
				// The pointer over the agent's first reply shows its copy button.
				do(func() { p.Move(ggui.Pt(420, 190)) }), frames(3), do(func() { p.Move(ggui.Pt(430, 192)) }), frames(5), shot("02-chat-hover"),
				// An agent asking for an approval, and a long run of tools.
				do(func() {
					id := ggui.Peek(m.Selected)
					chat := m.chatState(id)
					v := ggui.Peek(chat.result)
					v.Chat = proto.CloneOf(v.Chat) // the result held is shared
					for _, cmd := range []string{"ls internal", "go vet ./...", "go test ./internal/gui", "git status --short"} {
						v.Chat.Messages = append(v.Chat.Messages, &protocol.ChatMessage{Role: "tool", Tool: "Bash", Kind: protocol.ToolShell, Detail: cmd, Text: `{"command": ` + strconv.Quote(cmd) + `}`})
					}
					// A subagent at work, and the call still waiting on it.
					v.Chat.Messages = append(v.Chat.Messages,
						&protocol.ChatMessage{Role: "tool", Id: "agent", Tool: "Agent", Kind: protocol.ToolTask, Detail: "Find the flaky tests", Pending: true},
						&protocol.ChatMessage{Role: "tool", Id: "build", Tool: "Bash", Kind: protocol.ToolShell, Detail: "go build ./...", Text: `{"command": "go build ./..."}`, Pending: true})
					v.Chat.Agents = []*protocol.SubAgent{{Id: "a1", CallId: "agent", Kind: "Explore", Description: "Find the flaky tests",
						Running: true, ToolCalls: 7, Tool: "Grep", ToolKind: protocol.ToolSearch, Detail: "probeUntil", Started: time.Now().Add(-3 * time.Minute).Unix()}}
					v.Chat.Usage = &protocol.Usage{Input: 41200, CachedInput: 380000, Output: 9800, Context: 96000, Window: 258400,
						Model: "claude-opus-5-5", Effort: "high", PermissionMode: "acceptEdits",
						Limits: []*protocol.RateLimit{
							{Minutes: protocol.FiveHours, Used: 23, Resets: time.Now().Add(2*time.Hour + 10*time.Minute).Unix()},
							{Minutes: protocol.Week, Used: 84, Resets: time.Now().Add(76 * time.Hour).Unix()},
						}}
					chat.result.Set(v)
					usage := m.usageState(id)
					usage.loading = true
					usage.usage.Set(v.Chat.GetUsage())
					m.Sessions.Set(previewStatus(ggui.Peek(m.Sessions), id))
					unread := map[string]bool{}
					for _, s := range ggui.Peek(m.Sessions) {
						unread[s.GetId()] = s.GetStatus() == protocol.AgentDone
					}
					m.Unread.Set(unread)
				}),
				frames(5), shot("02-chat-approval"),
				// An edit, as a diff, and a plan put to the user.
				do(func() { setPreviewApproval(m, previewApprovals()[1]) }), frames(5), shot("02-chat-approval-edit"),
				do(func() { setPreviewApproval(m, previewApprovals()[2]) }), frames(10), shot("02-chat-approval-plan"),
				// A dialog in the terminal no hook holds, as a folder's trust.
				do(func() {
					setPreviewApproval(m, nil)
					m.editSession(ggui.Peek(m.Selected), func(s *protocol.SessionInfo) {
						s.Dialog = &protocol.ScreenDialog{Id: "preview", Text: "Accessing workspace:\n/workspace/mako\nQuick safety check: Is this a project you created or one you trust?",
							Options: []string{"No, exit", "Yes, I trust this folder"}}
					})
				}), frames(5), shot("02-chat-dialog"),
				do(func() { m.editSession(ggui.Peek(m.Selected), func(s *protocol.SessionInfo) { s.Dialog = nil }) }),
				do(func() { setPreviewApproval(m, previewApprovals()[0]) }),
				// Tool calls as each tool's view reads them, their folds open.
				do(func() {
					id := ggui.Peek(m.Selected)
					chat := m.chatState(id)
					v := ggui.Peek(chat.result)
					v.Chat = proto.CloneOf(v.Chat)
					v.Chat.Messages = append(v.Chat.Messages[:4], previewTools()...)
					chat.result.Set(v)
					open := map[string]bool{}
					for _, msg := range v.Chat.Messages {
						open[chatMessageKey(msg)] = msg.GetRole() == "tool"
					}
					chat.tools.Set(open)
				}),
				frames(30), shot("02-chat-tools"),
				do(func() {
					id := ggui.Peek(m.Selected)
					m.setChatMode(id, false)
					m.Sessions.Set(previewStatus(ggui.Peek(m.Sessions), id))
				}),
				frames(5), shot("02-terminal-approval"),
				do(func() {
					id := ggui.Peek(m.Selected)
					delete(m.chats, id)
				}),
				// A second session, dragged to the right of the first.
				do(func() {
					s, _ := m.session(ggui.Peek(m.Selected))
					m.launch("shell", s.GetNodeId(), s.GetProject())
				}),
				until("a second tab", func() bool { return len(ggui.Peek(m.Tabs)) > 1 }),
				do(func() { m.splitTab(ggui.Peek(m.Selected), ggui.Peek(m.FocusGroup), dropRight) }),
				until("its terminal", func() bool { return slices.Contains(ggui.Peek(m.Attached), ggui.Peek(m.Selected)) }),
				frames(30), shot("03-split"),
				// The pane in hand marked, as the settings may ask.
				do(func() { m.Prefs.Update(func(p prefs) prefs { p.PaneGlow = true; return p }) }), frames(4), shot("03-split-outline"),
				do(func() { m.Prefs.Update(func(p prefs) prefs { p.PaneGlow = false; return p }) }),
				// The project's files, a folder open and a file previewed.
				do(func() {
					r := ggui.Untrack(m.filesRoot)
					dir := joinPath(r.Path, "internal")
					m.toggleDir(fileItem{Key: dirKey(r.Node, dir), Node: r.Node, Path: dir, Dir: true})
					readme := joinPath(r.Path, "README.md")
					m.openPreview(fileItem{Key: dirKey(r.Node, readme), Node: r.Node, Path: readme, Name: "README.md"})
				}),
				until("the preview", func() bool { return !ggui.Peek(m.files.Preview).Loading }),
				frames(5), shot("03-files"),
				// Files of a few kinds, each with its type's icon.
				do(func() {
					r := ggui.Untrack(m.filesRoot)
					for _, sub := range []string{"internal", "internal/gitstat"} {
						dir := joinPath(r.Path, sub)
						m.toggleDir(fileItem{Key: dirKey(r.Node, dir), Node: r.Node, Path: dir, Dir: true})
					}
					m.files.Preview.Set(filePreview{})
				}),
				frames(30), shot("03-file-icons"),
				// README.md opened in a tab of its own.
				do(func() {
					r := ggui.Untrack(m.filesRoot)
					readme := joinPath(r.Path, "README.md")
					m.openFile(fileItem{Key: dirKey(r.Node, readme), Node: r.Node, Path: readme, Name: "README.md"})
				}),
				until("the file", func() bool { d, ok := m.doc(ggui.Peek(m.Selected)); return ok && !d.Loading }),
				frames(60), shot("03-file-tab"),
				do(m.toggleSelectedPreview), frames(30), shot("03-markdown-preview"),
				do(m.toggleSelectedPreview), do(m.openPalette), frames(10), shot("03-palette"),
				do(func() { m.pickers.PaletteQuery.Set("side") }), frames(4), shot("03-palette-search"),
				do(func() { m.pickers.PaletteOpen.Set(false); m.ext.Open.Set(true) }),
				until("the extension store", func() bool { r := ggui.Peek(m.ext.Results); return r.Listings != nil || r.Err != "" }),
				frames(10), shot("03-extensions"),
				do(func() { m.ext.Open.Set(false); m.setAgentView(func(p *prefs) { p.AgentGroup = agentGroupMachine }) }), frames(10), shot("03-sidebar-machine"),
				do(func() { m.setAgentView(func(p *prefs) { p.AgentGroup, p.AgentCompact = agentGroupNone, true }) }), frames(4), shot("03-sidebar-compact"),
				do(func() { m.setAgentView(func(p *prefs) { p.AgentGroup, p.AgentCompact = agentGroupFolder, false }) }), frames(2),
				do(func() { m.togglePin(ggui.Peek(m.Sessions)[0].GetId()) }), frames(4), shot("03-sidebar-pinned"),
				do(func() { m.togglePin(ggui.Peek(m.Sessions)[0].GetId()) }), frames(2),
				// The right pane's tab dragged over the bottom of the left
				// pane, then let go where nothing takes it.
				do(func() {
					var tab ggui.Found
					for _, f := range p.FindAll(ggui.RoleButton) {
						if f.Label == sessionLabel(&protocol.SessionInfo{Agent: "shell"}) && f.Rect.Origin.Y < headerHeight+tabBarHeight {
							tab = f // the last is the right pane's
						}
					}
					at := tab.Center()
					p.Press(at)
					p.Move(ggui.Pt(at.X-12, at.Y+6))
					to := ggui.Pt(float64(width)*0.33, float64(height)*0.75)
					p.Move(to)
					p.Move(to)
				}),
				frames(2), shot("03-drag"),
				do(func() { p.Release(ggui.Pt(float64(width)/2, headerHeight/2)) }),
				do(func() { m.DetailTab.Set(gitTab) }), frames(30), shot("03-changes"),
				// The history of a project on this node's own daemon, which
				// another on the mesh may predate.
				do(func() {
					wd, _ := os.Getwd()
					m.launch("shell", ggui.Peek(m.Conn).Self.GetId(), wd)
					m.DetailTab.Set(gitTab)
				}),
				until("its session", func() bool {
					s, ok := m.session(ggui.Peek(m.Selected))
					return ok && s.GetNodeId() == ggui.Peek(m.Conn).Self.GetId()
				}),
				until("the history", func() bool { v := ggui.Peek(m.git.History); return v.Root.Path != "" && !v.Loading }),
				frames(5), shot("03-history"),
				// The changes folded, the stashes and branches open.
				do(func() { m.git.ChangesOpen.Set(false); m.git.StashOpen.Set(true); m.git.BranchOpen.Set(true) }),
				frames(5), shot("03-git-sections"),
				do(func() { m.git.ChangesOpen.Set(true); m.git.StashOpen.Set(false); m.git.BranchOpen.Set(false) }),
				do(func() {
					if cs := ggui.Peek(m.git.History).History.GetCommits(); len(cs) > 0 {
						m.openCommit(cs[0].GetHash())
					}
				}),
				until("the commit", func() bool { return !ggui.Peek(m.git.Commit).Loading }),
				frames(5), shot("03-commit"),
				// A file of it, its diff in a tab, beside a wider panel.
				do(func() {
					if fs := ggui.Peek(m.git.Commit).Info.GetFiles(); len(fs) > 0 {
						m.openCommitFile(fs[0])
					}
					m.DetailsWidth.Set(440)
				}),
				until("the diff", func() bool { d, ok := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]; return !ok || !d.Loading }),
				frames(30), shot("03-diff-tab"),
				do(func() { m.Prefs.Update(func(p prefs) prefs { p.DiffSplit = true; return p }) }),
				frames(10), shot("03-diff-split"),
				do(func() { m.Prefs.Update(func(p prefs) prefs { p.DiffSplit = false; return p }) }),
				do(func() {
					m.SearchQuery.Set("func")
					m.openSearch()
					m.runSearch()
				}),
				until("the search", func() bool { return !ggui.Peek(m.Search).Loading }),
				frames(5), shot("03-search"),
				do(func() { m.SearchOpen.Set(false) }),
				do(m.openQuickOpen), do(func() { m.pickers.QuickQuery.Set("view") }), frames(30), shot("03-quick-open"),
				do(func() { m.pickers.QuickOpen.Set(false); m.DetailTab.Set(gitTab) }),
				do(func() { m.DetailsWidth.Set(m.restingSize(detailsNode, detailsWidth)) }),
				do(func() { m.DetailTab.Set(agentsTab) }), frames(2), shot("03-details"),
				do(func() { m.DetailTab.Set(filesTab); m.openNew("", "") }), frames(2), shot("04-new-session"),
				do(func() { m.NewOpen.Set(false); m.openSettings("general") }), frames(2), shot("05-settings-general"),
				do(func() { m.Section.Set("appearance") }), frames(2), shot("06-settings-appearance"),
				do(func() { m.Section.Set("terminal") }), frames(2), shot("07-settings-terminal"),
				do(func() { m.Section.Set("daemon") }), frames(2), shot("08-settings-daemon"),
				do(func() { m.Section.Set("host") }), frames(2), shot("08-settings-host"),
				do(func() { m.Section.Set("agents") }), frames(2), shot("08-settings-agents"),
				do(func() { m.Section.Set("shortcuts") }), frames(2), shot("09-settings-shortcuts"),
				do(func() { m.Section.Set("extensions") }), frames(5), shot("09-settings-extensions"),
				do(setTheme("light")), frames(2), shot("10-light-settings"),
				do(func() { m.Page.Set("workspace") }), frames(10), shot("11-light-session"),
				// Background images under the window and the sessions,
				// then a theme of the user's in its editor, both kept in
				// memory: the themes folder is not touched.
				do(func() {
					_ = writeGradient(window, skin.Hex(0x2563EB), skin.Hex(0xE07070))
					_ = writeGradient(session, skin.Hex(0x35C264), skin.Hex(0xE2B04A))
					m.Prefs.Update(func(p prefs) prefs {
						p.Theme = "dark"
						p.WindowBackdrop = backdrop{Image: window, Opacity: 0.35}
						p.SessionBackdrop = backdrop{Image: session, Opacity: 0.2}
						return p
					})
				}), frames(40), shot("12-backdrop"),
				do(func() {
					pal := skin.Dark
					pal.Name, pal.AccentBright, pal.Sidebar = "Preview", skin.Hex(0xE2B04A), skin.Hex(0x101826)
					m.Themes.Set(map[string]skin.Palette{"Preview": pal})
					m.Prefs.Update(func(p prefs) prefs {
						p.Theme, p.WindowBackdrop, p.SessionBackdrop = "Preview", backdrop{}, backdrop{}
						return p
					})
					m.Page.Set("settings")
					m.Section.Set("appearance")
				}), frames(10), shot("13-theme-editor"),
				do(func() {
					m.Prefs.Update(func(p prefs) prefs {
						p.Theme = "dark"
						p.WindowBackdrop = backdrop{Image: window, Opacity: 0.6, Fit: "contain", Position: "bottom-right", Width: 420, MarginX: 24, MarginY: 24}
						return p
					})
				}), frames(40), shot("14-appearance-backdrop"),
				do(func() { m.Prefs.Set(saved); m.reloadThemes() }), frames(2),
			}
			// MAKO_PREVIEW_FORGE names a clone of a repository on a forge,
			// whose forge tab is rendered in place of the rest.
			if dir := os.Getenv("MAKO_PREVIEW_FORGE"); dir != "" {
				steps = forgePreview(m, dir, do, until, frames, shot)
			}
			if mdSrc != "" {
				steps = []previewStep{
					do(setTheme("dark")), frames(200), shot("50-markdown"),
					do(func() {
						if n, ok := p.Semantics().Find(ggui.RoleDisclosure, ""); ok {
							p.Click(n.Rect.Center())
						}
					}), frames(20), shot("51-markdown-details"),
				}
			}
			if browse != "" {
				steps = browserPreview(m, func() *browser { return b }, do, until, frames, shot)
			}
			// MAKO_PREVIEW_TABS names folders, as a path list, whose
			// sessions' and files' tabs share a pane.
			if dirs := os.Getenv("MAKO_PREVIEW_TABS"); dirs != "" {
				steps = tabsPreview(m, func() *ggui.Probe { return p }, filepath.SplitList(dirs), do, until, frames, shot)
			}
			// MAKO_PREVIEW_PICK names a folder the New session dialog's
			// picker starts in.
			if dir := os.Getenv("MAKO_PREVIEW_PICK"); dir != "" {
				steps = []previewStep{
					do(setTheme("dark")),
					until("the daemon", func() bool { return ggui.Peek(m.Conn).OK }),
					do(func() { m.openNew("", dir) }), frames(20), shot("40-new-session"),
					do(m.pickFolder), until("the picker", func() bool { return ggui.Peek(m.Pick.On) }), frames(20), shot("41-pick-folder"),
				}
			}
			if railOnly && !sidebarOnly && !settingsOnly {
				tick := func(i int) ggui.Point {
					return ggui.Pt(10, (float64(height)-rail.gap*float64(len(ggui.Peek(rail.prompts))))/2+rail.gap*(float64(i)+0.5))
				}
				steps = []previewStep{
					do(setTheme("dark")), frames(5),
					do(func() { rail.find.jumpTo(ggui.Peek(rail.prompts)[12].Key) }), frames(10), shot("rail"),
					do(func() { p.Move(tick(15)) }), frames(5), do(func() { p.Move(tick(16)) }), frames(5),
					do(func() { p.Move(tick(17)) }), frames(10), shot("rail-hover"),
				}
			}
			if sidebarOnly {
				steps = []previewStep{
					do(setTheme("dark")), frames(5), shot("sidebar"),
					do(func() { m.setScope(scopeKey{Section: "xxgg"}) }), frames(5), shot("sidebar-project"),
					do(func() { m.togglePinScope(scopeKey{Section: "xxgg"}) }), frames(5), shot("sidebar-pinned-project"),
					do(func() { m.togglePinScope(scopeKey{Section: "xxgg"}) }),
					do(func() { m.setAgentView(func(p *prefs) { p.AgentGroup = agentGroupMachine }) }), frames(5), shot("sidebar-machine"),
					do(func() { m.setAgentView(func(p *prefs) { p.AgentGroup, p.AgentCompact = agentGroupNone, true }) }), frames(5), shot("sidebar-compact"),
					do(setTheme("light")), frames(5), shot("sidebar-light"),
					do(func() { m.setSpace(1) }), frames(5), shot("sidebar-space-light"),
					do(setTheme("dark")), frames(5), shot("sidebar-space"),
					do(func() { m.setSpace(2) }), frames(5), shot("sidebar-space-default"),
				}
			}
			if settingsOnly {
				steps = []previewStep{do(setTheme("dark"))}
				for _, w := range []int{width, 900} {
					steps = append(steps, do(func() { p.Resize(ggui.Sz(float64(w), float64(height))); img = ggfx.NewImage(w, height) }))
					for _, g := range settingsNav {
						for _, s := range g.sections {
							steps = append(steps, do(func() { m.Section.Set(s.id) }), frames(3), shot(fmt.Sprintf("settings-%d-%s", w, s.id)))
						}
					}
				}
			}
			if agentsOnly {
				steps = []previewStep{
					do(setTheme(saved.Theme)), frames(3), shot("agents-panel"),
					do(func() { p.Tap(Tf("Filter messages for %s", "Coordinator")) }), frames(3), shot("agents-panel-filtered"),
					do(func() { p.Resize(ggui.Sz(260, float64(height))); img = ggfx.NewImage(260, height) }), frames(3), shot("agents-panel-narrow"),
				}
			}
		case ggwd.FrameEvent:
			p.Frame()
			if len(steps) == 0 {
				m.shutdown()
				p.Close()
				return ggwd.Termination
			}
			done, err := steps[0]()
			if err != nil {
				m.shutdown()
				p.Close()
				return err
			}
			if done {
				steps = steps[1:]
			}
			window.RequestFrame()
		}
		return nil
	}), nil)
}

// forgePreview renders the forge tab of the repository cloned in dir:
// its lists, a pull request, issue and run in full, a pull request's
// diff, the Git tab's line for the branch, and the new pull request
// dialog.
func forgePreview(m *model, dir string, do func(func()) previewStep, until func(string, func() bool) previewStep,
	frames func(int) previewStep, shot func(string) previewStep) []previewStep {
	listed := func() bool {
		v := ggui.Peek(m.Forge.List)
		return v.Root.Path == dir && v.Kind == ggui.Peek(m.Forge.Kind) && !v.Loading
	}
	// A list with nothing in it has nothing to open.
	opened := func() bool {
		o := ggui.Peek(m.Forge.Open)
		return len(ggui.Peek(m.Forge.List).Items) == 0 || o.Kind != "" && !o.Loading
	}
	openFirst := func() {
		if items := ggui.Peek(m.Forge.List).Items; len(items) > 0 {
			m.openForge(ggui.Peek(m.Forge.List).Root, items[0].GetKind(), items[0].GetNumber())
		}
	}
	list := func(kind, name string) []previewStep {
		return []previewStep{
			do(func() { m.Forge.Open.Set(forgeOpen{}); m.Forge.Kind.Set(kind) }),
			until("the "+kind+" list", listed), frames(5), shot("20-github-" + name + "s"),
			do(openFirst), until("the "+kind, opened), frames(20), shot("21-github-" + name),
		}
	}
	steps := []previewStep{
		do(func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = "dark"; return p }) }),
		until("the daemon", func() bool { return ggui.Peek(m.Conn).OK || ggui.Peek(m.Conn).Err != "" }),
		do(func() {
			m.launch("shell", ggui.Peek(m.Conn).Self.GetId(), dir)
			m.DetailsWidth.Set(420)
			m.DetailTab.Set(forgeTab)
		}),
		until("its session", func() bool { s, ok := m.session(ggui.Peek(m.Selected)); return ok && s.GetProject() == dir }),
	}
	steps = append(steps, list(protocol.ForgePR, "pr")...)
	steps = append(steps,
		do(func() {
			if o := ggui.Peek(m.Forge.Open); len(o.Detail.GetFiles()) > 0 {
				m.openDiffFrom(o.diffFrom(), o.Detail.GetFiles()[0], true)
			}
		}),
		until("the diff", func() bool { d, ok := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]; return !ok || !d.Loading }),
		frames(20), shot("22-github-pr-diff"),
	)
	steps = append(steps, list(protocol.ForgeIssue, "issue")...)
	steps = append(steps, list(protocol.ForgeRuns, "run")...)
	return append(steps,
		do(func() { m.DetailTab.Set(gitTab) }),
		until("the branch", func() bool { return ggui.Peek(m.Forge.Branch).Root.Path == dir }),
		frames(30), shot("23-github-branch"),
		do(m.askPR), frames(5), shot("24-github-new-pr"),
		do(func() { m.Forge.PROpen.Set(false) }),
	)
}

// tabsPreview renders a pane's tabs of a project merged of the folders
// dirs, kept together by folder: a session in each folder, and a second
// in the first, then a file of the last previewed and one of the first
// opened.
func tabsPreview(m *model, p func() *ggui.Probe, dirs []string, do func(func()) previewStep, until func(string, func() bool) previewStep,
	frames func(int) previewStep, shot func(string) previewStep) []previewStep {
	steps := []previewStep{
		do(func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = "dark"; return p }) }),
		until("the daemon", func() bool { return ggui.Peek(m.Conn).OK || ggui.Peek(m.Conn).Err != "" }),
		do(func() {
			self := ggui.Peek(m.Conn).Self.GetId()
			p := project{ID: "preview-merged", Name: "merged"}
			for _, dir := range dirs {
				p.Roots = append(p.Roots, projectRoot{Locations: []fileRoot{{Node: self, Path: dir}}})
			}
			m.Spaces.Update(func(l spaceList) spaceList { l.Projects = setProject(l.Projects, p); return l })
		}),
	}
	launch := func(dir string, n int) []previewStep {
		return []previewStep{
			do(func() { m.launch("shell", ggui.Peek(m.Conn).Self.GetId(), dir) }),
			until("its session", func() bool { return len(ggui.Peek(m.Tabs)) >= n }),
			frames(10),
		}
	}
	for i, dir := range dirs {
		steps = append(steps, launch(dir, i+1)...)
	}
	steps = append(steps, launch(dirs[0], len(dirs)+1)...)
	file := func(dir, name string) fileItem {
		self := ggui.Peek(m.Conn).Self.GetId()
		path := joinPath(dir, name)
		return fileItem{Key: dirKey(self, path), Node: self, Path: path, Name: name}
	}
	return append(steps,
		frames(20), shot("60-tabs"),
		do(func() { m.previewFile(file(dirs[len(dirs)-1], "README.md")) }),
		frames(30), shot("61-tabs-preview"),
		do(func() { m.openFile(file(dirs[0], "README.md")) }),
		frames(30), shot("62-tabs-file"),
		// The pointer over a tab not shown: tinted, its close button out.
		do(func() {
			// The sidebar names sessions too: the tab is the one in a bar.
			for _, f := range p().FindAll("") {
				if f.Label == "shell" && f.Rect.Origin.Y < 2*tabBarHeight && f.Rect.Origin.X > sidebarWidth {
					p().Move(f.Center())
					break
				}
			}
		}), frames(5), shot("62-tabs-hover"),
		do(func() { p().Move(ggui.Pt(600, 500)) }),
		// The palette, its commands' English names after them, and found
		// as typed with the input method left on Korean.
		do(m.openPalette), frames(10), shot("63-palette"),
		do(func() { m.pickers.PaletteQuery.Set("ㄴㄷㅅ샤") }), frames(5), shot("64-palette-as-typed"),
	)
}

// browserPreview renders a node's file browser: a folder, one open in it,
// a copy under way and done, and the rename prompt.
func browserPreview(m *model, b func() *browser, do func(func()) previewStep, until func(string, func() bool) previewStep,
	frames func(int) previewStep, shot func(string) previewStep) []previewStep {
	rows := func() []fileItem { return m.children(dirItem("", ggui.Peek(b().Path))) }
	read := func() bool { r := rows(); return len(r) > 0 && !r[0].Stub }
	firstDir := func() (fileItem, bool) {
		for _, it := range rows() {
			if it.Dir {
				return it, true
			}
		}
		return fileItem{}, false
	}
	return []previewStep{
		do(func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = "dark"; return p }) }),
		until("the folder", read), frames(5), shot("30-browser"),
		do(func() {
			if it, ok := firstDir(); ok {
				b().Open.Set(map[string]bool{it.Key: true})
				m.loadDir(it.Node, it.Path, false)
				b().Sel.Set(it.Key)
			}
		}),
		frames(30), shot("31-browser-open"),
		do(func() { b().Sort.Set(browserSort{By: sortSize, Desc: true}); b().Query.Set("go") }), frames(5), shot("31-browser-sorted"),
		do(func() { b().Query.Set("no such file") }), frames(5), shot("31-browser-filtered-out"),
		do(func() { b().Sort.Set(browserSort{}); b().Query.Set("") }),
		do(func() { b().Busy.Set(Tf("Uploading %s", "build.zip") + " · 12.4 MB / 48.0 MB") }), frames(3), shot("32-browser-busy"),
		do(func() { b().Busy.Set(""); b().say(Tf("Downloaded to %s.", "~/Downloads/build.zip"), "/tmp") }), frames(3), shot("33-browser-done"),
		do(func() {
			if r := rows(); len(r) > 0 {
				b().askFile(protocol.FileRename, r[len(r)-1])
			}
		}), frames(10), shot("34-browser-rename"),
	}
}

// previewStatus has the session id ask for an approval, and the others
// show the states their agents can be in, for the preview.
func previewStatus(sessions []*protocol.SessionInfo, id string) []*protocol.SessionInfo {
	out := make([]*protocol.SessionInfo, len(sessions))
	for i, s := range sessions {
		out[i] = proto.CloneOf(s) // the listing held is shared
	}
	states := []protocol.AgentStatus{protocol.AgentWorking, protocol.AgentDone, protocol.AgentIdle}
	n := 0
	for i := range out {
		out[i].State = protocol.StateRunning
		if out[i].GetId() == id {
			out[i].Status = protocol.AgentWaiting
			out[i].Approval = previewApprovals()[0]
			out[i].Controls = &protocol.AgentControls{Modes: []string{"default", "acceptEdits", "plan", "auto"},
				Models: []string{"opus", "sonnet", "haiku"}, Efforts: []string{"low", "medium", "high"}}
			continue
		}
		out[i].Status = states[n%len(states)]
		n++
	}
	return out
}

// previewApprovals are approvals of the kinds a card shows its own way: a
// command offered a rule, an edit offered a mode, a plan.
func previewApprovals() []*protocol.Approval {
	return []*protocol.Approval{
		{Id: "preview", Tool: "Bash", Kind: protocol.ToolShell, Detail: "rm -rf build && go build ./...",
			Input:   "{\n  \"command\": \"rm -rf build && go build ./...\",\n  \"description\": \"Rebuild from scratch\"\n}",
			Options: []*protocol.ApprovalOption{{Id: "0", Rules: []string{"Bash(go build *)"}}}},
		{Id: "preview-edit", Tool: "Edit", Kind: protocol.ToolEdit, Detail: "internal/gui/approval.go",
			Input:   `{"new": "func approvalCard(m *model, id string) ggui.Widget {\n\treturn planCard(m, id)\n}", "old": "func approvalCard(m *model, id string) ggui.Widget {\n\treturn nil\n}", "path": "internal/gui/approval.go"}`,
			Options: []*protocol.ApprovalOption{{Id: "0", Mode: "acceptEdits"}}},
		{Id: "preview-plan", Tool: "ExitPlanMode", Kind: protocol.ToolExitPlan, Detail: "Plan: Show plans in the window",
			Input:   `{"plan": "# Plan: Show plans in the window\n\n## Context\nA plan Claude puts to the user shows only its first line.\n\n## Steps\n1. Read ExitPlanMode as a kind of its own.\n2. Render the plan as **Markdown** in its approval card.\n3. Offer the modes the work goes on in."}`,
			Options: []*protocol.ApprovalOption{{Id: "auto", Mode: "auto"}, {Id: "acceptEdits", Mode: "acceptEdits"}, {Id: "default", Mode: "default"}}},
	}
}

// setPreviewApproval puts a in place of the selected session's approval.
func setPreviewApproval(m *model, a *protocol.Approval) {
	id := ggui.Peek(m.Selected)
	m.editSession(id, func(s *protocol.SessionInfo) { s.Approval = a })
}

// previewStep runs on a frame and reports whether it is finished.
type previewStep func() (done bool, err error)

// previewTools are tool calls of the kinds the chat reads, as Claude Code
// and Codex write them.
func previewTools() []*protocol.ChatMessage {
	return []*protocol.ChatMessage{
		{Role: "tool", Id: "t1", Tool: "Bash", Detail: "go test ./internal/gui", Text: `{"command": "go test ./internal/gui", "description": "Run the GUI tests"}`,
			Result: "ok  \tgithub.com/ironpark/mako/internal/gui\t7.450s\n"},
		{Role: "tool", Id: "t2", Tool: "Edit", Detail: "internal/gui/view.go", Text: `{"file_path": "internal/gui/view.go", "old_string": "\tsidebarWidth = 272\n\tdetailsWidth = 320", "new_string": "\tsidebarWidth = 272 // until dragged\n\tdetailsWidth = 320"}`},
		{Role: "tool", Id: "t3", Tool: "TodoWrite", Kind: protocol.ToolTodo, Text: `{"items": [{"text": "Resize the sidebar", "status": "completed"}, {"text": "Fold sidebar groups", "status": "in_progress"}, {"text": "Render tool calls", "status": "pending"}]}`},
		{Role: "tool", Id: "t4", Tool: "exec_command", Detail: "git status --short", Text: `{"cmd": "git status --short", "workdir": "/Users/me/mako"}`,
			Result: "Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n M internal/gui/view.go\n?? internal/gui/toolview.go\n"},
		{Role: "tool", Id: "t5", Tool: "apply_patch", Text: "*** Begin Patch\n*** Update File: internal/gui/prefs.go\n@@ type prefs struct {\n-\tDetailsWidth float64\n+\tSidebarWidth float64\n+\tDetailsWidth float64\n*** End Patch"},
	}
}

func projectAgentsPreviewData(root fileRoot) *protocol.ProjectAgents {
	return &protocol.ProjectAgents{Project: root.Path,
		Agents: []*protocol.ProjectAgent{
			{Session: &protocol.SessionInfo{Id: "coordinator", NodeId: root.Node, Project: root.Path, Name: "Coordinator", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentWorking}, Messaging: true},
			{Session: &protocol.SessionInfo{Id: "reviewer", NodeId: root.Node, Project: root.Path, Name: "Reviewer", Agent: "codex", State: protocol.StateRunning, Status: protocol.AgentWaiting}, ParentSessionId: "coordinator", Messaging: true, PendingMessages: 1},
			{Session: &protocol.SessionInfo{Id: "builder", NodeId: root.Node, Project: root.Path, Name: "Builder", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentDone}, ParentSessionId: "coordinator", Messaging: true},
		},
		Messages: []*protocol.AgentMessage{
			{Id: 1, FromSessionId: "coordinator", ToSessionId: "reviewer", Text: "Review cancellation", State: "pending", CreatedAt: protocol.Time(time.Now())},
			{Id: 2, FromSessionId: "coordinator", ToSessionId: "builder", Text: "Build passed", State: "acknowledged", CreatedAt: protocol.Time(time.Now())},
			{Id: 3, FromSessionId: "builder", ToSessionId: "coordinator", Text: "The GUI and daemon packages build and their tests pass.\nThe one flaky test left is the tunnel's, which times out under -race.", State: "delivered", CreatedAt: protocol.Time(time.Now())},
			{Id: 4, FromSessionId: "reviewer", ToSessionId: "coordinator", Text: "On it", State: "delivered", CreatedAt: protocol.Time(time.Now())},
		},
	}
}

// railPreview is a long conversation's feed with its turn rail, for
// MAKO_PREVIEW_RAIL.
func railPreview(m *model) (ggui.Widget, *turnRail) {
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "preview", Agent: "claude", Project: "/workspace/mako"}})
	state := m.chatState("preview")
	var messages []*protocol.ChatMessage
	for i := range 30 {
		messages = append(messages,
			&protocol.ChatMessage{Role: "user", Id: fmt.Sprintf("u%d", i), Text: fmt.Sprintf("C%d를 완료하려면?", i)},
			&protocol.ChatMessage{Role: "assistant", Id: fmt.Sprintf("a%d", i), Text: fmt.Sprintf(
				"현재 남은 항목은 **C5.%d 원격 CI**, **C6.1 전체 검증**, **C6.7 종료 판정**입니다. 기능 수정과 로컬 build/check는 통과했습니다.\n\n- 대상 브랜치는 main입니다.\n- 전체 검증은 CI에서 다시 돕니다.\n- 바이너리는 릴리스에 올립니다.", i)})
	}
	turns := ggui.State(chatTurns(messages, nil))
	scroll := &widget.ScrollController{}
	find := &chatFinder{turns: turns, scroll: scroll, marks: map[string]rowMark{}}
	rail := newTurnRail(m, find, turns)
	scrolling, feed := chatFeed(m, "preview", state, turns, widget.Box(), find)
	scrolling.Controller(scroll)
	return widget.Box(widget.Stack(feed, rail, &afterPaint{fn: find.painted}).Expand()).Fill(skin.Surface0), rail
}

// sidebarPreview gives the sidebar a mesh of four machines, a project of
// four repositories and a few of one, and sessions across them, for
// MAKO_PREVIEW_SIDEBAR.
func sidebarPreview(m *model) {
	self := &protocol.Node{Id: "studio", Name: "MacStudio.local", Os: "darwin", Source: "self"}
	nodes := []*protocol.Node{self,
		{Id: "air", Name: "MacBook-Air", Os: "darwin", Source: "mesh"},
		{Id: "box", Name: "build-box", Os: "linux", Source: "mesh"},
		{Id: "desk", Name: "desktop", Os: "windows", Source: "mesh"},
	}
	m.Nodes.Set(nodes)
	m.Conn.Set(connState{OK: true, Self: self})
	root := func(name string) projectRoot {
		return projectRoot{Remote: "github.com/xxgg/" + name, Owner: "github.com/xxgg", Locations: []fileRoot{{Node: "studio", Path: "/w/" + name}}}
	}
	m.Spaces.Set(spaceList{Default: "side", Spaces: []space{
		{ID: "work", Name: "Work", Color: "blue", Owners: []string{"github.com/xxgg"}},
		{ID: "side", Name: "Side projects", Color: "green"},
	}, Projects: []project{{ID: "xxgg", Name: "XXGG/MAKO", Roots: []projectRoot{root("mako"), root("ggui"), root("ggfx"), root("ggwd")}}}})
	type row struct{ id, node, dir, agent, title, status, branch string }
	rows := []row{
		{"50518a4e", "studio", "/w/mako", "claude", "메모리 개선", "", "main"},
		{"a6660693", "studio", "/w/mako", "claude", "프롬프트 템플릿 인자 및 자동완성", protocol.AgentDone, "main"},
		{"5d09dc58", "box", "/w/ggui", "codex", "range-drawer", "", "ci/release"},
		{"9593a6cb", "studio", "/w/ggui", "claude", "Ggui 루트 Go 파일 구조", protocol.AgentWorking, "main"},
		{"31d6d43d", "studio", "/w/ggui", "claude", "claude", protocol.AgentWaiting, "main"},
		{"6a3b0c59", "air", "/w/ggui", "codex", "code-trace", "", "main"},
		{"e81f22c0", "box", "/w/mako", "codex", "Linux 패키징", protocol.AgentWorking, "ci/release"},
		{"4d2a9b17", "air", "/w/ggwd", "claude", "Wayland 입력 메서드", protocol.AgentWaiting, "main"},
		{"19c675d2", "studio", "/w/ggfx", "claude", "gogpu/naga 의존성 업데이트", "", "main"},
		{"6f0e3d21", "desk", "/w/ggwd", "codex", "Windows IME 조합 창 위치", protocol.AgentWorking, "windows-ime"},
		{"0b14e2aa", "studio", "/w/acp-go", "claude", "ACP 핸드셰이크 정리", "", "main"},
		{"7e01c9d3", "studio", "/w/gostty", "claude", "키 인코딩 회귀 수정", "", "main"},
		{"aa31b7e2", "studio", "/w/qngg", "claude", "렌더 루프 프로파일링", protocol.AgentWorking, "main"},
	}
	var sessions []*protocol.SessionInfo
	titles := map[string]string{}
	printed := map[string]time.Time{}
	now := time.Now().Truncate(time.Minute)
	for i, r := range rows {
		owner, remote := "", ""
		if slices.Contains([]string{"/w/mako", "/w/ggui", "/w/ggfx", "/w/ggwd"}, r.dir) {
			owner, remote = "github.com/xxgg", "github.com/xxgg/"+projectName(r.dir)
		}
		sessions = append(sessions, &protocol.SessionInfo{Id: r.id + "00000000", NodeId: r.node, Project: r.dir, Repo: r.dir, Owner: owner, Remote: remote,
			Agent: r.agent, State: protocol.StateRunning, Status: r.status, Branch: r.branch})
		titles[r.id+"00000000"] = r.title
		printed[r.id+"00000000"] = now.Add(-time.Duration(i*i*17) * time.Minute)
	}
	m.Sessions.Set(sessions)
	m.Titles.Set(titles)
	m.Printed.Set(printed)
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Pinned = []string{"9593a6cb00000000"}
		l.PinnedProjects = []scopeKey{{Section: "xxgg", Root: "github.com/xxgg/ggui"}}
		return l
	})
}
