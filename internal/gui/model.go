package gui

import (
	"cmp"
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
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
	// Prompt is the agent's first prompt.
	Prompt string
	// Team is the name of the team the agent leads, started with it; ""
	// for none.
	Team string
}

// model is the GUI's state. Signals are touched on the UI goroutine only;
// workers hand results back through post.
type model struct {
	cfg  *config.Config
	post func(func())
	// host is the app's, or a probe's, which shows the notifications;
	// notifyFailed says once why one could not be shown.
	host         ggui.Host
	notifyFailed sync.Once
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
	// WorkspaceSync is whether this node shares its workspace with its
	// peers: its daemon's setting (config.Workspace), which Settings
	// changes.
	WorkspaceSync       *ggui.StateValue[bool]
	IsolateHome         *ggui.StateValue[bool]
	AgentDraft          *ggui.StateValue[[]agentDraft]
	AgentCatalogs       *ggui.StateValue[map[string][]*protocol.AgentModel]
	AgentCatalogLoading *ggui.StateValue[map[string]bool]
	catalogAsked        map[string]bool // UI goroutine only
	HomeMigrate         *homeMigrateState
	HostNote            *ggui.StateValue[string]
	RenameOpen          *ggui.StateValue[bool]
	RenameText          *ggui.StateValue[string]
	RenameErr           *ggui.StateValue[string]
	renaming            string                   // the session RenameOpen names
	FindOpen            *ggui.StateValue[bool]   // the find bar is over the selected session; see find.go
	FindText            *ggui.StateValue[string] // the words it finds
	findFocus           bool                     // the find bar's field takes the focus as it is painted
	quitting            bool
	quitAsked           bool       // a quit, not the close button, is closing the window; see tray.go
	tray                *ggui.Tray // the menu bar's icon, in tray mode
	badge               string     // the count of sessions waiting, as the Dock's badge shows it

	pickers *pickerState      // the command palette and quick open; see pickers.go
	toaster *ui.ToasterWidget // notices over the window, such as a dropped file sent
	// The Search tab: what is typed, what was found, and whether its field
	// is to take the keys when it next shows.
	SearchQuery *ggui.StateValue[string]
	SearchOpen  *ggui.StateValue[bool]
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
	titleCache  *titleCache              // titles.json; see titlecache.go
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

	Selected      *ggui.StateValue[string]      // the tab the current pane shows
	Tabs          *ggui.StateValue[[]string]    // every pane's tabs, in order
	Layout        *ggui.StateValue[*layoutNode] // the panes; see layout.go
	FocusGroup    *ggui.StateValue[int]         // the current pane
	Dragging      *ggui.StateValue[bool]        // a tab is being dragged
	StackDragging *ggui.StateValue[bool]        // a right-panel / stack tab is being dragged
	DragItem      *ggui.StateValue[tabDrag]     // tab or project chip currently dragged from a tab bar
	DragGhost     *ggui.StateValue[*dragGhost]  // floating preview of the dragged tab or chip
	ChipHeld      *ggui.StateValue[string]      // foldKey of a project chip currently held down
	TabHint       *ggui.StateValue[tabHint]     // where in a tab bar it would drop
	Attached      *ggui.StateValue[[]string]    // tabs whose terminal is connected
	Titles        *ggui.StateValue[map[string]string]
	// Unread are the sessions that finished or asked out of view; see
	// attention.go.
	Unread *ggui.StateValue[map[string]bool]
	// seeing is the attention, by session, the window last told a daemon
	// the user saw, so it tells each once; UI goroutine only.
	seeing map[string]uint64
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
	// plugins are the daemon's plugins; see plugins.go.
	plugins   *pluginsState
	providers *providersState // decision-model & API keys; see providers.go
	// store is the plugin store's: the registries' listings and the
	// registries themselves; see pluginstore.go.
	store *storeState
	// AgentBlocks are the blocks the daemon's agents show, and winBase the
	// document the window draws them over; see agentblocks.go.
	AgentBlocks *ggui.StateValue[[]*protocol.AgentBlock]
	winBase     *layoutdoc.Doc

	diffChanges *diffChangesState // open diff tabs' change lists; see diffchanges.go
	RepoPick    *ggui.StateValue[repoPick]
	// Roots are the folders the Files, Git and forge tabs show; see
	// panelRoots.
	Roots *ggui.DerivedValue[panelRoots] // the project's folder the tab shows; see repoFolder
	// dark is whether the theme shown is dark, for what changes with that
	// alone, as a page's diagrams: not every setting the theme is read from.
	dark *ggui.DerivedValue[bool]
	// openChat says the settings open an agent's tab in its chat.
	openChat ggui.Readable[bool]
	// An operation that loses work, asking to be sure; see confirm.go.
	Confirmation *ggui.StateValue[confirmation]
	AgentsSplit  *ggui.StateValue[float64] // the agents panel's agents over its messages

	SidebarOpen *ggui.StateValue[bool]
	DetailsOpen *ggui.StateValue[bool]
	// DetailsWidth is the right panel's width, following a drag of its
	// edge; the settings keep it when the drag ends.
	DetailsWidth *ggui.StateValue[float64]
	SidebarWidth *ggui.StateValue[float64] // the sidebar's, as DetailsWidth
	SidebarSplit *ggui.StateValue[float64] // the sidebar's projects' height, as SidebarWidth
	// Scope is the project, or folder of one, picked in the sidebar for
	// the agents it lists; see sidebartree.go.
	Scope *ggui.StateValue[sidebarScope]
	// DetailTab is the block the right panel, the details stack, shows:
	// one of filesTab, gitTab, agentsTab and forgeTab in the default
	// layout.
	DetailTab *ggui.StateValue[string]
	// WinDoc is the window's layout document: the one the space shown
	// uses, or a change of it an agent asked for, which the daemon holds
	// staged, LayoutStage (winsync.go). blocks are what it can place, and
	// winStates its nodes' state on this device, by ID (winstate.go). UI
	// goroutine only. winDocBody is the JSON WinDoc was read from, and
	// winDocBad that of the last that could not be read, told once.
	WinDoc           *ggui.StateValue[*layoutdoc.Doc]
	blocks           *blockRegistry
	winStates        map[string]*nodeState
	winDocBody       string
	winDocBad        string
	LayoutStage      *ggui.StateValue[*protocol.LayoutStage]
	ProjectAgents    *ggui.StateValue[projectAgentsView]
	projectAgentsGen uint64
	Forge            forgeState                          // the GitHub tab, and the watch on the branch
	Tunnels          *tunnelState                        // ports of other nodes forwarded here
	Pick             *folderPick                         // the New session dialog's folder picker
	Proj             *projectAsk                         // the project dialogs; see projectmenu.go
	projectsSent     map[string][]*protocol.ProjectScope // by node, the projects its daemon was last told; UI goroutine only
	Shell            *shellView                          // a background command shown in full; see tasks.go
	// launches are the new sessions' tabs' states, by tab; launchSeq
	// numbers the next, and lastAgent is the agent the last one started.
	// UI goroutine only.
	launches  map[string]*launchState
	launchSeq int
	lastAgent string

	conns map[string]*termConn // UI goroutine only
	// killed are the sessions killed here, by when, which a poll begun
	// before the kill still lists; they stay out of the list until the
	// daemon's leaves them out too. UI goroutine only.
	killed map[string]time.Time
	// lastSession is the session tab shown last, which a note from a diff
	// goes to first.
	lastSession string
	lastTap     tabTap // the last tap on a diff's or file's tab, which a second keeps
	// revealedTab is the tab each pane last scrolled its tab bar to, so it
	// does so once per tab it comes to show.
	revealedTab map[int]string
	// tabEases is each pane's tabs' widths, easing as they come and go.
	tabEases map[int]*tabEase
	// FoldedTabs are the projects' runs of tabs folded away under their
	// labels, by pane and project (foldKey); hiddenTabs how many of each
	// pane's tabs are out of sight, scrolled or folded away.
	FoldedTabs *ggui.StateValue[map[string]bool]
	hiddenTabs map[int]*ggui.StateValue[int]
	dialing    map[string]bool
	redials    map[string]redial // see redialDue
	sleeps     sleeps            // see sleepIdle

	// Multi-window state: detached split panes (ephemeral view-only; never
	// persisted into m.Layout or workspace.json) and independent Space windows.
	DetachedPanes *ggui.StateValue[map[paneRef]bool]
	SpaceWindows  *ggui.StateValue[map[string]bool]
	paneWindows   map[paneRef]*ggui.Window
	spaceWindows  map[string]*spaceWindowState
	wsRev         *ggui.StateValue[uint64]
	secWinMu      sync.RWMutex
	secWins       map[*ggui.Window]struct{}

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
	blocks := builtinBlocks()
	doc := layoutdoc.Default()
	m := &model{
		cfg:                 cfg,
		link:                newDaemonLink(addr, p.Daemon),
		Prefs:               ggui.State(p),
		Spaces:              ggui.State(spaceList{}).WithEqual(func(a, b spaceList) bool { return reflect.DeepEqual(a, b) }),
		Themes:              ggui.State(loadThemes()).WithEqual(maps.Equal),
		ThemesRead:          ggui.State(0),
		Page:                ggui.State("workspace"),
		Section:             ggui.State("general"),
		Host:                ggui.State(draftFrom(cfg)),
		WorkspaceSync:       ggui.State(workspaceSyncs(cfg)),
		IsolateHome:         ggui.State(cfg.IsolateHome),
		AgentDraft:          ggui.State(draftAgents(cfg.Agents)).WithEqual(slices.Equal),
		AgentCatalogs:       ggui.State(map[string][]*protocol.AgentModel{}).WithEqual(equalCatalogMaps),
		AgentCatalogLoading: ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		catalogAsked:        map[string]bool{},
		HomeMigrate:         newHomeMigrateState(),
		HostNote:            ggui.State(""),
		RenameOpen:          ggui.State(false),
		RenameText:          ggui.State(""),
		RenameErr:           ggui.State(""),
		FindOpen:            ggui.State(false),
		FindText:            ggui.State(""),
		SearchQuery:         ggui.State(""),
		SearchOpen:          ggui.State(false),
		Search:              ggui.State(searchView{}).WithEqual(deepEqual),
		Docs:                ggui.State(map[string]docInfo{}).WithEqual(maps.Equal),
		docs:                map[string]*document{},
		Diffs:               ggui.State(map[string]diffTab{}).WithEqual(deepEqual),
		pickers:             newPickerState(),
		ext:                 newExtState(cfg),
		plugins:             newPluginsState(),
		providers:           newProvidersState(),
		store:               newStoreState(),
		AgentBlocks:         ggui.State([]*protocol.AgentBlock(nil)),
		files:               newFilesState(),
		ws:                  newWsState(),
		prefsWriter:         &fileWriter{what: "settings"},
		titleCache:          loadTitleCache(time.Now()),
		hl:                  newHighlighter(),
		DaemonState:         ggui.State(daemonState{}),
		DaemonLog:           ggui.State([]string(nil)).WithEqual(slices.Equal),
		embedded:            newEmbeddedDaemon(),
		toaster:             ui.NewToaster(),
		tabNodes:            map[string]string{},
		missing:             map[string]time.Time{},
		Conn:                ggui.State(connState{}).WithEqual(deepEqual),
		Nodes:               ggui.State([]*protocol.Node(nil)).WithEqual(protocol.Equal),
		Access:              ggui.State((*protocol.AccessInfo)(nil)).WithEqual(func(a, b *protocol.AccessInfo) bool { return proto.Equal(a, b) }),
		Devices:             ggui.State(deviceList{}).WithEqual(deepEqual[deviceList]),
		AccessNote:          ggui.State(""),
		AccessKey:           ggui.State(""),
		asked:               map[string]ui.ToastID{},
		Sessions:            ggui.State([]*protocol.SessionInfo(nil)).WithEqual(protocol.Equal),
		Agents:              ggui.State(agentspec.Targets(cfg.Agents)).WithEqual(slices.Equal),
		Selected:            ggui.State(""),
		Tabs:                ggui.State([]string(nil)).WithEqual(slices.Equal),
		Layout:              ggui.State(newLayout()).WithEqual(func(a, b *layoutNode) bool { return reflect.DeepEqual(a, b) }),
		FocusGroup:          ggui.State(1),
		Dragging:            ggui.State(false),
		StackDragging:       ggui.State(false),
		DragItem:            ggui.State(tabDrag{}).WithEqual(tabDragEqual),
		DragGhost:           ggui.State((*dragGhost)(nil)),
		ChipHeld:            ggui.State(""),
		TabHint:             ggui.State(tabHint{}),
		Attached:            ggui.State([]string(nil)).WithEqual(slices.Equal),
		Titles:              ggui.State(map[string]string{}).WithEqual(func(a, b map[string]string) bool { return reflect.DeepEqual(a, b) }),
		Unread:              ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Printed:             ggui.State(map[string]time.Time{}).WithEqual(func(a, b map[string]time.Time) bool { return reflect.DeepEqual(a, b) }),
		Minute:              ggui.State(time.Now().Truncate(time.Minute)),
		ChatModes:           ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		chats:               map[string]*chatState{},
		chatImages:          newImageCache(),
		usages:              map[string]*usageState{},
		diffChanges:         newDiffChangesState(),
		RepoPick:            ggui.State(repoPick{}),
		// Its functions do not compare: each asking is new.
		Confirmation:  ggui.State(confirmation{}).WithEqual(func(a, b confirmation) bool { return false }),
		AgentsSplit:   ggui.State(0.4),
		SidebarOpen:   ggui.State(true),
		DetailsOpen:   ggui.State(true),
		DetailsWidth:  ggui.State(float64(detailsWidth)), // as the document and the settings say, below
		SidebarWidth:  ggui.State(float64(sidebarWidth)),
		SidebarSplit:  ggui.State(p.SidebarSplit),
		Scope:         ggui.State(sidebarScope{}),
		DetailTab:     ggui.State(""),
		WinDoc:        ggui.State(doc),
		blocks:        blocks,
		LayoutStage:   ggui.State((*protocol.LayoutStage)(nil)).WithEqual(func(a, b *protocol.LayoutStage) bool { return proto.Equal(a, b) }),
		ProjectAgents: ggui.State(projectAgentsView{}).WithEqual(deepEqual),
		Forge:         newForgeState(),
		Tunnels:       newTunnelState(),
		Shell:         newShellView(),
		launches:      map[string]*launchState{},
		conns:         map[string]*termConn{},
		killed:        map[string]time.Time{},
		revealedTab:   map[int]string{},
		tabEases:      map[int]*tabEase{},
		FoldedTabs:    ggui.State(map[string]bool{}).WithEqual(maps.Equal[map[string]bool]),
		hiddenTabs:    map[int]*ggui.StateValue[int]{},
		dialing:       map[string]bool{},
		redials:       map[string]redial{},
		DetachedPanes: ggui.State(map[paneRef]bool{}).WithEqual(maps.Equal[map[paneRef]bool]),
		SpaceWindows:  ggui.State(map[string]bool{}).WithEqual(maps.Equal[map[string]bool]),
		paneWindows:   map[paneRef]*ggui.Window{},
		spaceWindows:  map[string]*spaceWindowState{},
		wsRev:         ggui.State(uint64(0)),
		secWins:       map[*ggui.Window]struct{}{},
	}
	m.winStates = m.builtinNodeStates()
	m.drawWindowDoc(doc)
	m.link.setApproveInApp(p.ApproveInApp)
	m.recorder = newStreamRecorder(recordDir())
	m.recorder.on.Store(p.RecordStreams)
	m.Roots = ggui.Derived(m.panelRoots).WithEqual(deepEqual)
	m.dark = ggui.Derived(func() bool { return m.themePalette().Dark })
	m.openChat = ggui.Map(m.Prefs, func(p prefs) bool { return p.OpenView == openChat })
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
	// The space shows the tabs of the session's project, and the
	// sidebar's agents follow to it from another project.
	if s, ok := m.session(id); ok {
		m.showContext(s)
		m.followScope(s)
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
		conn := m.conns[id]
		switch {
		case conn != nil && !conn.failed():
			m.heldUp(id)
		case conn != nil && !m.sessionLive(id):
			// exited: its last screen stays
		default:
			if _, ok := m.session(id); ok && m.redialDue(id) {
				m.attach(id)
			}
		}
	}
	m.attachVisibleMultiWindow()
}

// A terminal dialed again and again, as its stream fails as soon as it
// is made or the daemon is not there, is dialed less and less often: the
// wait after each try doubles from redialMin to redialMax, and starts over
// once one has held for redialMax. Showing the tab dials it at once.
const (
	redialMin = pollInterval
	redialMax = time.Minute
)

// redial is when a tab's terminal was last dialed of itself, and the
// wait before the next.
type redial struct {
	next  time.Time
	delay time.Duration
}

// redialDue reports whether tab id may be dialed again now, noting the try
// if it may.
func (m *model) redialDue(id string) bool {
	r, now := m.redials[id], time.Now()
	if now.Before(r.next) {
		return false
	}
	r.delay = min(max(2*r.delay, redialMin), redialMax)
	r.next = now.Add(r.delay)
	m.redials[id] = r
	return true
}

// heldUp notes that tab id's terminal holds, starting its waits over once
// it has for redialMax past the last.
func (m *model) heldUp(id string) {
	if r, ok := m.redials[id]; ok && time.Since(r.next) >= redialMax {
		delete(m.redials, id)
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
	if m.post == nil || m.dialing[id] || !isSessionTab(id) {
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
			// Output repaints the terminal alone, not the window (plus any
			// secondary windows that may host detached panes or spaces).
			repaint := m.repaintTerminalRect
			conn.repaint.Store(&repaint)
		}
		post(func() {
			delete(m.dialing, id)
			if err != nil {
				m.connErr(err)
				return
			}
			if !slices.Contains(m.allTabs(), id) {
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
	maps.DeleteFunc(m.launches, func(id string, _ *launchState) bool { return gone(id) })
	maps.DeleteFunc(m.usages, func(id string, _ *usageState) bool { return gone(id) })
	maps.DeleteFunc(m.redials, func(id string, _ redial) bool { return gone(id) })
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

// openNew opens another new session's tab, prefilled for a node and
// project, and gives its state.
func (m *model) openNew(nodeID, project string) *launchState {
	if nodeID == "" && project == "" {
		if n, d, ok := m.scopeLocation(); ok {
			nodeID, project = n, d
		}
	}
	f := newSession{Agent: cmp.Or(m.lastAgent, ggui.Peek(m.Prefs).DefaultAgent), NodeID: nodeID, Project: project}
	if f.NodeID == "" {
		f.NodeID = ggui.Peek(m.Conn).Self.GetId()
	}
	if f.Project == "" {
		f.Project = m.launchProject(f.NodeID)
	}
	if f.Project != "" {
		if pl := placeIn(m.projectsNow(), f.NodeID, f.Project, "", ""); pl.At >= 0 {
			m.showProjectTabs(m.projectsNow()[pl.At].ID)
		}
	}
	_, l := m.newLaunch(f)
	return l
}

// launching reports whether a new session's tab is open.
func (m *model) launching() bool { return slices.ContainsFunc(ggui.Peek(m.Tabs), isLaunchTab) }

// launch starts a session directly, as the tab bar's quick buttons do.
func (m *model) launch(agent, nodeID, project string) {
	m.startSession("", newSession{Agent: agent, NodeID: nodeID, Project: project})
}

// submitLaunch starts the session new session's tab id describes.
func (m *model) submitLaunch(id string) {
	l := m.launchOf(id)
	if ggui.Peek(l.Starting) {
		return
	}
	m.startSession(id, ggui.Peek(l.Form))
}

// startSession starts the session f describes, with its prompt and its
// team: in the place of new session's tab tab, or of none, in a tab of
// its own. A session that does not start says why in its tab, opened for
// one started from none.
func (m *model) startSession(tab string, f newSession) {
	fail := func(why string) {
		if tab == "" || m.launches[tab] == nil {
			tab, _ = m.newLaunch(f)
		}
		l := m.launchOf(tab)
		l.Starting.Set(false)
		l.Err.Set(why)
	}
	if tab != "" {
		l := m.launchOf(tab)
		l.Err.Set("")
		l.Starting.Set(true)
	}
	// Without a working directory the node starts the session in its home.
	nodeID := m.remoteID(f.NodeID)
	agent, profile := agentspec.SplitTarget(f.Agent)
	prompt := strings.TrimSpace(f.Prompt)
	req := &protocol.OpenSession{NodeId: nodeID, Agent: agent, Profile: profile, Project: f.Project, Prompt: prompt, Cols: 120, Rows: 36}
	var team agentTeam // the one the agent leads
	if agent != "shell" {
		if t, ok := m.teamNamed(f.Team); ok && len(t.Members) > 0 {
			team, req.Prompt = t, strings.TrimSpace(teamBrief(t)+"\n"+prompt)
		}
	}
	if f.Worktree {
		branch := strings.TrimSpace(f.Branch)
		if branch == "" {
			fail(T("Name the branch the worktree is made on."))
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
			fail(err.Error())
			return
		}
		m.lastAgent = f.Agent
		sessions := slices.Clone(ggui.Peek(m.Sessions))
		m.Sessions.Set(append(sessions, info))
		inMain := slices.Contains(ggui.Peek(m.Tabs), tab)
		otherKey := ""
		if tab != "" && !inMain {
			otherKey = m.findLayoutOfTab(tab)
		}
		if otherKey != "" {
			// Launched from a secondary Space window or detached pane:
			// replace the launch tab in-place without hijacking the main window.
			m.editLayoutForKey(otherKey, func(root *layoutNode) int {
				g := root.groupOf(tab)
				if g == nil {
					return 0
				}
				i := slices.Index(g.Tabs, tab)
				g.Tabs[i] = info.GetId()
				g.Active = info.GetId()
				return g.ID
			})
			delete(m.launches, tab)
			m.attach(info.GetId())
		} else {
			targetKey := layoutKey(m.spacesNow().spaceID(m.spacesNow().Space), projectContext(sectionID(m.projectsNow(), info)))
			if inMain {
				if m.ws.layoutKey == targetKey {
					m.replaceTab(tab, info.GetId())
				} else {
					m.closeTab(tab)
				}
			}
			delete(m.launches, tab)
			m.selectSession(info.GetId())
		}
		if len(team.Members) > 0 {
			m.startTeam(info.GetId(), team, prompt)
		}
		if agent != "shell" && req.GetPrompt() != "" {
			m.setChatMode(info.GetId(), true)
		}
	})
}

// syncTitles publishes the titles programs set on their terminals.
func (m *model) syncTitles() {
	titles := map[string]string{}
	sessions := ggui.Peek(m.Sessions)
	ids := make([]string, len(sessions))
	for i, info := range sessions {
		ids[i] = info.GetId()
		if title := protocol.BareTitle(info.GetTitle()); title != "" {
			titles[info.GetId()] = title
		}
	}
	live := map[string]bool{}
	for id, conn := range m.conns {
		if title, known := conn.windowTitle(); known {
			title = protocol.BareTitle(title)
			live[id] = true
			// Even an empty live title overrides an older polling result.
			if title == "" {
				delete(titles, id)
			} else {
				titles[id] = title
			}
		}
	}
	m.Titles.Set(m.titleCache.merge(titles, live, ids, time.Now()))
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
		m.Section.Set(canonicalSettingsSection(section))
	}
	m.Host.Set(draftFrom(m.cfg))
	m.HostNote.Set("")
	m.reloadThemes()
	m.Page.Set("settings")
}

// shutdown detaches every terminal, hangs up, and stops the app's daemon
// with the sessions it runs.
func (m *model) shutdown() {
	m.closeAllSecondaryWindows()
	m.embedded.stop()
	if m.ws.timer != nil {
		m.ws.timer.Stop()
	}
	if m.ws.give != nil {
		// The last edit, which the pause cut short, is written down, and
		// given at the next start.
		m.writeWorkspace()
	}
	m.ws.writer.wait()
	m.prefsWriter.wait()
	m.titleCache.flush(time.Now())
	m.titleCache.writer.wait()
	m.hl.close()
	m.closeTunnels()
	m.dropConns()
	m.link.hangUp()
}
