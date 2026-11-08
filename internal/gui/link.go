package gui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// pollInterval is how often the mesh's nodes and sessions are refreshed.
const pollInterval = 2 * time.Second

// connState describes the link to the daemon the GUI talks through.
type connState struct {
	OK   bool
	Err  string
	Self *protocol.Node
}

// daemonLink is what the window shares between the UI goroutine and the
// workers that poll and watch its daemon: which daemon to talk to, the
// client dialed to it, and the flags that pace the poll. Every field of it
// is safe to use from any goroutine; the model's other fields are not, but
// for devicesAt, which only the poll touches.
type daemonLink struct {
	addr string // the -daemon flag; Prefs.Daemon overrides it. Set once.

	mu        sync.Mutex // guards the fields below
	cli       *client.Client
	daemon    string // the external daemon's address from the settings
	embedAddr string // the app's own daemon, while it runs
	gen       int    // counts the swaps, so a dial can tell it was outrun

	// linked says the window knows which daemon to talk to; until then a
	// poll would dial an address nothing may answer and wait out its timeout.
	linked atomic.Bool
	kick   chan struct{} // polls now rather than at the next tick
	// watching is set while the daemon tells the GUI of changes, which
	// then polls seldom.
	watching atomic.Bool
	// approveInApp mirrors the setting for the polling worker.
	approveInApp atomic.Bool
	// accessStale, wsStale and devicesStale say the next poll asks for
	// Access, the workspace and the devices again, as the daemon says they
	// changed; the first two start set, and the devices are first asked
	// for as model.devicesAt is zero.
	accessStale  atomic.Bool
	wsStale      atomic.Bool
	devicesStale atomic.Bool
	syncing      atomic.Bool // the window follows the other nodes; for the poll
}

func newDaemonLink(addr, daemon string) *daemonLink {
	l := &daemonLink{addr: addr, daemon: daemon, kick: make(chan struct{}, 1)}
	l.stale()
	return l
}

// target is the daemon the GUI talks through: the app's own when it runs
// one, else the external one the settings or the -daemon flag name, else
// the local default.
func (l *daemonLink) target() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.targetLocked()
}

func (l *daemonLink) targetLocked() string {
	switch {
	case l.embedAddr != "":
		return l.embedAddr
	case l.daemon != "":
		return l.daemon
	}
	return l.addr
}

// client returns a connected client, dialing on first use or after a
// failure. It dials without the lock, so the UI goroutine pointing the
// link elsewhere meanwhile does not wait out a dial to an address nothing
// answers; a client dialed to an address the link has since left is
// closed and the new one dialed, and one dialed while another worker's
// was kept gives way to it.
func (l *daemonLink) client(ctx context.Context) (*client.Client, error) {
	for {
		l.mu.Lock()
		if c := l.cli; c != nil {
			l.mu.Unlock()
			return c, nil
		}
		addr, gen := l.targetLocked(), l.gen
		l.mu.Unlock()
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, err := client.Dial(dialCtx, addr)
		cancel()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		switch {
		case l.gen != gen:
			l.mu.Unlock()
			_ = c.Close()
			continue
		case l.cli != nil:
			kept := l.cli
			l.mu.Unlock()
			_ = c.Close()
			return kept, nil
		}
		l.cli = c
		l.mu.Unlock()
		return c, nil
	}
}

// drop closes c, and forgets it if it is still the link's client, so the
// next request dials again.
func (l *daemonLink) drop(c *client.Client) {
	l.mu.Lock()
	if l.cli == c {
		l.cli = nil
	}
	l.mu.Unlock()
	_ = c.Close()
}

// hangUp closes the client, so the next request dials again.
func (l *daemonLink) hangUp() { l.swap(func() {}) }

// setDaemon points the link at the external daemon the settings name, ""
// for the -daemon flag's.
func (l *daemonLink) setDaemon(addr string) { l.swap(func() { l.daemon = addr }) }

// setEmbedded points the link at the app's own daemon, "" for none.
func (l *daemonLink) setEmbedded(addr string) { l.swap(func() { l.embedAddr = addr }) }

// swap closes the client, running set under the lock as it lets it go,
// and has the poll ask again for what a new daemon may hold otherwise.
func (l *daemonLink) swap(set func()) {
	l.mu.Lock()
	c := l.cli
	l.cli = nil
	l.gen++
	set()
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	l.stale()
}

// stale asks the next poll for Access and the workspace again.
func (l *daemonLink) stale() {
	l.accessStale.Store(true)
	l.wsStale.Store(true)
}

// pollNow has the poll run now rather than at its next tick.
func (l *daemonLink) pollNow() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// The workers ---------------------------------------------------------------

// start begins polling the daemon. post delivers work to the UI goroutine.
func (m *model) start(ctx context.Context, post func(func())) {
	m.post = post
	m.applyDaemonMode()
	m.loadExtensions()
	go func() {
		for {
			m.poll(ctx)
			every := pollInterval
			if m.link.watching.Load() {
				every = watchedPollInterval
			}
			select {
			case <-ctx.Done():
				return
			case <-m.link.kick:
			case <-time.After(every):
			}
		}
	}()
	go m.watch(ctx)
}

// watchedPollInterval is how often the GUI polls while its daemon tells
// it of changes, as a check on a change it missed.
const watchedPollInterval = 15 * time.Second

// watch follows the daemon's changes, polling as soon as one comes, for as
// long as the daemon offers them; an older daemon does not, and is polled.
func (m *model) watch(ctx context.Context) {
	for ctx.Err() == nil {
		wait := time.Second
		// Nothing is followed until the window knows which daemon it has.
		if m.link.linked.Load() {
			if c, err := m.link.client(ctx); err == nil {
				err = c.Watch(ctx, func(ch *protocol.Changed) {
					if ch.GetAccess() {
						m.link.accessStale.Store(true)
					}
					if ch.GetWorkspace() {
						m.link.wsStale.Store(true)
					}
					if ch.GetDevices() {
						m.link.devicesStale.Store(true)
					}
					m.link.watching.Store(true)
					m.link.pollNow()
				})
				if protocol.HasCode(err, protocol.CodeUnsupported) {
					wait = time.Minute // an older daemon, which may be updated
				}
			}
		}
		m.link.watching.Store(false)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// reconnect drops the daemon link so the next request dials again, as after
// the external address changed.
func (m *model) reconnect(daemon string) {
	m.link.setDaemon(daemon)
}

// unlink leaves the window with no daemon, as when the app's own stops.
func (m *model) unlink(reason string) {
	m.link.linked.Store(false)
	m.link.setEmbedded("")
	m.dropConns()
	m.Attached.Set(nil)
	m.Conn.Set(connState{Err: reason})
}

// setLink points the window at the app's own daemon, or with "" back at the
// external one.
func (m *model) setLink(embedAddr string) {
	m.link.setEmbedded(embedAddr)
	m.link.linked.Store(true)
	m.link.pollNow()
	// Terminals attached through the old link went with it.
	for _, id := range ggui.Peek(m.Tabs) {
		m.dropConn(id)
	}
	m.Attached.Set(nil)
	m.attachVisible()
}

// poll fetches the mesh view on a worker and publishes it on the UI thread.
func (m *model) poll(ctx context.Context) {
	logs := m.embedded.logs.snapshot()
	m.post(func() { m.DaemonLog.Set(logs) })
	if !m.link.linked.Load() {
		return // still deciding, or starting, which daemon to talk to
	}
	fail := func(err error) {
		m.post(func() { m.Conn.Set(connState{Err: err.Error()}) })
	}
	c, err := m.link.client(ctx)
	if err != nil {
		fail(err)
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodes, err := c.Nodes(reqCtx)
	if err != nil {
		m.link.drop(c)
		fail(err)
		return
	}
	// Approvals wait for this window's answer only while it offers one.
	approvals := m.link.approveInApp.Load()
	sessions, err := c.ListSessions(reqCtx, &protocol.ListSessions{Approvals: approvals})
	if err != nil {
		m.link.drop(c)
		fail(err)
		return
	}
	// Access is asked for when it changed, or on every poll while the
	// daemon is not watched.
	accessStale := m.link.accessStale.Swap(false)
	fetchAccess := accessStale || !m.link.watching.Load()
	// The workspace too: at first, and when the daemon has a newer one.
	// A request that failed on the way, rather than being refused by the
	// daemon, is asked again at the next poll, and what the window shows
	// stays as it was meanwhile.
	var ws *protocol.Workspace
	if m.link.syncing.Load() {
		if wsStale := m.link.wsStale.Swap(false); wsStale || !m.link.watching.Load() {
			if ws, err = c.Workspace(reqCtx); err != nil {
				ws = nil // an older daemon
				if wsStale && !daemonRefused(err) {
					m.link.wsStale.Store(true)
				}
			}
		}
	}
	var access *protocol.AccessInfo
	if fetchAccess {
		if access, err = c.Access(reqCtx, &protocol.Access{}); err != nil {
			access = nil // the mesh is off on the daemon
			if !daemonRefused(err) {
				fetchAccess = false
				if accessStale {
					m.link.accessStale.Store(true)
				}
			}
		}
	}
	self := c.Node()
	now := time.Now()
	live := liveNodes(self, nodes, now)
	// LastSeen moves on every poll; leaving it out keeps an unchanged mesh
	// from rebuilding the sidebar.
	for i := range nodes {
		nodes[i].LastSeen = nil
	}
	self.LastSeen = nil
	devices := m.fetchDevices(reqCtx, c, self)
	printed := make(map[string]time.Time, len(sessions))
	for i := range sessions {
		// An agent's time is its conversation's last message: a redraw of
		// its screen says nothing new. Without one, it is the last print.
		t := protocol.TimeOf(sessions[i].GetLastMessage())
		if t.IsZero() {
			t = protocol.TimeOf(sessions[i].GetLastOutput())
		}
		if !t.IsZero() {
			printed[sessions[i].GetId()] = t.Truncate(time.Minute)
		}
		sessions[i].LastOutput, sessions[i].LastMessage = nil, nil
		// An agent at work spins a glyph at the head of its title, which
		// the daemon then reports changed every second: without it, the
		// sessions read the same, and nothing showing them is rebuilt.
		sessions[i].Title = protocol.BareTitle(sessions[i].GetTitle())
	}
	m.post(func() {
		m.Conn.Set(connState{OK: true, Self: self})
		noteAgents(nodes)
		m.Nodes.Set(nodes)
		if fetchAccess {
			m.Access.Set(access)
			m.noteRequests(access)
		}
		if devices != nil {
			m.Devices.Set(*devices)
		}
		// The sidebar keeps the folders it groups, so a folder outlives its
		// last session: killing that session leaves the folder to start
		// another in.
		m.keepProjects(sessions)
		m.joinClones(sessions)
		m.pushProjects(nodes)
		// A restarted daemon restores IDs as stopped. Discard their old
		// terminal links, including hidden tabs, before displaying them again.
		for _, info := range sessions {
			previous, known := m.session(info.GetId())
			if info.GetState() == protocol.StateStopped && known && previous.GetState() != protocol.StateStopped {
				if m.dropConn(info.GetId()) {
					m.Attached.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Attached)), func(id string) bool { return id == info.GetId() }))
				}
			}
		}
		sessions = m.dropKilled(sessions, time.Now())
		m.noteStatus(sessions)
		m.Sessions.Set(sessions)
		m.dropLeftoverTabs(sessions, live, now)
		m.Printed.Set(printed)
		if now := time.Now().Truncate(time.Minute); !now.Equal(ggui.Peek(m.Minute)) {
			m.Minute.Set(now)
		}
		m.attachVisible()
		m.syncTitles()
		if ws != nil {
			m.takeWorkspace(ws)
		}
	})
}

// daemonRefused reports whether err is the daemon's answer to a request,
// as one it does not know or cannot serve, rather than the request failing
// on the way to it. transport.Reply marks the daemon's answers so.
func daemonRefused(err error) bool {
	var coded *protocol.CodedError
	return errors.As(err, &coded) || strings.HasPrefix(err.Error(), "remote: ")
}

// dropConn closes the terminal attached to the tab id, reporting whether
// there was one.
func (m *model) dropConn(id string) bool {
	conn := m.conns[id]
	if conn == nil {
		return false
	}
	conn.close()
	delete(m.conns, id)
	return true
}

// dropConns closes every terminal attached.
func (m *model) dropConns() {
	for id := range m.conns {
		m.dropConn(id)
	}
}
