package gui

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/fonts/gofont"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

// Preview renders the window against a live daemon into PNGs in dir,
// without showing it, for reviewing layout: the workspace with a session
// attached, the dialogs, the settings pages, and the light theme. It is a
// development aid; the daemon should be running a session or two. The theme
// setting it switches through is put back when it is done.
func Preview(cfg *config.Config, addr, dir string, width, height int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		m      *model
		p      *ggui.Probe
		img    *ggfx.Image
		window *ggfx.Window
		steps  []previewStep
		since  time.Time
	)
	shot := func(name string) previewStep {
		return func() (bool, error) {
			img.Clear()
			p.Draw(img)
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				return false, err
			}
			defer f.Close()
			return true, png.Encode(f, img)
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

	return ggfx.Run(ggfx.HandlerFunc(func(ev ggfx.Event) error {
		switch ev.(type) {
		case ggfx.StartEvent:
			w, err := ggfx.NewWindow(&ggfx.WindowOptions{Title: "sanbo preview", Width: 64, Height: 64, Hidden: true})
			if err != nil {
				return err
			}
			window = w
			// A probe draws in Go Regular; Go Mono stands in for the
			// platform's monospaced font so the grid lines up as it will.
			ggui.SetDefaultMonoFont(gofont.Mono())
			m = newModel(cfg, addr)
			saved := ggui.Peek(m.Prefs)
			p = ggui.ProbeBuilder(func() ggui.Widget { return ggui.Provide(ggui.ReducedMotionKey, true, build(m)) },
				ggui.Sz(float64(width), float64(height)))
			p.Setup(func() {
				setup(m)
				m.start(ctx, p.Post)
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
							{Role: "tool", Text: "go test ./internal/transcript\nok   github.com/ironpark/sanbo/internal/transcript"},
							{Role: "assistant", Text: "Ready to try. Use **⌘⇧J** to switch views."},
						},
					}})
					m.setChatMode(id, true)
				}),
				frames(5), shot("02-chat"),
				// The pointer over the agent's first reply shows its copy button.
				do(func() { p.Move(ggui.Pt(600, 200)) }), frames(3), do(func() { p.Move(ggui.Pt(620, 210)) }), frames(5), shot("02-chat-hover"),
				// An agent asking for an approval, and a long run of tools.
				do(func() {
					id := ggui.Peek(m.Selected)
					chat := m.chatState(id)
					v := ggui.Peek(chat.result)
					v.Chat = proto.CloneOf(v.Chat) // the result held is shared
					for _, cmd := range []string{"ls internal", "go vet ./...", "go test ./internal/gui", "git status --short"} {
						v.Chat.Messages = append(v.Chat.Messages, &protocol.ChatMessage{Role: "tool", Tool: "Bash", Detail: cmd, Text: cmd})
					}
					v.Chat.Usage = &protocol.Usage{Input: 41200, CachedInput: 380000, Output: 9800, Context: 96000, Window: 258400}
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
				// The project's files, a folder open and a file previewed.
				do(func() {
					r := ggui.Untrack(m.filesRoot)
					dir := joinPath(r.Path, "internal")
					m.toggleDir(fileItem{Key: dirKey(r.Node, dir), Node: r.Node, Path: dir, Dir: true})
					readme := joinPath(r.Path, "README.md")
					m.openPreview(fileItem{Key: dirKey(r.Node, readme), Node: r.Node, Path: readme, Name: "README.md"})
				}),
				until("the preview", func() bool { return !ggui.Peek(m.Preview).Loading }),
				frames(5), shot("03-files"),
				// Files of a few kinds, each with its type's icon.
				do(func() {
					r := ggui.Untrack(m.filesRoot)
					for _, sub := range []string{"internal", "internal/gitstat"} {
						dir := joinPath(r.Path, sub)
						m.toggleDir(fileItem{Key: dirKey(r.Node, dir), Node: r.Node, Path: dir, Dir: true})
					}
					m.Preview.Set(filePreview{})
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
				do(func() { m.PaletteQuery.Set("side") }), frames(4), shot("03-palette-search"),
				do(func() { m.PaletteOpen.Set(false); m.ExtensionsOpen.Set(true) }),
				until("the extension store", func() bool { r := ggui.Peek(m.ExtResults); return r.Listings != nil || r.Err != "" }),
				frames(10), shot("03-extensions"),
				do(func() { m.ExtensionsOpen.Set(false); m.setSidebarGroup(groupHost) }), frames(10), shot("03-sidebar-host"),
				do(func() { m.setSidebarGroup(groupFlat) }), frames(4), shot("03-sidebar-flat"),
				do(func() { m.setSidebarGroup(groupProject) }), frames(2),
				do(func() {
					m.togglePin(ggui.Peek(m.Sessions)[0].GetId())
					m.FilterOpen.Set(true)
					m.SidebarFilter.Set("shell")
				}), frames(4), shot("03-sidebar-filter"),
				do(func() {
					m.togglePin(ggui.Peek(m.Sessions)[0].GetId())
					m.SidebarFilter.Set("")
					m.FilterOpen.Set(false)
				}), frames(2),
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
				do(func() { m.DetailTab.Set(changesTab) }), frames(30), shot("03-changes"),
				// The history of a project on this node's own daemon, which
				// another on the mesh may predate.
				do(func() {
					wd, _ := os.Getwd()
					m.launch("shell", ggui.Peek(m.Conn).Self.GetId(), wd)
					m.DetailTab.Set(historyTab)
				}),
				until("its session", func() bool {
					s, ok := m.session(ggui.Peek(m.Selected))
					return ok && s.GetNodeId() == ggui.Peek(m.Conn).Self.GetId()
				}),
				until("the history", func() bool { v := ggui.Peek(m.GitHistory); return v.Root.Path != "" && !v.Loading }),
				frames(5), shot("03-history"),
				do(func() {
					if cs := ggui.Peek(m.GitHistory).History.GetCommits(); len(cs) > 0 {
						m.openCommit(cs[0].GetHash())
					}
				}),
				until("the commit", func() bool { return !ggui.Peek(m.GitCommit).Loading }),
				frames(5), shot("03-commit"),
				// A file of it, its diff in a tab, beside a wider panel.
				do(func() {
					if fs := ggui.Peek(m.GitCommit).Info.GetFiles(); len(fs) > 0 {
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
				do(m.openQuickOpen), do(func() { m.QuickQuery.Set("view") }), frames(30), shot("03-quick-open"),
				do(func() { m.QuickOpen.Set(false); m.DetailTab.Set(historyTab) }),
				do(func() { m.DetailsWidth.Set(ggui.Peek(m.Prefs).DetailsWidth) }),
				do(func() { m.DetailTab.Set(sessionTab) }), frames(2), shot("03-details"),
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
				do(func() { m.Prefs.Set(saved) }), frames(2),
			}
		case ggfx.FrameEvent:
			p.Frame()
			if len(steps) == 0 {
				m.shutdown()
				p.Close()
				return ggfx.Termination
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
			out[i].Approval = &protocol.Approval{Id: "preview", Tool: "Bash", Detail: "rm -rf build && go build ./...",
				Input: "{\n  \"command\": \"rm -rf build && go build ./...\"\n}"}
			continue
		}
		out[i].Status = states[n%len(states)]
		n++
	}
	return out
}

// previewStep runs on a frame and reports whether it is finished.
type previewStep func() (done bool, err error)

// previewTools are tool calls of the kinds the chat reads, as Claude Code
// and Codex write them.
func previewTools() []*protocol.ChatMessage {
	return []*protocol.ChatMessage{
		{Role: "tool", Id: "t1", Tool: "Bash", Detail: "go test ./internal/gui", Text: `{"command": "go test ./internal/gui", "description": "Run the GUI tests"}`,
			Result: "ok  \tgithub.com/ironpark/sanbo/internal/gui\t7.450s\n"},
		{Role: "tool", Id: "t2", Tool: "Edit", Detail: "internal/gui/view.go", Text: `{"file_path": "internal/gui/view.go", "old_string": "\tsidebarWidth = 272\n\tdetailsWidth = 320", "new_string": "\tsidebarWidth = 272 // until dragged\n\tdetailsWidth = 320"}`},
		{Role: "tool", Id: "t3", Tool: "TodoWrite", Text: `{"todos": [{"content": "Resize the sidebar", "status": "completed"}, {"content": "Fold sidebar groups", "status": "in_progress"}, {"content": "Render tool calls", "status": "pending"}]}`},
		{Role: "tool", Id: "t4", Tool: "exec_command", Detail: "git status --short", Text: `{"cmd": "git status --short", "workdir": "/Users/me/sanbo"}`,
			Result: "Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n M internal/gui/view.go\n?? internal/gui/toolview.go\n"},
		{Role: "tool", Id: "t5", Tool: "apply_patch", Text: "*** Begin Patch\n*** Update File: internal/gui/prefs.go\n@@ type prefs struct {\n-\tDetailsWidth float64\n+\tSidebarWidth float64\n+\tDetailsWidth float64\n*** End Patch"},
	}
}
