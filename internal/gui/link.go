package gui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/mako/internal/client"
)

// daemonLink is what the window shares between the UI goroutine and the
// workers that poll and watch its daemon: which daemon to talk to, the
// client dialed to it, and the flags that pace the poll. Every field of it
// is safe to use from any goroutine; the model's other fields are not.
type daemonLink struct {
	addr string // the -daemon flag; Prefs.Daemon overrides it. Set once.

	mu        sync.Mutex // guards the fields below
	cli       *client.Client
	daemon    string // the external daemon's address from the settings
	embedAddr string // the app's own daemon, while it runs

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
	// Access, the workspace and the devices again: at first, and when the
	// daemon says they changed.
	accessStale  atomic.Bool
	wsStale      atomic.Bool
	devicesStale atomic.Bool
	syncing      atomic.Bool // the window follows the other nodes; for the poll
}

func newDaemonLink(addr, daemon string) *daemonLink {
	l := &daemonLink{addr: addr, daemon: daemon, kick: make(chan struct{}, 1)}
	l.accessStale.Store(true)
	l.wsStale.Store(true)
	return l
}

// target is the daemon the GUI talks through: the app's own when it runs
// one, else the external one the settings or the -daemon flag name, else
// the local default.
func (l *daemonLink) target() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.embedAddr != "":
		return l.embedAddr
	case l.daemon != "":
		return l.daemon
	}
	return l.addr
}

// client returns a connected client, dialing on first use or after a failure.
func (l *daemonLink) client(ctx context.Context) (*client.Client, error) {
	addr := l.target()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cli != nil {
		return l.cli, nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := client.Dial(dialCtx, addr)
	if err != nil {
		return nil, err
	}
	l.cli = c
	return c, nil
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

// retarget closes the client and points the link elsewhere: set changes
// the addresses under the lock. The next request dials again.
func (l *daemonLink) retarget(set func(l *daemonLink)) {
	l.mu.Lock()
	c := l.cli
	l.cli = nil
	set(l)
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
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
