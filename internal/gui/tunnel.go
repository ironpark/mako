package gui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// A web address on another node's loopback, which a session there prints
// for the server it started, opens in this machine's browser through a
// tunnel: the window listens on the same port here when it is free, else
// on another, and carries each connection made to it to that port on the
// node, through the daemons. A tunnel stays until it is stopped from the
// sidebar's menu of them, or the window closes.

// tunnelInfo is a port of a node forwarded to one here.
type tunnelInfo struct {
	Node  string
	Port  int // on the node
	Local int // on this machine's loopback
}

// tunnelKey is the port of a node a tunnel reaches.
type tunnelKey struct {
	Node string
	Port int
}

// tunnel is a listener here that carries what reaches it to a node's port.
type tunnel struct {
	ln    net.Listener
	local int
}

// tunnelState is the tunnels open, and the dialog that opens one by hand.
type tunnelState struct {
	List *ggui.StateValue[[]tunnelInfo] // what is open, for the window; set as it changes

	mu   sync.Mutex
	open map[tunnelKey]tunnel

	AskOpen *ggui.StateValue[bool]
	Port    *ggui.StateValue[string]
	Err     *ggui.StateValue[string]
	askNode string
}

func newTunnelState() *tunnelState {
	return &tunnelState{
		List:    ggui.State[[]tunnelInfo](nil),
		open:    map[tunnelKey]tunnel{},
		AskOpen: ggui.State(false),
		Port:    ggui.State(""),
		Err:     ggui.State(""),
	}
}

// tunnelTimeout bounds reaching a node's port, for each connection.
const tunnelTimeout = 10 * time.Second

// loopbackHost reports whether host names the machine it is on: localhost,
// a name under it, a loopback address, or the any-address a server
// listening on every interface prints.
func loopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// webPort is the port a web address names, or its scheme's.
func webPort(u *url.URL) int {
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	switch u.Scheme {
	case "https", "wss":
		return 443
	case "http", "ws":
		return 80
	}
	return 0
}

// openWeb opens a web address a session on node printed: through a tunnel
// when it is on that node's loopback and the node is another, else as it
// is.
func (m *model) openWeb(node, raw string) {
	u, err := url.Parse(raw)
	port := 0
	if err == nil && loopbackHost(u.Hostname()) {
		port = webPort(u)
	}
	if port == 0 || m.onThisMachine(node) || m.canTransfer(node) != nil {
		_ = runtime.OpenURL(raw)
		return
	}
	m.forward(node, port, func(t tunnelInfo) {
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" {
			host = "localhost" // the tunnel listens on IPv4's loopback, which this reaches
		}
		u.Host = net.JoinHostPort(host, strconv.Itoa(t.Local))
		_ = runtime.OpenURL(u.String())
	})
}

// forward forwards port of node to one here, unless it is already, and
// hands done the tunnel on the UI goroutine. A port nothing answers on on
// the node is not forwarded; a toast says so.
func (m *model) forward(node string, port int, done func(tunnelInfo)) {
	ts := m.Tunnels
	ts.mu.Lock()
	open, ok := ts.open[tunnelKey{node, port}]
	ts.mu.Unlock()
	if ok {
		done(tunnelInfo{node, port, open.local})
		return
	}
	where := nodeName(ggui.Peek(m.Nodes), node)
	if err := m.canTransfer(node); err != nil {
		m.toaster.Push(ui.Toast(Tf("Could not reach port %d on %s", port, where), nodeErr(err)).Destructive())
		return
	}
	go func() {
		t, err := m.listen(node, port)
		m.post(func() {
			if err != nil {
				m.toaster.Push(ui.Toast(Tf("Could not reach port %d on %s", port, where), err.Error()).Destructive())
				return
			}
			ts.List.Set(m.tunnelList())
			body := Tf("localhost:%d here reaches it.", t.Local)
			if t.Local != port {
				body = Tf("Port %d is taken here; localhost:%d reaches it.", port, t.Local)
			}
			m.toaster.Push(ui.Toast(Tf("Port %d of %s is forwarded", port, where), body))
			done(t)
		})
	}()
}

// listen opens a tunnel to port of node, once a connection there says
// something answers on it.
func (m *model) listen(node string, port int) (tunnelInfo, error) {
	probe, err := m.dialTunnel(node, port)
	if err != nil {
		return tunnelInfo{}, errors.New(nodeErr(err))
	}
	probe.Close()
	ts := m.Tunnels
	ts.mu.Lock()
	defer ts.mu.Unlock()
	key := tunnelKey{node, port}
	if open, ok := ts.open[key]; ok { // opened meanwhile
		return tunnelInfo{node, port, open.local}, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil { // taken here: any port will do
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return tunnelInfo{}, err
		}
	}
	t := tunnelInfo{Node: node, Port: port, Local: ln.Addr().(*net.TCPAddr).Port}
	ts.open[key] = tunnel{ln, t.Local}
	go m.serveTunnel(ln, t)
	return t, nil
}

// dialTunnel connects to port on node, through the daemon.
func (m *model) dialTunnel(node string, port int) (io.ReadWriteCloser, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tunnelTimeout)
	defer cancel()
	c, err := m.client(ctx)
	if err != nil {
		return nil, err
	}
	return c.Tunnel(ctx, &protocol.Tunnel{NodeId: node, Port: int32(port)})
}

// serveTunnel carries each connection made to ln to port of node, until
// ln is closed.
func (m *model) serveTunnel(ln net.Listener, t tunnelInfo) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			far, err := m.dialTunnel(t.Node, t.Port)
			if err != nil {
				conn.Close()
				return
			}
			_ = transport.Splice(context.Background(), conn, far)
		}()
	}
}

// tunnelList is the tunnels open, by node and port.
func (m *model) tunnelList() []tunnelInfo {
	ts := m.Tunnels
	ts.mu.Lock()
	defer ts.mu.Unlock()
	list := make([]tunnelInfo, 0, len(ts.open))
	for k, t := range ts.open {
		list = append(list, tunnelInfo{k.Node, k.Port, t.local})
	}
	slices.SortFunc(list, func(a, b tunnelInfo) int {
		return cmp.Or(strings.Compare(a.Node, b.Node), cmp.Compare(a.Port, b.Port))
	})
	return list
}

// stopTunnel stops listening for t; the connections it carries run on
// until they end.
func (m *model) stopTunnel(t tunnelInfo) {
	ts := m.Tunnels
	ts.mu.Lock()
	key := tunnelKey{t.Node, t.Port}
	if open, ok := ts.open[key]; ok {
		open.ln.Close()
		delete(ts.open, key)
	}
	ts.mu.Unlock()
	ts.List.Set(m.tunnelList())
}

// closeTunnels stops every tunnel, as the window closes.
func (m *model) closeTunnels() {
	ts := m.Tunnels
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for k, t := range ts.open {
		t.ln.Close()
		delete(ts.open, k)
	}
}

// askTunnel asks which port of node to forward.
func (m *model) askTunnel(node string) {
	ts := m.Tunnels
	ts.askNode = node
	ts.Port.Set("")
	ts.Err.Set("")
	ts.AskOpen.Set(true)
}

// submitTunnel forwards the port asked for, and opens it in the browser.
func (m *model) submitTunnel() {
	ts := m.Tunnels
	port, err := strconv.Atoi(strings.TrimSpace(ggui.Peek(ts.Port)))
	if err != nil || port <= 0 || port > 65535 {
		ts.Err.Set(T("Enter a port, from 1 to 65535."))
		return
	}
	ts.AskOpen.Set(false)
	m.forward(ts.askNode, port, func(t tunnelInfo) { openLocal(t.Local) })
}

// tunnelsMenu is the sidebar's menu of the ports forwarded, there while
// any is.
func tunnelsMenu(m *model) ggui.Widget {
	return ggui.View(m.Tunnels.List, func(list []tunnelInfo) ggui.Widget {
		if len(list) == 0 {
			return ggui.Box()
		}
		nodes := ggui.Peek(m.Nodes)
		var items []ggui.Widget
		for i, t := range list {
			if i > 0 {
				items = append(items, ui.MenuDivider())
			}
			name := fmt.Sprintf("%s:%d", nodeName(nodes, t.Node), t.Port)
			items = append(items,
				ui.MenuItem(Tf("Open %s (localhost:%d)", name, t.Local), func() { openLocal(t.Local) }),
				ui.MenuItem(Tf("Copy localhost:%d", t.Local), func() { ggui.CurrentClipboard().Write(fmt.Sprintf("localhost:%d", t.Local)) }),
				ui.MenuItem(Tf("Stop forwarding %s", name), func() { m.stopTunnel(t) }),
			)
		}
		return iconMenu(ui.MenuOf(icon("arrow-right-left"), items...).Name(Tf("%d ports forwarded", len(list))), 6)
	})
}

// tunnelDialog asks for a port to forward.
func tunnelDialog(m *model) ggui.Widget {
	ts := m.Tunnels
	return nameDialog(nameAsk{Open: ts.AskOpen, Text: ts.Port, Err: ts.Err,
		Title: T("Forward a port"), Field: T("Port"), Label: T("Port"), Action: T("Forward"), Placeholder: "3000",
		Note:   T("A server on the node's own loopback opens here, in the browser."),
		Submit: m.submitTunnel})
}

// openLocal opens a port of this machine's loopback in the browser.
func openLocal(port int) { _ = runtime.OpenURL(fmt.Sprintf("http://localhost:%d", port)) }
