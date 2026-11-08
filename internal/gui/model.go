package gui

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// newSession is the new-session dialog's form.
type newSession struct {
	Agent   string // a target: agent, or agent:profile
	NodeID  string
	Project string
	// Worktree starts the session in a new git worktree, on Branch.
	Worktree bool
	Branch   string
	Base     string // the branch the worktree merges into, "" for the one checked out
	Start    string // the commit it starts at, when not Base
	// Prompt is put in the new session's message box, to send once its
	// agent is ready.
	Prompt string
}

// model is the GUI's state. Signals are touched on the UI goroutine only;
// workers hand results back through post.
type model struct {
	cfg  *config.Config
	post func(func())
	// link is the daemon the window talks to, shared with the workers
	// that poll it; see link.go.
	link *daemonLink

	Prefs *ggui.StateValue[prefs]
	// Spaces are the spaces, the workspace's, and spaceSet them with the
	// one shown; see spaces.go.
	Spaces   *ggui.StateValue[spaceList]
	spaceSet *ggui.DerivedValue[spaceSet]
	// backdropSeen is the size each background image was last laid out
	// at, which the settings' preview of it takes the shape of; for the
	// sessions', the pane laid out last.
	backdropSeen [2]ggui.Size
	// Themes are the user's themes, by name; ThemesRead counts their
	// readings from disk, so the editor starts again on what was read.
	Themes     *ggui.StateValue[map[string]skin.Palette]
	ThemesRead *ggui.StateValue[int]
	Page       *ggui.StateValue[string] // "workspace" or "settings"
	Section    *ggui.StateValue[string] // the settings page shown
	Host       *ggui.StateValue[hostDraft]
	AgentDraft *ggui.StateValue[[]agentDraft]
	HostNote   *ggui.StateValue[string]
	RenameOpen *ggui.StateValue[bool]
	RenameText *ggui.StateValue[string]
	RenameErr  *ggui.StateValue[string]
	renaming   string                   // the session RenameOpen names
	FindOpen   *ggui.StateValue[bool]   // the find bar is over the selected session; see find.go
	FindText   *ggui.StateValue[string] // the words it finds
	findFocus  bool                     // the find bar's field takes the focus as it is painted
	quitting   bool
	quitAsked  bool       // a quit, not the close button, is closing the window; see tray.go
	tray       *ggui.Tray // the menu bar's icon, in tray mode
	badge      string     // the count of sessions waiting, as the Dock's badge shows it

	pickers *pickerState      // the command palette and quick open; see pickers.go
	toaster *ui.ToasterWidget // notices over the window, such as a dropped file sent
	// The Search tab: what is typed, what was found, and whether its field
	// is to take the keys when it next shows.
	SearchQuery *ggui.StateValue[string]
	Search      *ggui.StateValue[searchView]
	searchFocus bool

	DaemonState *ggui.StateValue[daemonState]
	DaemonLog   *ggui.StateValue[[]string]
	embedded    *embeddedDaemon
	daemonGen   int // UI goroutine only; see startEmbedded

	Conn  *ggui.StateValue[connState]
	Nodes *ggui.StateValue[[]*protocol.Node]
	// Access is who the daemon lets in and the requests waiting; nil while
	// unknown, or with the mesh off.
	Access      *ggui.StateValue[*protocol.AccessInfo]
	AccessNote  *ggui.StateValue[string] // the last access change's outcome
	AccessKey   *ggui.StateValue[string] // a key being let in, as typed
	asked       map[string]ui.ToastID    // requests told of, by their toasts; UI goroutine only
	ws          *wsState                 // how the workspace is kept and written down; see wsstate.go
	prefsWriter *fileWriter              // writes gui.json
	// tabNodes are the nodes of session tabs, and missing when each tab's
	// session went missing from the list; see leftover.go.
	tabNodes map[string]string
	missing  map[string]time.Time
	// Devices are the mesh's devices, and those the daemon borrows;
	// devicesAt is when the poll last asked for them, on its goroutine.
	Devices   *ggui.StateValue[deviceList]
	devicesAt time.Time
	Sessions  *ggui.StateValue[[]*protocol.SessionInfo]
	Agents    *ggui.StateValue[[]string] // what this node can launch, as agent:profile targets

	Selected   *ggui.StateValue[string]      // the tab the current pane shows
	Tabs       *ggui.StateValue[[]string]    // every pane's tabs, in order
	Layout     *ggui.StateValue[*layoutNode] // the panes; see layout.go
	FocusGroup *ggui.StateValue[int]         // the current pane
	Dragging   *ggui.StateValue[bool]        // a tab is being dragged
	TabHint    *ggui.StateValue[tabHint]     // where in a tab bar it would drop
	Attached   *ggui.StateValue[[]string]    // tabs whose terminal is connected
	Titles     *ggui.StateValue[map[string]string]
	// Unread are the sessions that finished or asked out of view; see
	// attention.go.
	Unread *ggui.StateValue[map[string]bool]
	// Printed is when each session last said something, its conversation's
	// last message or else its last print, to the minute, kept apart
	// from Sessions so a busy program does not redraw every list of them;
	// Minute moves on each minute, so the times shown age.
	Printed   *ggui.StateValue[map[string]time.Time]
	Minute    *ggui.StateValue[time.Time]
	window    *ggui.Window                      // nil under a probe
	ChatModes *ggui.StateValue[map[string]bool] // true shows chat; terminal stays attached
	chats     map[string]*chatState
	// chatImages are the images Chat and pages show, read once, by node
	// and path or by address, the latest used of them; see imageCache.
	chatImages *imageCache
	usages     map[string]*usageState // for the usage overlay

	files *filesState // the Files tab's; see filesstate.go

	// The files open in tabs, by tab ID; see docs.go.
	Docs *ggui.StateValue[map[string]docInfo]
	// Diffs are the diffs open in tabs, by tab.
	Diffs *ggui.StateValue[map[string]diffTab]
	docs  map[string]*document // UI goroutine only

	hl  *highlighter // highlighting, which takes languages from ext; see highlight.go
	ext *extState    // the extensions and MCP servers; see extstate.go

	git      *gitState // the Git panel's; see gitstate.go
	RepoPick *ggui.StateValue[repoPick]
	// Roots are the folders the Files, Git and forge tabs show; see
	// panelRoots.
	Roots *ggui.DerivedValue[panelRoots] // the project's folder the tab shows; see repoFolder
	// An operation that loses work, asking to be sure; see confirm.go.
	Confirmation *ggui.StateValue[confirmation]
	AgentsSplit  *ggui.StateValue[float64] // the agents panel's agents over its messages

	SidebarOpen *ggui.StateValue[bool]
	DetailsOpen *ggui.StateValue[bool]
	// DetailsWidth is the right panel's width, following a drag of its
	// edge; the settings keep it when the drag ends.
	DetailsWidth     *ggui.StateValue[float64]
	SidebarWidth     *ggui.StateValue[float64] // the sidebar's, as DetailsWidth
	DetailTab        *ggui.StateValue[int]
	ProjectAgents    *ggui.StateValue[projectAgentsView]
	projectAgentsGen uint64
	Forge            forgeState                          // the GitHub tab, and the watch on the branch
	Tunnels          *tunnelState                        // ports of other nodes forwarded here
	Pick             *folderPick                         // the New session dialog's folder picker
	Proj             *projectAsk                         // the project dialogs; see projectmenu.go
	projectsSent     map[string][]*protocol.ProjectScope // by node, the projects its daemon was last told; UI goroutine only
	Shell            *shellView                          // a background command shown in full; see tasks.go
	NewOpen          *ggui.StateValue[bool]
	Form             *ggui.StateValue[newSession]
	FormErr          *ggui.StateValue[string]

	conns map[string]*termConn // UI goroutine only
	// killed are the sessions killed here, by when, which a poll begun
	// before the kill still lists; they stay out of the list until the
	// daemon's leaves them out too. UI goroutine only.
	killed map[string]time.Time
	// lastSession is the session tab shown last, which a note from a diff
	// goes to first.
	lastSession string
	lastTap     tabTap // the last tap on a diff's tab, which a second keeps
	// revealedTab is the tab each pane last scrolled its tab bar to, so it
	// does so once per tab it comes to show.
	revealedTab map[int]string
	dialing     map[string]bool

	// recorder keeps the terminal streams the developer option records.
	recorder *streamRecorder
}

func newModel(cfg *config.Config, addr string) *model {
	go i18n.System() // asks the platform, so not on the first frame
	if addr == "" {
		// The configuration's socket, found now: requests dial it from
		// goroutines, which must not find another should MAKO_HOME change,
		// as it does when a test ends.
		addr, _ = config.SocketPath("")
	}
	p := loadPrefs()
	m := &model{
		cfg:         cfg,
		link:        newDaemonLink(addr, p.Daemon),
		Prefs:       ggui.State(p),
		Spaces:      ggui.State(spaceList{}).WithEqual(func(a, b spaceList) bool { return reflect.DeepEqual(a, b) }),
		Themes:      ggui.State(loadThemes()).WithEqual(maps.Equal),
		ThemesRead:  ggui.State(0),
		Page:        ggui.State("workspace"),
		Section:     ggui.State("general"),
		Host:        ggui.State(draftFrom(cfg)),
		AgentDraft:  ggui.State(draftAgents(cfg.Agents)).WithEqual(slices.Equal),
		HostNote:    ggui.State(""),
		RenameOpen:  ggui.State(false),
		RenameText:  ggui.State(""),
		RenameErr:   ggui.State(""),
		FindOpen:    ggui.State(false),
		FindText:    ggui.State(""),
		SearchQuery: ggui.State(""),
		Search:      ggui.State(searchView{}).WithEqual(deepEqual),
		Docs:        ggui.State(map[string]docInfo{}).WithEqual(maps.Equal),
		docs:        map[string]*document{},
		Diffs:       ggui.State(map[string]diffTab{}).WithEqual(deepEqual),
		pickers:     newPickerState(),
		ext:         newExtState(cfg),
		files:       newFilesState(),
		ws:          newWsState(),
		prefsWriter: &fileWriter{what: "settings"},
		hl:          newHighlighter(),
		DaemonState: ggui.State(daemonState{}),
		DaemonLog:   ggui.State([]string(nil)).WithEqual(slices.Equal),
		embedded:    newEmbeddedDaemon(),
		toaster:     ui.NewToaster(),
		tabNodes:    map[string]string{},
		missing:     map[string]time.Time{},
		Conn:        ggui.State(connState{}).WithEqual(deepEqual),
		Nodes:       ggui.State([]*protocol.Node(nil)).WithEqual(protocol.Equal),
		Access:      ggui.State((*protocol.AccessInfo)(nil)).WithEqual(func(a, b *protocol.AccessInfo) bool { return proto.Equal(a, b) }),
		Devices:     ggui.State(deviceList{}).WithEqual(deepEqual[deviceList]),
		AccessNote:  ggui.State(""),
		AccessKey:   ggui.State(""),
		asked:       map[string]ui.ToastID{},
		Sessions:    ggui.State([]*protocol.SessionInfo(nil)).WithEqual(protocol.Equal),
		Agents:      ggui.State(agentspec.Targets(cfg.Agents)).WithEqual(slices.Equal),
		Selected:    ggui.State(""),
		Tabs:        ggui.State([]string(nil)).WithEqual(slices.Equal),
		Layout:      ggui.State(newLayout()).WithEqual(func(a, b *layoutNode) bool { return reflect.DeepEqual(a, b) }),
		FocusGroup:  ggui.State(1),
		Dragging:    ggui.State(false),
		TabHint:     ggui.State(tabHint{}),
		Attached:    ggui.State([]string(nil)).WithEqual(slices.Equal),
		Titles:      ggui.State(map[string]string{}).WithEqual(func(a, b map[string]string) bool { return reflect.DeepEqual(a, b) }),
		Unread:      ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Printed:     ggui.State(map[string]time.Time{}).WithEqual(func(a, b map[string]time.Time) bool { return reflect.DeepEqual(a, b) }),
		Minute:      ggui.State(time.Now().Truncate(time.Minute)),
		ChatModes:   ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		chats:       map[string]*chatState{},
		chatImages:  newImageCache(),
		usages:      map[string]*usageState{},
		git:         newGitState(),
		RepoPick:    ggui.State(repoPick{}),
		// Its functions do not compare: each asking is new.
		Confirmation:  ggui.State(confirmation{}).WithEqual(func(a, b confirmation) bool { return false }),
		AgentsSplit:   ggui.State(0.4),
		SidebarOpen:   ggui.State(true),
		DetailsOpen:   ggui.State(true),
		DetailsWidth:  ggui.State(p.DetailsWidth),
		SidebarWidth:  ggui.State(p.SidebarWidth),
		DetailTab:     ggui.State(0),
		ProjectAgents: ggui.State(projectAgentsView{}).WithEqual(deepEqual),
		Forge:         newForgeState(),
		Tunnels:       newTunnelState(),
		Shell:         newShellView(),
		NewOpen:       ggui.State(false),
		Form:          ggui.State(newSession{Agent: p.DefaultAgent, Project: p.DefaultProject}),
		FormErr:       ggui.State(""),
		conns:         map[string]*termConn{},
		killed:        map[string]time.Time{},
		revealedTab:   map[int]string{},
		dialing:       map[string]bool{},
	}
	m.link.approveInApp.Store(p.ApproveInApp)
	m.recorder = newStreamRecorder(recordDir())
	m.recorder.on.Store(p.RecordStreams)
	m.Roots = ggui.Derived(m.panelRoots).WithEqual(deepEqual)
	m.spaceSet = ggui.Combine(m.Spaces, ggui.Map(m.Prefs, func(p prefs) string { return p.ShownSpace }), spaceSetOf).
		WithEqual(func(a, b spaceSet) bool { return reflect.DeepEqual(a, b) })
	m.Pick = newFolderPick(m)
	m.Proj = newProjectAsk()
	m.projectsSent = map[string][]*protocol.ProjectScope{}
	return m
}

// Lookups ------------------------------------------------------------------

// busy reports whether a session's program says it is at work: from its
// terminal here, which hears a report at once, or from its daemon, which
// heard any it sent before the terminal here attached.
func (m *model) busy(id string) bool {
	if conn := m.conns[id]; conn != nil {
		if busy, known := conn.busyState(); known {
			return busy
		}
	}
	s, ok := m.session(id)
	return ok && s.GetBusy() && s.GetState() != protocol.StateExited
}

func (m *model) session(id string) (*protocol.SessionInfo, bool) {
	for _, s := range ggui.Peek(m.Sessions) {
		if s.GetId() == id {
			return s, true
		}
	}
	return &protocol.SessionInfo{}, false
}

func nodeName(nodes []*protocol.Node, id string) string {
	if n, ok := nodeIn(nodes, id); ok {
		return n.GetName()
	}
	return short(id)
}

func nodeIn(nodes []*protocol.Node, id string) (*protocol.Node, bool) {
	for _, n := range nodes {
		if n.GetId() == id {
			return n, true
		}
	}
	return &protocol.Node{}, false
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// projectName is how a working directory is shown: its last element, as a
// repository name is.
func projectName(dir string) string {
	if dir == "" {
		return "~"
	}
	return filepath.Base(dir)
}

// prettyPath shortens the home directory to ~.
func prettyPath(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 2 || rel[:2] != "..") {
			if rel == "." {
				return "~"
			}
			return filepath.Join("~", rel)
		}
	}
	return dir
}

// Actions -------------------------------------------------------------------

// selectSession shows a session: in the pane that has it as a tab, or as a
// new tab of the current pane, attaching its terminal if it is not already.
func (m *model) selectSession(id string) {
	if id == "" {
		return
	}
	// A space that keeps tabs by owner or project shows the session's.
	if s, ok := m.session(id); ok {
		m.showContext(s)
	}
	m.showTab(id)
}

// attachVisible resumes only the active tab of each visible pane. A
// terminal whose stream failed is dialed again too, while its session has
// not exited: a poll that loses the daemon closes the client, and with it
// every stream, and those tabs would otherwise stay disconnected until
// each is clicked.
func (m *model) attachVisible() {
	for _, group := range ggui.Peek(m.Layout).groups() {
		id := group.Active
		if id == "" {
			continue
		}
		if conn := m.conns[id]; conn == nil {
			m.attach(id)
		} else if conn.failed() && m.sessionLive(id) {
			m.attach(id)
		}
	}
}

// sessionLive reports whether the session of tab id is known and has not
// exited.
func (m *model) sessionLive(id string) bool {
	s, ok := m.session(id)
	return ok && s.GetState() != protocol.StateExited
}

// attach dials a session's terminal on a worker.
func (m *model) attach(id string) {
	if m.dialing[id] || !isSessionTab(id) {
		return
	}
	if conn := m.conns[id]; conn != nil {
		if !conn.failed() {
			return
		}
		m.dropConn(id)
		m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(tab string) bool { return tab == id }))
	}
	info, ok := m.session(id)
	if !ok {
		return
	}
	m.dialing[id] = true
	post, wake := m.post, m.wake
	n, _ := m.nodeInfo(info.GetNodeId())
	goos := n.GetOs()
	tap := m.recorder.tap(id, info.GetAgent(), goos)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		var conn *termConn
		if err == nil {
			conn, err = dialTerm(context.Background(), c, info, goos, tap, wake, func() { post(m.syncTitles) })
		}
		if conn != nil && m.window != nil {
			// Output repaints the terminal alone, not the window.
			repaint := m.window.RepaintRect
			conn.repaint.Store(&repaint)
		}
		post(func() {
			delete(m.dialing, id)
			if err != nil {
				m.Conn.Set(connState{OK: ggui.Peek(m.Conn).OK, Self: ggui.Peek(m.Conn).Self, Err: err.Error()})
				return
			}
			if !slices.Contains(ggui.Peek(m.Tabs), id) {
				conn.close() // the tab closed while dialing
				return
			}
			m.conns[id] = conn
			m.syncTitles()
			m.Attached.Set(append(slices.Clone(ggui.Peek(m.Attached)), id))
		})
	}()
}

// closeTab detaches a session's tab, closing its pane if it was the last
// there; the session keeps running.
func (m *model) closeTab(id string) {
	if !slices.Contains(ggui.Peek(m.Tabs), id) {
		return
	}
	m.editLayout(func(root *layoutNode) int {
		root.remove(id)
		return 0
	})
	m.forgetTab(id)
}

// forgetTab lets go of what the window holds for a tab no layout has: its
// terminal, with the stream and goroutine reading it, its chat and usage,
// and its view.
func (m *model) forgetTab(id string) {
	m.forgetTabs(func(t string) bool { return t == id })
}

// forgetClosedTabs forgets every tab that is in no layout, shown or not.
// A tab leaves a space not shown, or with a layout another node gave,
// without closeTab, which closes only the tabs shown.
func (m *model) forgetClosedTabs() {
	open := map[string]bool{}
	for _, id := range m.allTabs() {
		open[id] = true
	}
	m.forgetTabs(func(id string) bool { return !open[id] })
}

// forgetTabs forgets the tabs gone says are, setting the window's state
// once for them all.
func (m *model) forgetTabs(gone func(id string) bool) {
	for id := range m.conns {
		if gone(id) {
			m.dropConn(id)
		}
	}
	maps.DeleteFunc(m.chats, func(id string, _ *chatState) bool { return gone(id) })
	maps.DeleteFunc(m.usages, func(id string, _ *usageState) bool { return gone(id) })
	for id := range ggui.Peek(m.ChatModes) {
		if gone(id) {
			modes := maps.Clone(ggui.Peek(m.ChatModes))
			maps.DeleteFunc(modes, func(id string, _ bool) bool { return gone(id) })
			m.ChatModes.Set(modes)
			break
		}
	}
	if attached := ggui.Peek(m.Attached); slices.ContainsFunc(attached, gone) {
		m.Attached.Set(slices.DeleteFunc(slices.Clone(attached), gone))
	}
}

// openNew shows the new-session dialog, prefilled for a node and project.
func (m *model) openNew(nodeID, project string) {
	p := ggui.Peek(m.Prefs)
	f := newSession{Agent: p.DefaultAgent, NodeID: nodeID, Project: project}
	if prev := ggui.Peek(m.Form); prev.Agent != "" {
		f.Agent = prev.Agent // the last choice, within one run
	}
	if f.NodeID == "" {
		f.NodeID = ggui.Peek(m.Conn).Self.GetId()
	}
	if f.Project == "" {
		f.Project = m.launchProject(f.NodeID)
	}
	m.Form.Set(f)
	m.FormErr.Set("")
	m.NewOpen.Set(true)
}

// launch starts a session directly, as the tab bar's quick buttons do.
func (m *model) launch(agent, nodeID, project string) {
	m.Form.Set(newSession{Agent: agent, NodeID: nodeID, Project: project})
	m.submitNew()
}

// submitNew starts the session the dialog describes.
func (m *model) submitNew() {
	// Without a working directory the node starts the session in its home.
	f := ggui.Peek(m.Form)
	nodeID := m.remoteID(f.NodeID)
	agent, profile := agentspec.SplitTarget(f.Agent)
	req := &protocol.OpenSession{NodeId: nodeID, Agent: agent, Profile: profile, Project: f.Project, Cols: 120, Rows: 36}
	if f.Worktree {
		branch := strings.TrimSpace(f.Branch)
		if branch == "" {
			m.FormErr.Set(T("Name the branch the worktree is made on."))
			return
		}
		req.Worktree = &protocol.Worktree{Branch: branch, Base: f.Base, Start: f.Start}
	}
	ggui.Async(func() (*protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Open(ctx, req)
	}, func(info *protocol.SessionInfo, err error) {
		if err != nil {
			m.FormErr.Set(err.Error())
			m.NewOpen.Set(true)
			return
		}
		m.NewOpen.Set(false)
		sessions := slices.Clone(ggui.Peek(m.Sessions))
		m.Sessions.Set(append(sessions, info))
		m.selectSession(info.GetId())
		if f.Prompt != "" {
			m.setChatMode(info.GetId(), true)
			m.setComposerDraft(info.GetId(), f.Prompt)
		}
	})
}

// syncTitles publishes the titles programs set on their terminals.
func (m *model) syncTitles() {
	titles := map[string]string{}
	for _, info := range ggui.Peek(m.Sessions) {
		if title := protocol.BareTitle(info.GetTitle()); title != "" {
			titles[info.GetId()] = title
		}
	}
	for id, conn := range m.conns {
		if title, known := conn.windowTitle(); known {
			title = protocol.BareTitle(title)
			// Even an empty live title overrides an older polling result.
			if title == "" {
				delete(titles, id)
			} else {
				titles[id] = title
			}
		}
	}
	m.Titles.Set(titles)
}

// themePalette is the palette the theme preference picks; it subscribes
// the caller.
func (m *model) themePalette() skin.Palette {
	return skin.Pick(m.Prefs.Get().Theme, ggui.SystemDark().Get(), m.Themes.Get())
}

// reloadThemes reads the user's themes from disk again, and starts the
// editor again on them if they changed.
func (m *model) reloadThemes() {
	themes := loadThemes()
	if maps.Equal(themes, ggui.Peek(m.Themes)) {
		return
	}
	m.Themes.Set(themes)
	m.ThemesRead.Update(func(n int) int { return n + 1 })
}

// openSettings shows the settings page, at a section when one is named.
func (m *model) openSettings(section string) {
	if section != "" {
		m.Section.Set(section)
	}
	m.Host.Set(draftFrom(m.cfg))
	m.HostNote.Set("")
	m.reloadThemes()
	m.Page.Set("settings")
}

// shutdown detaches every terminal, hangs up, and stops the app's daemon
// with the sessions it runs.
func (m *model) shutdown() {
	if m.ws.timer != nil {
		m.ws.timer.Stop()
	}
	m.ws.writer.wait()
	m.prefsWriter.wait()
	defer m.embedded.stop()
	defer m.hl.close()
	defer m.closeTunnels()
	m.dropConns()
	m.link.hangUp()
}
