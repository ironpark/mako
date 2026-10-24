package gui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
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
}

// model is the GUI's state. Signals are touched on the UI goroutine only;
// workers hand results back through post.
type model struct {
	cfg  *config.Config
	addr string // the -daemon flag; Prefs.Daemon overrides it
	post func(func())

	Prefs       *ggui.StateValue[prefs]
	Page        *ggui.StateValue[string] // "workspace" or "settings"
	Section     *ggui.StateValue[string] // the settings page shown
	Host        *ggui.StateValue[hostDraft]
	AgentDraft  *ggui.StateValue[[]agentDraft]
	HostNote    *ggui.StateValue[string]
	ConfirmOpen *ggui.StateValue[bool]
	confirmKill string // the session ConfirmOpen asks about
	StopOpen    *ggui.StateValue[bool]
	pendingStop func() // what StopOpen confirms

	DaemonState *ggui.StateValue[daemonState]
	DaemonLog   *ggui.StateValue[[]string]
	embedded    *embeddedDaemon
	daemonGen   int // UI goroutine only; see startEmbedded

	Conn     *ggui.StateValue[connState]
	Nodes    *ggui.StateValue[[]protocol.Node]
	Sessions *ggui.StateValue[[]protocol.SessionInfo]
	Agents   *ggui.StateValue[[]string] // what this node can launch, as agent:profile targets

	Selected *ggui.StateValue[string]   // the session in the main pane
	Tabs     *ggui.StateValue[[]string] // sessions opened as tabs, in order
	Attached *ggui.StateValue[[]string] // tabs whose terminal is connected
	Titles   *ggui.StateValue[map[string]string]

	SidebarOpen *ggui.StateValue[bool]
	DetailsOpen *ggui.StateValue[bool]
	DetailTab   *ggui.StateValue[int]
	NewOpen     *ggui.StateValue[bool]
	Form        *ggui.StateValue[newSession]
	FormErr     *ggui.StateValue[string]
	Composer    *ggui.StateValue[string]

	conns   map[string]*termConn // UI goroutine only
	dialing map[string]bool

	mu        sync.Mutex // guards the link fields below, which workers read
	cli       *client.Client
	daemon    string // the external daemon's address from the settings
	embedAddr string // the app's own daemon, while it runs
	psk       string

	// linked says the window knows which daemon to talk to; until then a
	// poll would dial an address nothing may answer and wait out its timeout.
	linked atomic.Bool
	kick   chan struct{} // polls now rather than at the next tick
}

func newModel(cfg *config.Config, addr string) *model {
	deep := func(a, b []protocol.Node) bool { return reflect.DeepEqual(a, b) }
	p := loadPrefs()
	m := &model{
		cfg:         cfg,
		addr:        addr,
		Prefs:       ggui.State(p),
		Page:        ggui.State("workspace"),
		Section:     ggui.State("general"),
		Host:        ggui.State(draftFrom(cfg)),
		AgentDraft:  ggui.State(draftAgents(cfg.Agents)).WithEqual(slices.Equal),
		HostNote:    ggui.State(""),
		ConfirmOpen: ggui.State(false),
		StopOpen:    ggui.State(false),
		DaemonState: ggui.State(daemonState{}),
		DaemonLog:   ggui.State([]string(nil)).WithEqual(slices.Equal),
		embedded:    newEmbeddedDaemon(),
		kick:        make(chan struct{}, 1),
		Conn:        ggui.State(connState{}),
		Nodes:       ggui.State([]protocol.Node(nil)).WithEqual(deep),
		Sessions:    ggui.State([]protocol.SessionInfo(nil)).WithEqual(func(a, b []protocol.SessionInfo) bool { return reflect.DeepEqual(a, b) }),
		Agents:      ggui.State(agentspec.Targets(cfg.Agents)).WithEqual(slices.Equal),
		Selected:    ggui.State(""),
		Tabs:        ggui.State([]string(nil)).WithEqual(slices.Equal),
		Attached:    ggui.State([]string(nil)).WithEqual(slices.Equal),
		Titles:      ggui.State(map[string]string{}).WithEqual(func(a, b map[string]string) bool { return reflect.DeepEqual(a, b) }),
		SidebarOpen: ggui.State(true),
		DetailsOpen: ggui.State(false),
		DetailTab:   ggui.State(0),
		NewOpen:     ggui.State(false),
		Form:        ggui.State(newSession{Agent: p.DefaultAgent, Project: p.DefaultProject}),
		FormErr:     ggui.State(""),
		Composer:    ggui.State(""),
		conns:       map[string]*termConn{},
		dialing:     map[string]bool{},
		daemon:      p.Daemon,
		psk:         cfg.PSK,
	}
	return m
}

// start begins polling the daemon. post delivers work to the UI goroutine.
func (m *model) start(ctx context.Context, post func(func())) {
	m.post = post
	m.applyDaemonMode()
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
	if id := ggui.Peek(m.Selected); id != "" {
		m.attach(id)
	}
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
	sessions, err := c.Sessions(reqCtx, m.key())
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
	m.post(func() {
		m.Conn.Set(connState{OK: true, Self: self})
		m.Nodes.Set(nodes)
		m.Sessions.Set(sessions)
		m.syncTitles()
	})
}

// Lookups ------------------------------------------------------------------

func (m *model) session(id string) (protocol.SessionInfo, bool) {
	for _, s := range ggui.Peek(m.Sessions) {
		if s.ID == id {
			return s, true
		}
	}
	return protocol.SessionInfo{}, false
}

func nodeName(nodes []protocol.Node, id string) string {
	for _, n := range nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return short(id)
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

// projectGroup is one sidebar section: the sessions sharing a node and a
// working directory, the way Paseo groups workspaces under a repository.
type projectGroup struct {
	Key      string
	NodeID   string
	Project  string
	Sessions []protocol.SessionInfo
}

func groupSessions(sessions []protocol.SessionInfo) []projectGroup {
	var groups []projectGroup
	index := map[string]int{}
	for _, s := range sessions {
		key := s.NodeID + "\x00" + s.Project
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, projectGroup{Key: key, NodeID: s.NodeID, Project: s.Project})
		}
		groups[i].Sessions = append(groups[i].Sessions, s)
	}
	sort.SliceStable(groups, func(a, b int) bool {
		return projectName(groups[a].Project) < projectName(groups[b].Project)
	})
	return groups
}

// Actions -------------------------------------------------------------------

// selectSession shows a session, opening it as a tab and attaching its
// terminal if it is not already.
func (m *model) selectSession(id string) {
	if id == "" {
		m.Selected.Set("")
		return
	}
	tabs := ggui.Peek(m.Tabs)
	if !slices.Contains(tabs, id) {
		m.Tabs.Set(append(slices.Clone(tabs), id))
	}
	m.Selected.Set(id)
	m.attach(id)
}

// attach dials a session's terminal on a worker.
func (m *model) attach(id string) {
	if m.conns[id] != nil || m.dialing[id] {
		return
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
			conn, err = dialTerm(context.Background(), c, m.key(), info, wake)
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
			m.Attached.Set(append(slices.Clone(ggui.Peek(m.Attached)), id))
		})
	}()
}

// closeTab detaches a session's tab; the session keeps running.
func (m *model) closeTab(id string) {
	tabs := ggui.Peek(m.Tabs)
	i := slices.Index(tabs, id)
	if i < 0 {
		return
	}
	tabs = slices.Delete(slices.Clone(tabs), i, i+1)
	m.Tabs.Set(tabs)
	if conn := m.conns[id]; conn != nil {
		conn.close()
		delete(m.conns, id)
	}
	m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(s string) bool { return s == id }))
	if ggui.Peek(m.Selected) == id {
		next := ""
		if len(tabs) > 0 {
			next = tabs[min(i, len(tabs)-1)]
		}
		m.Selected.Set(next)
	}
}

// openNew shows the new-session dialog, prefilled for a node and project.
func (m *model) openNew(nodeID, project string) {
	p := ggui.Peek(m.Prefs)
	f := newSession{Agent: p.DefaultAgent, NodeID: nodeID, Project: p.DefaultProject}
	if prev := ggui.Peek(m.Form); prev.Agent != "" {
		f.Agent = prev.Agent // the last choice, within one run
	}
	if f.NodeID == "" {
		f.NodeID = ggui.Peek(m.Conn).Self.ID
	}
	if project != "" {
		f.Project = project
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
	f := ggui.Peek(m.Form)
	if f.Project == "" {
		m.FormErr.Set("Choose a working directory.")
		return
	}
	nodeID := f.NodeID
	if nodeID == ggui.Peek(m.Conn).Self.ID {
		nodeID = "" // the local daemon
	}
	agent, profile := agentspec.SplitTarget(f.Agent)
	req := protocol.OpenSession{NodeID: nodeID, Agent: agent, Profile: profile, Project: f.Project, Cols: 120, Rows: 36}
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
// to ask.
func (m *model) askKill(id string) {
	if !ggui.Peek(m.Prefs).ConfirmKill {
		m.kill(id)
		return
	}
	m.confirmKill = id
	m.ConfirmOpen.Set(true)
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

// sendComposer types the composer's text into the selected session,
// followed by Enter.
func (m *model) sendComposer() {
	text := ggui.Peek(m.Composer)
	conn := m.conns[ggui.Peek(m.Selected)]
	if conn == nil {
		return
	}
	conn.sendText(text)
	m.Composer.Set("")
}

// syncTitles publishes the titles programs set on their terminals.
func (m *model) syncTitles() {
	titles := map[string]string{}
	for id, conn := range m.conns {
		if t := conn.windowTitle(); t != "" {
			titles[id] = t
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
