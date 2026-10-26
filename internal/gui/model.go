package gui

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/mcp"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/zed"
)

// pollInterval is how often the mesh's nodes and sessions are refreshed.
const pollInterval = 2 * time.Second

// connState describes the link to the daemon the GUI talks through.
type connState struct {
	OK   bool
	Err  string
	Self protocol.Node
}

// newSession is the new-session dialog's form.
type newSession struct {
	Agent   string // a target: agent, or agent:profile
	NodeID  string
	Project string
	// Worktree starts the session in a new git worktree, on Branch.
	Worktree bool
	Branch   string
}

// model is the GUI's state. Signals are touched on the UI goroutine only;
// workers hand results back through post.
type model struct {
	cfg  *config.Config
	addr string // the -daemon flag; Prefs.Daemon overrides it
	post func(func())

	Prefs          *ggui.StateValue[prefs]
	Page           *ggui.StateValue[string] // "workspace" or "settings"
	Section        *ggui.StateValue[string] // the settings page shown
	Host           *ggui.StateValue[hostDraft]
	AgentDraft     *ggui.StateValue[[]agentDraft]
	HostNote       *ggui.StateValue[string]
	ConfirmOpen    *ggui.StateValue[bool]
	RenameOpen     *ggui.StateValue[bool]
	RenameText     *ggui.StateValue[string]
	RenameErr      *ggui.StateValue[string]
	renaming       string // the session RenameOpen names
	confirmKill    string // the session ConfirmOpen asks about
	StopOpen       *ggui.StateValue[bool]
	pendingStop    func() // what StopOpen confirms
	DiscardOpen    *ggui.StateValue[bool]
	HunkOpen       *ggui.StateValue[bool] // asks before a diff's hunk is undone
	pendingHunk    func()                 // what HunkOpen confirms
	confirmDiscard string                 // the file tab DiscardOpen asks about
	QuitOpen       *ggui.StateValue[bool] // asks before quitting with unsaved files
	quitting       bool

	// The command palette, and the extensions it opens over the window;
	// see palette.go. Each opening gives palette the commands of the
	// moment.
	PaletteOpen    *ggui.StateValue[bool]
	PaletteQuery   *ggui.StateValue[string]
	palette        *ui.CommandWidget
	ExtensionsOpen *ggui.StateValue[bool]
	// Quick open, and the files it last read; see search.go.
	QuickOpen  *ggui.StateValue[bool]
	QuickQuery *ggui.StateValue[string]
	quick      *ui.CommandWidget
	quickFiles quickFiles
	// The Search tab: what is typed, what was found, and whether its field
	// is to take the keys when it next shows.
	SearchQuery *ggui.StateValue[string]
	Search      *ggui.StateValue[searchView]
	searchFocus bool

	DaemonState *ggui.StateValue[daemonState]
	DaemonLog   *ggui.StateValue[[]string]
	embedded    *embeddedDaemon
	daemonGen   int // UI goroutine only; see startEmbedded

	Conn     *ggui.StateValue[connState]
	Nodes    *ggui.StateValue[[]protocol.Node]
	Sessions *ggui.StateValue[[]protocol.SessionInfo]
	Agents   *ggui.StateValue[[]string] // what this node can launch, as agent:profile targets

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
	// Printed is when each session last printed, to the minute, kept apart
	// from Sessions so a busy program does not redraw every list of them;
	// Minute moves on each minute, so the times shown age.
	Printed   *ggui.StateValue[map[string]time.Time]
	Minute    *ggui.StateValue[time.Time]
	window    *ggui.Window                      // nil under a probe
	ChatModes *ggui.StateValue[map[string]bool] // true shows chat; terminal stays attached
	chats     map[string]*chatState

	// The Files tab: folders read, by dirKey; those open; the row chosen;
	// and the file shown.
	Dirs      *ggui.StateValue[map[string]dirListing]
	FilesOpen *ggui.StateValue[map[string]bool]
	FileSel   *ggui.StateValue[string]
	Preview   *ggui.StateValue[filePreview]
	// A file operation asked of the user, the name typed for it, why it
	// failed, and the dialogs asking; FileNote shows a failure under the
	// Files header once its dialog has closed.
	FileAsk        *ggui.StateValue[fileAsk]
	FileName       *ggui.StateValue[string]
	FileErr        *ggui.StateValue[string]
	FileNote       *ggui.StateValue[string]
	FileNameOpen   *ggui.StateValue[bool]
	FileDeleteOpen *ggui.StateValue[bool]

	// The files open in tabs, by tab ID; see docs.go.
	Docs *ggui.StateValue[map[string]docInfo]
	// Diffs are the diffs open in tabs, by tab.
	Diffs *ggui.StateValue[map[string]diffTab]
	docs  map[string]*document // UI goroutine only

	// Highlighting, and the Zed extensions it takes languages from; see
	// highlight.go. langGen counts the changes to the languages.
	hl         *highlighter
	Extensions *ggui.StateValue[[]zed.Extension]
	// extLanguages names each extension's languages, by its ID, as read
	// with Extensions.
	extLanguages map[string][]string
	langGen      int
	ExtResults   *ggui.StateValue[extSearch]
	ExtBusy      *ggui.StateValue[map[string]bool] // extensions being installed
	ExtErr       *ggui.StateValue[string]
	// MCPServers mirrors cfg.ContextServers for the settings page, and
	// MCPInfo is what the extensions say about setting each up.
	MCPServers *ggui.StateValue[map[string]mcp.Server]
	MCPInfo    *ggui.StateValue[map[string]mcpInfo]
	MCPNote    *ggui.StateValue[string]
	mcpTexts   map[string]*ggui.StateValue[string]

	// The Changes tab: the project's changes, the folders closed in their
	// tree, and the row chosen.
	Git       *ggui.StateValue[gitView]
	GitClosed *ggui.StateValue[map[string]bool]
	GitSel    *ggui.StateValue[string]
	// The files left out of the next commit, its message, and whether it
	// amends the last; the operation under way and what the last said.
	GitSkip    *ggui.StateValue[map[string]bool]
	GitMessage *ggui.StateValue[string]
	GitAmend   *ggui.StateValue[bool]
	GitBusy    *ggui.StateValue[string]
	GitBase    *ggui.StateValue[string] // the branch the changes are read since, "" for HEAD
	GitNote    *ggui.StateValue[gitNote]
	BranchOpen *ggui.StateValue[bool]
	BranchName *ggui.StateValue[string]
	BranchErr  *ggui.StateValue[string]
	// The History tab: the commits, and the one chosen.
	GitHistory *ggui.StateValue[historyView]
	GitCommit  *ggui.StateValue[commitView]
	// historyReading is set while the history is asked for; UI goroutine
	// only.
	historyReading bool

	SidebarOpen *ggui.StateValue[bool]
	// The sidebar's filter, and whether its field shows.
	FilterOpen    *ggui.StateValue[bool]
	SidebarFilter *ggui.StateValue[string]
	DetailsOpen   *ggui.StateValue[bool]
	// DetailsWidth is the right panel's width, following a drag of its
	// edge; the settings keep it when the drag ends.
	DetailsWidth *ggui.StateValue[float64]
	DetailTab    *ggui.StateValue[int]
	ImageOpen    *ggui.StateValue[bool] // the previewed image is shown enlarged
	NewOpen      *ggui.StateValue[bool]
	Form         *ggui.StateValue[newSession]
	FormErr      *ggui.StateValue[string]

	conns map[string]*termConn // UI goroutine only
	// lastSession is the session tab shown last, which a note from a diff
	// goes to first.
	lastSession string
	lastTap     tabTap // the last tap on a diff's tab, which a second keeps
	// revealedTab is the tab each pane last scrolled its tab bar to, so it
	// does so once per tab it comes to show.
	revealedTab map[int]string
	dialing     map[string]bool

	mu        sync.Mutex // guards the link fields below, which workers read
	cli       *client.Client
	daemon    string // the external daemon's address from the settings
	embedAddr string // the app's own daemon, while it runs
	psk       string

	// linked says the window knows which daemon to talk to; until then a
	// poll would dial an address nothing may answer and wait out its timeout.
	linked atomic.Bool
	kick   chan struct{} // polls now rather than at the next tick
	// approveInApp mirrors the setting for the polling worker.
	approveInApp atomic.Bool
}

func newModel(cfg *config.Config, addr string) *model {
	go systemLanguage() // asks the platform, so not on the first frame
	deep := func(a, b []protocol.Node) bool { return reflect.DeepEqual(a, b) }
	p := loadPrefs()
	m := &model{
		cfg:            cfg,
		addr:           addr,
		Prefs:          ggui.State(p),
		Page:           ggui.State("workspace"),
		Section:        ggui.State("general"),
		Host:           ggui.State(draftFrom(cfg)),
		AgentDraft:     ggui.State(draftAgents(cfg.Agents)).WithEqual(slices.Equal),
		HostNote:       ggui.State(""),
		ConfirmOpen:    ggui.State(false),
		RenameOpen:     ggui.State(false),
		RenameText:     ggui.State(""),
		RenameErr:      ggui.State(""),
		StopOpen:       ggui.State(false),
		DiscardOpen:    ggui.State(false),
		HunkOpen:       ggui.State(false),
		QuitOpen:       ggui.State(false),
		PaletteOpen:    ggui.State(false),
		PaletteQuery:   ggui.State(""),
		ExtensionsOpen: ggui.State(false),
		QuickOpen:      ggui.State(false),
		QuickQuery:     ggui.State(""),
		SearchQuery:    ggui.State(""),
		Search:         ggui.State(searchView{}).WithEqual(func(a, b searchView) bool { return reflect.DeepEqual(a, b) }),
		Docs:           ggui.State(map[string]docInfo{}).WithEqual(maps.Equal),
		docs:           map[string]*document{},
		Diffs:          ggui.State(map[string]diffTab{}).WithEqual(func(a, b map[string]diffTab) bool { return reflect.DeepEqual(a, b) }),
		hl:             newHighlighter(),
		ExtResults:     ggui.State(extSearch{}).WithEqual(func(a, b extSearch) bool { return reflect.DeepEqual(a, b) }),
		ExtBusy:        ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		ExtErr:         ggui.State(""),
		MCPServers:     ggui.State(maps.Clone(cfg.ContextServers)).WithEqual(func(a, b map[string]mcp.Server) bool { return reflect.DeepEqual(a, b) }),
		MCPInfo:        ggui.State(map[string]mcpInfo(nil)).WithEqual(func(a, b map[string]mcpInfo) bool { return reflect.DeepEqual(a, b) }),
		MCPNote:        ggui.State(""),
		Extensions:     ggui.State([]zed.Extension(nil)).WithEqual(func(a, b []zed.Extension) bool { return reflect.DeepEqual(a, b) }),
		DaemonState:    ggui.State(daemonState{}),
		DaemonLog:      ggui.State([]string(nil)).WithEqual(slices.Equal),
		embedded:       newEmbeddedDaemon(),
		kick:           make(chan struct{}, 1),
		Conn:           ggui.State(connState{}),
		Nodes:          ggui.State([]protocol.Node(nil)).WithEqual(deep),
		Sessions:       ggui.State([]protocol.SessionInfo(nil)).WithEqual(func(a, b []protocol.SessionInfo) bool { return reflect.DeepEqual(a, b) }),
		Agents:         ggui.State(agentspec.Targets(cfg.Agents)).WithEqual(slices.Equal),
		Selected:       ggui.State(""),
		Tabs:           ggui.State([]string(nil)).WithEqual(slices.Equal),
		Layout:         ggui.State(newLayout()).WithEqual(func(a, b *layoutNode) bool { return reflect.DeepEqual(a, b) }),
		FocusGroup:     ggui.State(1),
		Dragging:       ggui.State(false),
		TabHint:        ggui.State(tabHint{}),
		Attached:       ggui.State([]string(nil)).WithEqual(slices.Equal),
		Titles:         ggui.State(map[string]string{}).WithEqual(func(a, b map[string]string) bool { return reflect.DeepEqual(a, b) }),
		Unread:         ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Printed:        ggui.State(map[string]time.Time{}).WithEqual(func(a, b map[string]time.Time) bool { return reflect.DeepEqual(a, b) }),
		Minute:         ggui.State(time.Now().Truncate(time.Minute)),
		ChatModes:      ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		chats:          map[string]*chatState{},
		Dirs:           ggui.State(map[string]dirListing{}).WithEqual(func(a, b map[string]dirListing) bool { return reflect.DeepEqual(a, b) }),
		FilesOpen:      ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		FileSel:        ggui.State(""),
		Preview:        ggui.State(filePreview{}).WithEqual(filePreview.same),
		FileAsk:        ggui.State(fileAsk{}),
		FileName:       ggui.State(""),
		FileErr:        ggui.State(""),
		FileNote:       ggui.State(""),
		FileNameOpen:   ggui.State(false),
		FileDeleteOpen: ggui.State(false),
		Git:            ggui.State(gitView{}).WithEqual(func(a, b gitView) bool { return reflect.DeepEqual(a, b) }),
		GitClosed:      ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		GitSel:         ggui.State(""),
		GitSkip:        ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		GitMessage:     ggui.State(""),
		GitAmend:       ggui.State(false),
		GitBusy:        ggui.State(""),
		GitBase:        ggui.State(""),
		GitNote:        ggui.State(gitNote{}),
		BranchOpen:     ggui.State(false),
		BranchName:     ggui.State(""),
		BranchErr:      ggui.State(""),
		GitHistory:     ggui.State(historyView{}).WithEqual(func(a, b historyView) bool { return reflect.DeepEqual(a, b) }),
		GitCommit:      ggui.State(commitView{}).WithEqual(func(a, b commitView) bool { return reflect.DeepEqual(a, b) }),
		SidebarOpen:    ggui.State(true),
		FilterOpen:     ggui.State(false),
		SidebarFilter:  ggui.State(""),
		DetailsOpen:    ggui.State(true),
		DetailsWidth:   ggui.State(p.DetailsWidth),
		DetailTab:      ggui.State(0),
		ImageOpen:      ggui.State(false),
		NewOpen:        ggui.State(false),
		Form:           ggui.State(newSession{Agent: p.DefaultAgent, Project: p.DefaultProject}),
		FormErr:        ggui.State(""),
		conns:          map[string]*termConn{},
		revealedTab:    map[int]string{},
		dialing:        map[string]bool{},
		daemon:         p.Daemon,
		psk:            cfg.PSK,
	}
	m.approveInApp.Store(p.ApproveInApp)
	return m
}

// start begins polling the daemon. post delivers work to the UI goroutine.
func (m *model) start(ctx context.Context, post func(func())) {
	m.post = post
	m.applyDaemonMode()
	m.loadExtensions()
	go func() {
		for {
			m.poll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-m.kick:
			case <-time.After(pollInterval):
			}
		}
	}()
}

// daemonAddr is the daemon the GUI talks through: the app's own when it
// runs one, else the external one the settings or the -daemon flag name,
// else the local default.
func (m *model) daemonAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.embedAddr != "":
		return m.embedAddr
	case m.daemon != "":
		return m.daemon
	}
	return m.addr
}

// reconnect drops the daemon link so the next request dials again, as after
// the external address or the key changed.
func (m *model) reconnect(daemon, psk string) {
	m.mu.Lock()
	c := m.cli
	m.cli, m.daemon, m.psk = nil, daemon, psk
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// unlink leaves the window with no daemon, as when the app's own stops.
func (m *model) unlink(reason string) {
	m.linked.Store(false)
	m.mu.Lock()
	c := m.cli
	m.cli, m.embedAddr = nil, ""
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	for id, conn := range m.conns {
		conn.close()
		delete(m.conns, id)
	}
	m.Attached.Set(nil)
	m.Conn.Set(connState{Err: reason})
}

// setLink points the window at the app's own daemon, or with "" back at the
// external one.
func (m *model) setLink(embedAddr string) {
	m.mu.Lock()
	c := m.cli
	m.cli, m.embedAddr = nil, embedAddr
	m.mu.Unlock()
	m.linked.Store(true)
	select {
	case m.kick <- struct{}{}:
	default:
	}
	if c != nil {
		_ = c.Close()
	}
	// Terminals attached through the old link went with it.
	for _, id := range slices.Clone(ggui.Peek(m.Tabs)) {
		if conn := m.conns[id]; conn != nil {
			conn.close()
			delete(m.conns, id)
		}
	}
	m.Attached.Set(nil)
	m.attachVisible()
}

// key is the pre-shared key requests carry.
func (m *model) key() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.psk
}

// client returns a connected client, dialing on first use or after a failure.
func (m *model) client(ctx context.Context) (*client.Client, error) {
	addr := m.daemonAddr()
	psk := m.key()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cli != nil {
		return m.cli, nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := client.Dial(dialCtx, addr, psk)
	if err != nil {
		return nil, err
	}
	m.cli = c
	return c, nil
}

func (m *model) dropClient(c *client.Client) {
	m.mu.Lock()
	if m.cli == c {
		m.cli = nil
	}
	m.mu.Unlock()
	_ = c.Close()
}

// poll fetches the mesh view on a worker and publishes it on the UI thread.
func (m *model) poll(ctx context.Context) {
	logs := m.embedded.logs.snapshot()
	m.post(func() { m.DaemonLog.Set(logs) })
	if !m.linked.Load() {
		return // still deciding, or starting, which daemon to talk to
	}
	fail := func(err error) {
		m.post(func() { m.Conn.Set(connState{Err: err.Error()}) })
	}
	c, err := m.client(ctx)
	if err != nil {
		fail(err)
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodes, err := c.Nodes(reqCtx, m.key())
	if err != nil {
		m.dropClient(c)
		fail(err)
		return
	}
	// Approvals wait for this window's answer only while it offers one.
	approvals := m.approveInApp.Load()
	sessions, err := c.ListSessions(reqCtx, m.key(), protocol.ListSessions{Approvals: approvals})
	if err != nil {
		m.dropClient(c)
		fail(err)
		return
	}
	// LastSeen moves on every poll; leaving it out keeps an unchanged mesh
	// from rebuilding the sidebar.
	for i := range nodes {
		nodes[i].LastSeen = time.Time{}
	}
	self := c.Node()
	self.LastSeen = time.Time{}
	printed := make(map[string]time.Time, len(sessions))
	for i := range sessions {
		if t := sessions[i].LastOutput; !t.IsZero() {
			printed[sessions[i].ID] = t.Truncate(time.Minute)
		}
		sessions[i].LastOutput = time.Time{}
	}
	m.post(func() {
		m.Conn.Set(connState{OK: true, Self: self})
		m.Nodes.Set(nodes)
		// The sidebar keeps the folders it groups, so a folder outlives its
		// last session: killing that session leaves the folder to start
		// another in.
		m.keepProjects(sessions)
		// A restarted daemon restores IDs as stopped. Discard their old
		// terminal links, including hidden tabs, before displaying them again.
		for _, info := range sessions {
			previous, known := m.session(info.ID)
			if info.State == protocol.StateStopped && known && previous.State != protocol.StateStopped {
				if conn := m.conns[info.ID]; conn != nil {
					conn.close()
					delete(m.conns, info.ID)
					m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(id string) bool { return id == info.ID }))
				}
			}
		}
		m.noteStatus(sessions)
		m.Sessions.Set(sessions)
		m.Printed.Set(printed)
		if now := time.Now().Truncate(time.Minute); !now.Equal(ggui.Peek(m.Minute)) {
			m.Minute.Set(now)
		}
		m.attachVisible()
		m.syncTitles()
	})
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
	return ok && s.Busy && s.State != protocol.StateExited
}

func (m *model) session(id string) (protocol.SessionInfo, bool) {
	for _, s := range ggui.Peek(m.Sessions) {
		if s.ID == id {
			return s, true
		}
	}
	return protocol.SessionInfo{}, false
}

func nodeName(nodes []protocol.Node, id string) string {
	if n, ok := nodeIn(nodes, id); ok {
		return n.Name
	}
	return short(id)
}

func nodeIn(nodes []protocol.Node, id string) (protocol.Node, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return protocol.Node{}, false
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

// The ways the sidebar groups its sessions.
const (
	groupProject = "project" // by node and working directory
	groupHost    = "host"    // by node
	groupFlat    = "flat"    // one list
)

// sessionGroup is one sidebar section: the sessions sharing a node and a
// project, the way Paseo groups workspaces under a repository, or only a
// node. Project is empty for a node's section.
type sessionGroup struct {
	Key      string
	NodeID   string
	Project  string
	Sessions []protocol.SessionInfo
}

// keepProjects remembers the folders the sidebar groups sessions into. A
// folder with nothing in it says nothing here, so the list only grows.
func (m *model) keepProjects(sessions []protocol.SessionInfo) {
	projects := ggui.Peek(m.Prefs).Projects
	roots := projectRoots(sessions, projects)
	known := make(map[fileRoot]bool, len(projects))
	for _, r := range projects {
		known[r] = true
	}
	var added []fileRoot
	for _, s := range sessions {
		root := rootOf(roots, s.NodeID, s.Project)
		r := fileRoot{Node: s.NodeID, Path: root}
		if root == "" || known[r] {
			continue
		}
		known[r] = true
		added = append(added, r)
	}
	if len(added) == 0 {
		return
	}
	m.Prefs.Update(func(p prefs) prefs { p.Projects = append(slices.Clone(p.Projects), added...); return p })
}

// forgetProject drops a folder from the sidebar. It is offered for a folder
// with no sessions, whose groups would not bring it back.
func (m *model) forgetProject(r fileRoot) {
	m.Prefs.Update(func(p prefs) prefs {
		p.Projects = slices.DeleteFunc(slices.Clone(p.Projects), func(o fileRoot) bool { return o == r })
		return p
	})
}

// projectRoots are each node's project folders, shortest first: the
// sessions' projects and the ones kept without sessions.
func projectRoots(sessions []protocol.SessionInfo, projects []fileRoot) map[string][]string {
	roots := map[string][]string{}
	add := func(node, path string) {
		if path != "" {
			roots[node] = append(roots[node], path)
		}
	}
	for _, s := range sessions {
		add(s.NodeID, s.Project)
	}
	for _, r := range projects {
		add(r.Node, r.Path)
	}
	for _, ps := range roots {
		sort.Slice(ps, func(a, b int) bool { return len(ps[a]) < len(ps[b]) })
	}
	return roots
}

// rootOf is the shortest root of a node that contains path, or "".
func rootOf(roots map[string][]string, node, path string) string {
	for _, root := range roots[node] {
		if within(path, root) {
			return root
		}
	}
	return ""
}

// groupSessions sorts sessions into the sidebar's sections for a grouping.
// By project, a session started in a folder inside another session's
// project, as a worktree or a package of its repository is, goes in that
// project's section. Remembered projects are roots too: a folder keeps the
// sessions started in it after it had none, and keeps a section when it has
// none.
func groupSessions(sessions []protocol.SessionInfo, by string, nodes []protocol.Node, projects []fileRoot) []sessionGroup {
	switch by {
	case groupFlat:
		if len(sessions) == 0 {
			return nil
		}
		return []sessionGroup{{Key: "", Sessions: sessions}}
	case groupHost:
		groups := bucket(sessions, func(s protocol.SessionInfo) (string, string) { return s.NodeID, "" })
		sort.SliceStable(groups, func(a, b int) bool {
			return nodeName(nodes, groups[a].NodeID) < nodeName(nodes, groups[b].NodeID)
		})
		return groups
	}
	roots := projectRoots(sessions, projects)
	groups := bucket(sessions, func(s protocol.SessionInfo) (string, string) {
		return s.NodeID, rootOf(roots, s.NodeID, s.Project)
	})
	// A remembered folder with no sessions keeps a section of its own,
	// unless a shorter root covers it.
	present := make(map[string]bool, len(groups))
	for _, g := range groups {
		present[g.Key] = true
	}
	for _, r := range projects {
		if r.Path == "" || present[dirKey(r.Node, r.Path)] {
			continue
		}
		if root := rootOf(roots, r.Node, r.Path); root != "" && root != r.Path {
			continue
		}
		present[dirKey(r.Node, r.Path)] = true
		groups = append(groups, sessionGroup{Key: dirKey(r.Node, r.Path), NodeID: r.Node, Project: r.Path})
	}
	sort.SliceStable(groups, func(a, b int) bool {
		return projectName(groups[a].Project) < projectName(groups[b].Project)
	})
	return groups
}

// bucket groups sessions by the node and project key gives them, in the
// order each first appears.
func bucket(sessions []protocol.SessionInfo, key func(protocol.SessionInfo) (string, string)) []sessionGroup {
	var groups []sessionGroup
	index := map[string]int{}
	for _, s := range sessions {
		node, project := key(s)
		k := dirKey(node, project)
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, sessionGroup{Key: k, NodeID: node, Project: project})
		}
		groups[i].Sessions = append(groups[i].Sessions, s)
	}
	return groups
}

// within reports whether dir is root or a folder inside it.
func within(dir, root string) bool {
	if dir == root {
		return true
	}
	if root == "" {
		return false
	}
	sep := pathSep(root)
	return strings.HasPrefix(dir, strings.TrimSuffix(root, sep)+sep)
}

// Actions -------------------------------------------------------------------

// selectSession shows a session: in the pane that has it as a tab, or as a
// new tab of the current pane, attaching its terminal if it is not already.
func (m *model) selectSession(id string) {
	if id != "" {
		m.showTab(id)
	}
}

// attachVisible resumes only the active tab of each visible pane.
func (m *model) attachVisible() {
	for _, group := range ggui.Peek(m.Layout).groups() {
		if group.Active != "" && m.conns[group.Active] == nil {
			m.attach(group.Active)
		}
	}
}

// attach dials a session's terminal on a worker.
func (m *model) attach(id string) {
	if m.dialing[id] || !isSessionTab(id) {
		return
	}
	if conn := m.conns[id]; conn != nil {
		conn.mu.Lock()
		failed := conn.err != nil || conn.closed
		conn.mu.Unlock()
		if !failed {
			return
		}
		conn.close()
		delete(m.conns, id)
		m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(tab string) bool { return tab == id }))
	}
	info, ok := m.session(id)
	if !ok {
		return
	}
	m.dialing[id] = true
	post := m.post
	wake := func() { post(func() {}) }
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		var conn *termConn
		if err == nil {
			conn, err = dialTerm(context.Background(), c, m.key(), info, wake, func() { post(m.syncTitles) })
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
	if conn := m.conns[id]; conn != nil {
		conn.close()
		delete(m.conns, id)
	}
	delete(m.chats, id)
	m.ChatModes.Update(func(v map[string]bool) map[string]bool { v = maps.Clone(v); delete(v, id); return v })
	m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(s string) bool { return s == id }))
}

// openNew shows the new-session dialog, prefilled for a node and project.
func (m *model) openNew(nodeID, project string) {
	p := ggui.Peek(m.Prefs)
	f := newSession{Agent: p.DefaultAgent, NodeID: nodeID, Project: project}
	if prev := ggui.Peek(m.Form); prev.Agent != "" {
		f.Agent = prev.Agent // the last choice, within one run
	}
	if f.NodeID == "" {
		f.NodeID = ggui.Peek(m.Conn).Self.ID
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
	req := protocol.OpenSession{NodeID: nodeID, Agent: agent, Profile: profile, Project: f.Project, Cols: 120, Rows: 36}
	if f.Worktree {
		branch := strings.TrimSpace(f.Branch)
		if branch == "" {
			m.FormErr.Set(T("Name the branch the worktree is made on."))
			return
		}
		req.Worktree = &protocol.Worktree{Branch: branch}
	}
	ggui.Async(func() (protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err != nil {
			return protocol.SessionInfo{}, err
		}
		return c.Open(ctx, m.key(), req)
	}, func(info protocol.SessionInfo, err error) {
		if err != nil {
			m.FormErr.Set(err.Error())
			m.NewOpen.Set(true)
			return
		}
		m.NewOpen.Set(false)
		sessions := slices.Clone(ggui.Peek(m.Sessions))
		m.Sessions.Set(append(sessions, info))
		m.selectSession(info.ID)
	})
}

// askKill confirms before killing a session, unless the settings say not
// to ask, its program has already exited, or it is a shell with nothing
// running. Closing a tab does this: the
// tab and its session go together, and "Detach tab" keeps one running.
func (m *model) askKill(id string) {
	switch {
	case isDocTab(id):
		m.askCloseDoc(id)
		return
	case isDiffTab(id):
		m.closeDiff(id)
		return
	}
	s, ok := m.session(id)
	if !ggui.Peek(m.Prefs).ConfirmKill || (ok && (s.State == protocol.StateExited || s.State == protocol.StateStopped)) {
		m.kill(id)
		return
	}
	confirm := func() {
		m.confirmKill = id
		m.ConfirmOpen.Set(true)
	}
	if !ok || s.Agent != "shell" {
		confirm()
		return
	}
	// A shell at its prompt loses nothing; whether one is, is asked now
	// rather than taken from the last poll, as a command may have started
	// since.
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) ([]protocol.SessionInfo, error) {
		return c.Sessions(ctx, m.key())
	}, func(list []protocol.SessionInfo, err error) {
		if now, ok := sessionIn(list, id); err == nil && ok && now.Idle {
			m.kill(id)
			return
		}
		confirm()
	})
}

func (m *model) confirmedKill() {
	if id := m.confirmKill; id != "" {
		m.confirmKill = ""
		m.kill(id)
	}
}

// kill terminates a session and closes its tab.
func (m *model) kill(id string) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	m.closeTab(id)
	ggui.Async(func() (struct{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, c.Kill(ctx, m.key(), protocol.CloseSession{SessionID: id, NodeID: info.NodeID})
	}, func(_ struct{}, err error) {
		if err == nil {
			m.Sessions.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Sessions)), func(s protocol.SessionInfo) bool { return s.ID == id }))
		}
	})
}

// tidyable tells whether closing a tab loses nothing, as the sessions'
// last poll has it: a file without unsaved changes, a diff, or a shell at
// its prompt or exited. Agents' tabs are never tidied away.
func tidyable(id string, sessions []protocol.SessionInfo, docs map[string]docInfo) bool {
	switch {
	case isDocTab(id):
		return !docs[id].Dirty
	case isDiffTab(id):
		return true
	}
	s, ok := sessionIn(sessions, id)
	return ok && s.Agent == "shell" && (s.Idle || s.State == protocol.StateExited || s.State == protocol.StateStopped)
}

// anyTidyable tells whether any open tab is tidyable.
func (m *model) anyTidyable() bool {
	sessions, docs := ggui.Peek(m.Sessions), ggui.Peek(m.Docs)
	return slices.ContainsFunc(ggui.Peek(m.Tabs), func(id string) bool { return tidyable(id, sessions, docs) })
}

// tidyTabs closes the tabs among ids that lose nothing by closing, killing
// the shells among them. Whether a shell is at its prompt is asked now
// rather than taken from the last poll, as askKill does.
func (m *model) tidyTabs(ids []string) {
	docs := ggui.Peek(m.Docs)
	var shells []string
	for _, id := range ids {
		switch {
		case isDocTab(id):
			if tidyable(id, nil, docs) {
				m.closeDoc(id)
			}
		case isDiffTab(id):
			m.closeDiff(id)
		default:
			if s, ok := m.session(id); ok && s.Agent == "shell" {
				shells = append(shells, id)
			}
		}
	}
	if len(shells) == 0 {
		return
	}
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) ([]protocol.SessionInfo, error) {
		return c.Sessions(ctx, m.key())
	}, func(list []protocol.SessionInfo, err error) {
		if err != nil {
			return
		}
		for _, id := range shells {
			if tidyable(id, list, nil) {
				m.kill(id)
			}
		}
	})
}

// composerDraft is a session's message draft, kept with its chat state so
// closing the tab drops it.
func (m *model) composerDraft(id string) string { return ggui.Peek(m.chatState(id).draft) }

func (m *model) setComposerDraft(id, text string) { m.chatState(id).draft.Set(text) }

// chatTarget is the session a message would go to: the focused one while it
// shows Chat, else a pane that does.
func (m *model) chatTarget() string {
	if id := m.Selected.Get(); isSessionTab(id) && m.ChatModes.Get()[id] {
		return id
	}
	for _, g := range m.Layout.Get().groups() {
		if id := g.Active; isSessionTab(id) && m.ChatModes.Get()[id] {
			return id
		}
	}
	return ""
}

// sendComposer types a session's draft into its terminal, followed by
// Enter, and clears the draft.
func (m *model) sendComposer(id string) {
	conn := m.conns[id]
	if conn == nil {
		return
	}
	conn.sendText(m.composerDraft(id))
	m.setComposerDraft(id, "")
}

// syncTitles publishes the titles programs set on their terminals.
func (m *model) syncTitles() {
	titles := map[string]string{}
	for _, info := range ggui.Peek(m.Sessions) {
		if title := cleanTitle(info.Title); title != "" {
			titles[info.ID] = title
		}
	}
	for id, conn := range m.conns {
		if title, known := conn.windowTitle(); known {
			title = cleanTitle(title)
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

// openSettings shows the settings page, at a section when one is named.
func (m *model) openSettings(section string) {
	if section != "" {
		m.Section.Set(section)
	}
	m.Host.Set(draftFrom(m.cfg))
	m.HostNote.Set("")
	m.Page.Set("settings")
}

// shutdown detaches every terminal, hangs up, and stops the app's daemon
// with the sessions it runs.
func (m *model) shutdown() {
	defer m.embedded.stop()
	defer m.hl.close()
	for id, conn := range m.conns {
		conn.close()
		delete(m.conns, id)
	}
	m.mu.Lock()
	c := m.cli
	m.cli = nil
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}
