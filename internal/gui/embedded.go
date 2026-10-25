package gui

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/daemon"
	"github.com/ironpark/sanbo/internal/protocol"
)

// The app can run the daemon itself, in its own process, so a machine needs
// nothing but the app: the daemon mode setting chooses.
//
//	auto      use a sanbod that is already running, else run one in the app
//	embedded  always run the daemon in the app
//	external  only talk to a sanbod started some other way
//
// A daemon in the app owns its sessions' processes, so they end when it
// stops or the app quits; a separate sanbod keeps them running.

// daemonState is what the settings page and the sidebar say about where the
// daemon is. It is comparable, so an unchanged state does not notify.
type daemonState struct {
	Mode     string // what is in use: "embedded", "external", or "" while deciding
	Running  bool   // the app's own daemon is listening
	Starting bool
	Addr     string // the address the window talks through
	Err      string
	Since    time.Time
}

// embeddedDaemon runs one daemon.Daemon at a time inside the app.
type embeddedDaemon struct {
	logs *logRing

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newEmbeddedDaemon() *embeddedDaemon { return &embeddedDaemon{logs: newLogRing(400)} }

// start runs a daemon on cfg. ready is called with a dialable address once
// it listens; exited when Run returns, with its error, including after stop.
// Both run on the daemon's goroutines.
func (e *embeddedDaemon) start(cfg config.Config, ready func(addr string), exited func(err error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		return
	}
	// The daemon gets its own copy: the settings page writes the app's.
	cfg.Agents = maps.Clone(cfg.Agents)
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d := daemon.New(&cfg, log)
	if path, err := config.Path(); err == nil {
		d.FollowAgents(path) // the agents page's saves apply to its next session
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.cancel, e.done = cancel, done
	go func() {
		err := d.Run(ctx)
		e.mu.Lock()
		if e.done == done {
			e.cancel, e.done = nil, nil
		}
		e.mu.Unlock()
		close(done)
		if err != nil {
			log.Error("daemon stopped", "err", err)
		}
		exited(err)
	}()
	go func() {
		select {
		case <-d.Ready():
			ready(dialable(d.Addr()))
		case <-done:
		}
	}()
}

// stop ends the daemon and waits until its sessions are closed.
func (e *embeddedDaemon) stop() {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel, e.done = nil, nil
	e.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (e *embeddedDaemon) running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cancel != nil
}

// dialable turns a listen address into one a client on this machine can
// dial: an unspecified host, as ":4242" listens on, becomes loopback.
func dialable(a net.Addr) string {
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// externalAddr is the sanbod the window looks for: the settings' address,
// else the -daemon flag, else the local default.
func (m *model) externalAddr() string {
	if d := ggui.Peek(m.Prefs).Daemon; d != "" {
		return d
	}
	if m.addr != "" {
		return m.addr
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(config.DefaultPort))
}

// applyDaemonMode brings the daemon in line with the mode setting. It runs
// on the UI goroutine.
func (m *model) applyDaemonMode() {
	switch ggui.Peek(m.Prefs).DaemonMode {
	case "embedded":
		m.startEmbedded()
	case "external":
		m.stopEmbedded()
		m.useExternal()
	default: // auto
		if m.embedded.running() {
			return // already ours; probing the same port would find it
		}
		addr, psk := m.externalAddr(), m.key()
		m.linked.Store(false)
		m.setDaemonState(func(s *daemonState) { s.Mode, s.Err = "", "" })
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			c, err := client.Dial(ctx, addr, psk)
			if err == nil {
				_ = c.Close()
			}
			m.post(func() {
				if ggui.Peek(m.Prefs).DaemonMode != "auto" {
					return // the setting moved on while probing
				}
				if err == nil {
					m.useExternal()
				} else {
					m.startEmbedded()
				}
			})
		}()
	}
}

func (m *model) useExternal() {
	addr := m.externalAddr()
	m.setDaemonState(func(s *daemonState) { *s = daemonState{Mode: "external", Addr: addr} })
	m.setLink("")
}

// startEmbedded runs the app's daemon on the node's configuration and
// points the window at it once it listens.
func (m *model) startEmbedded() {
	if m.embedded.running() {
		return
	}
	m.linked.Store(false)
	m.setDaemonState(func(s *daemonState) { *s = daemonState{Mode: "embedded", Starting: true} })
	m.reconnect(ggui.Peek(m.Prefs).Daemon, m.cfg.PSK) // the key it starts with
	// A daemon stopped for a restart reports its exit after the next one
	// has started; the generation tells the two apart.
	m.daemonGen++
	gen := m.daemonGen
	post := m.post
	m.embedded.start(*m.cfg,
		func(addr string) {
			post(func() {
				if m.daemonGen != gen {
					return
				}
				m.setDaemonState(func(s *daemonState) {
					*s = daemonState{Mode: "embedded", Running: true, Addr: addr, Since: time.Now()}
				})
				m.setLink(addr)
			})
		},
		func(err error) {
			post(func() {
				if m.daemonGen != gen {
					return
				}
				m.setDaemonState(func(s *daemonState) {
					s.Running, s.Starting = false, false
					if err != nil {
						s.Err = err.Error()
					}
				})
			})
		})
}

// stopEmbedded ends the app's daemon, and with it the sessions it runs.
func (m *model) stopEmbedded() {
	if !m.embedded.running() {
		return
	}
	m.daemonGen++
	m.embedded.stop()
	m.unlink(T("The app's daemon is stopped."))
	m.setDaemonState(func(s *daemonState) { s.Running, s.Starting, s.Addr = false, false, "" })
}

func (m *model) restartEmbedded() {
	m.stopEmbedded()
	m.startEmbedded()
}

// embeddedSessions counts the running sessions the app's daemon owns, which
// stopping it would end.
func (m *model) embeddedSessions() int {
	if !ggui.Peek(m.DaemonState).Running {
		return 0
	}
	self := ggui.Peek(m.Conn).Self.ID
	n := 0
	for _, s := range ggui.Peek(m.Sessions) {
		if s.NodeID == self && s.State != protocol.StateExited {
			n++
		}
	}
	return n
}

// guardStop runs fn, which stops the app's daemon, after confirming when
// that would end running sessions.
func (m *model) guardStop(fn func()) {
	if m.embeddedSessions() == 0 {
		fn()
		return
	}
	m.pendingStop = fn
	m.StopOpen.Set(true)
}

func (m *model) confirmedStop() {
	if fn := m.pendingStop; fn != nil {
		m.pendingStop = nil
		fn()
	}
}

// setDaemonMode is the settings toggle's write: leaving the app's daemon
// with sessions in it asks first.
func (m *model) setDaemonMode(mode string) {
	apply := func() { m.Prefs.Update(func(p prefs) prefs { p.DaemonMode = mode; return p }) }
	if mode == "external" && ggui.Peek(m.DaemonState).Running {
		m.guardStop(apply)
		return
	}
	apply()
}

func (m *model) setDaemonState(fn func(*daemonState)) {
	s := ggui.Peek(m.DaemonState)
	fn(&s)
	m.DaemonState.Set(s)
}

// logRing keeps the last lines a daemon logged, for the settings page.
type logRing struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
	max     int
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		l.lines = append(l.lines, string(l.partial[:i]))
		l.partial = l.partial[i+1:]
	}
	if over := len(l.lines) - l.max; over > 0 {
		l.lines = append(l.lines[:0:0], l.lines[over:]...)
	}
	return len(p), nil
}

func (l *logRing) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}
