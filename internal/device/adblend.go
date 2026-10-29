package device

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// An adb device lent on a borrowing node. Its adb client's connections go
// to the device's node, but for the forwards, which are the borrower's:
//
//   - adb forward listens on a port of the borrower's loopback, as the
//     adb server would on its own, and opens each connection made to it
//     to the device, as the server would: the device's transport, then
//     the forward's remote service.
//   - adb reverse has the device connect to a port the device's node
//     listens on for the borrower (Link.Listen), and carries each
//     connection made there to the borrower's loopback, where the
//     reverse's host side is.
//
// So a forward or reverse listens where the borrower's programs are, as
// it would with the device attached to the borrower.

// Lend lends dev on the borrowing node.
func (a *ADB) Lend(ctx context.Context, dev *protocol.Device, link Link) Lent {
	ctx, cancel := context.WithCancel(ctx)
	return &adbLent{
		ctx: ctx, cancel: cancel, serial: dev.GetSerial(), link: link,
		forwards: map[string]*adbForward{}, reverses: map[string]*adbReverse{},
	}
}

type adbLent struct {
	ctx    context.Context
	cancel context.CancelFunc
	serial string
	link   Link

	mu       sync.Mutex
	forwards map[string]*adbForward // by their local side, "tcp:port"
	reverses map[string]*adbReverse // by their device side
}

// adbForward is a port of the borrower forwarded to a service of the
// device.
type adbForward struct {
	ln     net.Listener
	remote string // guarded by adbLent.mu
}

// adbReverse is a port of the device forwarded to one of the borrower,
// host, through a port of the device's node, ln.
type adbReverse struct {
	host string // "tcp:port" on the borrower
	ln   Listener
}

func (l *adbLent) Close() {
	l.cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	for local, f := range l.forwards {
		f.ln.Close()
		delete(l.forwards, local)
	}
	for dev, r := range l.reverses {
		r.ln.Close()
		delete(l.reverses, dev)
	}
}

// Serve serves one connection of the borrower's adb client.
func (l *adbLent) Serve(ctx context.Context, conn io.ReadWriteCloser) error {
	defer conn.Close()
	req, err := readMessage(conn)
	if err != nil {
		return err
	}
	if done, err := l.forwardRequest(conn, req); done {
		return err
	}
	up, err := l.link.Dial(ctx)
	if err != nil {
		return writeFail(conn, "mako: "+err.Error())
	}
	defer up.Close()
	if err := writeMessage(up, req); err != nil {
		return err
	}
	if strings.HasPrefix(req, "host:transport") || strings.HasPrefix(req, "host:tport:") {
		// The device's services follow the transport, adb reverse's
		// among them.
		if err := relayStatus(conn, up); err != nil {
			return err
		}
		if strings.HasPrefix(req, "host:tport:") {
			if _, err := io.CopyN(conn, up, 8); err != nil { // the transport's ID
				return err
			}
		}
		next, err := readMessage(conn)
		if err != nil {
			return err
		}
		// adb asks for forwards on a connection whose transport it chose
		// first, as it does for the device's services.
		if done, err := l.forwardRequest(conn, next); done {
			return err
		}
		if svc, ok := strings.CutPrefix(next, "reverse:"); ok {
			return l.reverse(ctx, conn, up, svc)
		}
		if err := writeMessage(up, next); err != nil {
			return err
		}
	}
	return transport.Splice(ctx, conn, up)
}

// forwardRequest answers a request of the borrower's forwards, and reports
// whether req was one.
func (l *adbLent) forwardRequest(conn io.Writer, req string) (bool, error) {
	svc, ok := l.hostService(req)
	switch {
	case !ok:
		return false, nil
	case strings.HasPrefix(svc, "forward:"):
		return true, l.forward(conn, strings.TrimPrefix(svc, "forward:"))
	case strings.HasPrefix(svc, "killforward:"):
		return true, l.killForward(conn, strings.TrimPrefix(svc, "killforward:"))
	case svc == "list-forward":
		return true, l.listForwards(conn)
	case svc == "killforward-all":
		return true, l.killForwards(conn)
	}
	return false, nil
}

// hostService is the service a host request asks of the device lent, or
// of any device; not ok for a request of no device, or of another.
func (l *adbLent) hostService(req string) (string, bool) {
	for _, prefix := range []string{"host:", "host-usb:", "host-local:", "host-serial:" + l.serial + ":"} {
		if svc, ok := strings.CutPrefix(req, prefix); ok {
			return svc, true
		}
	}
	if rest, ok := strings.CutPrefix(req, "host-transport-id:"); ok {
		_, svc, ok := strings.Cut(rest, ":")
		return svc, ok
	}
	return "", false
}

// tcpPort is the port of a "tcp:port" socket spec.
func tcpPort(spec string) (int, bool) {
	p, ok := strings.CutPrefix(spec, "tcp:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	return n, err == nil && n >= 0 && n <= 65535
}

// okays answers a host request as the adb server does a forward: OKAY for
// the transport, OKAY for the request, then a message if one is given.
func okays(w io.Writer, msg ...string) error {
	if _, err := io.WriteString(w, "OKAYOKAY"); err != nil {
		return err
	}
	for _, m := range msg {
		if err := writeMessage(w, m); err != nil {
			return err
		}
	}
	return nil
}

// forward forwards a port of the borrower to a service of the device,
// "[norebind:]tcp:port;remote".
func (l *adbLent) forward(conn io.Writer, spec string) error {
	spec, norebind := strings.CutPrefix(spec, "norebind:")
	local, remote, ok := strings.Cut(spec, ";")
	port, isTCP := tcpPort(local)
	switch {
	case !ok:
		return writeFail(conn, "malformed forward spec '"+spec+"'")
	case !isTCP:
		return writeFail(conn, "mako forwards TCP ports, not "+local)
	}
	l.mu.Lock()
	if f, ok := l.forwards[local]; ok && port != 0 {
		if norebind {
			l.mu.Unlock()
			return writeFail(conn, "cannot rebind existing socket")
		}
		f.remote = remote
		l.mu.Unlock()
		return okays(conn, strconv.Itoa(port))
	}
	l.mu.Unlock()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return writeFail(conn, "cannot bind listener: "+err.Error())
	}
	port = ln.Addr().(*net.TCPAddr).Port
	f := &adbForward{ln: ln, remote: remote}
	l.mu.Lock()
	if l.ctx.Err() != nil {
		l.mu.Unlock()
		ln.Close()
		return writeFail(conn, "mako: the device was given back")
	}
	l.forwards["tcp:"+strconv.Itoa(port)] = f
	l.mu.Unlock()
	go l.serveForward(f)
	return okays(conn, strconv.Itoa(port))
}

// serveForward opens each connection made to a forward's port to the
// device's service, as the adb server does.
func (l *adbLent) serveForward(f *adbForward) {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		remote := f.remote
		l.mu.Unlock()
		go func() {
			defer c.Close()
			up, err := l.openService(remote)
			if err != nil {
				return
			}
			_ = transport.Splice(l.ctx, c, up)
		}()
	}
}

// openService opens a service of the device: its transport, then the
// service.
func (l *adbLent) openService(service string) (io.ReadWriteCloser, error) {
	up, err := l.link.Dial(l.ctx)
	if err != nil {
		return nil, err
	}
	for _, req := range []string{"host:transport:" + l.serial, service} {
		if err := writeMessage(up, req); err != nil {
			up.Close()
			return nil, err
		}
		if err := readStatus(up); err != nil {
			up.Close()
			return nil, err
		}
	}
	return up, nil
}

func (l *adbLent) killForward(conn io.Writer, local string) error {
	l.mu.Lock()
	f, ok := l.forwards[local]
	delete(l.forwards, local)
	l.mu.Unlock()
	if !ok {
		return writeFail(conn, "listener '"+local+"' not found")
	}
	f.ln.Close()
	return okays(conn)
}

func (l *adbLent) killForwards(conn io.Writer) error {
	l.mu.Lock()
	for local, f := range l.forwards {
		f.ln.Close()
		delete(l.forwards, local)
	}
	l.mu.Unlock()
	return okays(conn)
}

func (l *adbLent) listForwards(conn io.Writer) error {
	l.mu.Lock()
	var b strings.Builder
	for _, local := range slices.SortedFunc(maps.Keys(l.forwards), compareSpecs) {
		fmt.Fprintf(&b, "%s %s %s\n", l.serial, local, l.forwards[local].remote)
	}
	l.mu.Unlock()
	if _, err := io.WriteString(conn, "OKAY"); err != nil {
		return err
	}
	return writeMessage(conn, b.String())
}

// compareSpecs orders socket specs by their port.
func compareSpecs(a, b string) int {
	pa, _ := tcpPort(a)
	pb, _ := tcpPort(b)
	return cmp.Or(cmp.Compare(pa, pb), cmp.Compare(a, b))
}

// reverse serves a reverse service on a connection whose transport is
// chosen: up, to the device's node, has had the transport's OKAY.
func (l *adbLent) reverse(ctx context.Context, conn, up io.ReadWriteCloser, svc string) error {
	switch {
	case strings.HasPrefix(svc, "forward:"):
		return l.reverseForward(ctx, conn, up, strings.TrimPrefix(svc, "forward:"))
	case svc == "list-forward":
		return l.listReverses(conn, up)
	case strings.HasPrefix(svc, "killforward:"):
		dev := strings.TrimPrefix(svc, "killforward:")
		return l.killReverses(conn, up, "reverse:"+svc, func(d string) bool { return d == dev })
	case svc == "killforward-all":
		return l.killReverses(conn, up, "reverse:"+svc, func(string) bool { return true })
	}
	if err := writeMessage(up, "reverse:"+svc); err != nil {
		return err
	}
	return transport.Splice(ctx, conn, up)
}

// reverseForward has the device forward its port dev to the borrower's
// host, "[norebind:]dev;tcp:port", through a port its node listens on.
func (l *adbLent) reverseForward(ctx context.Context, conn, up io.ReadWriteCloser, spec string) error {
	rest, norebind := strings.CutPrefix(spec, "norebind:")
	dev, host, ok := strings.Cut(rest, ";")
	if _, isTCP := tcpPort(host); !ok || !isTCP {
		// Not a port of the borrower's: the device's node's own socket.
		if err := writeMessage(up, "reverse:forward:"+spec); err != nil {
			return err
		}
		return transport.Splice(ctx, conn, up)
	}
	ln, err := l.link.Listen(ctx)
	if err != nil {
		return writeFail(conn, "mako: "+err.Error())
	}
	_, there, _ := net.SplitHostPort(ln.Addr())
	req := "reverse:forward:"
	if norebind {
		req += "norebind:"
	}
	if err := writeMessage(up, req+dev+";tcp:"+there); err != nil {
		ln.Close()
		return err
	}
	port, err := relayOkays(conn, up, true)
	if err != nil {
		ln.Close()
		return err
	}
	if dev == "tcp:0" {
		dev = "tcp:" + port
	}
	r := &adbReverse{host: host, ln: ln}
	l.mu.Lock()
	old := l.reverses[dev]
	l.reverses[dev] = r
	closed := l.ctx.Err() != nil
	l.mu.Unlock()
	if old != nil {
		old.ln.Close()
	}
	if closed {
		ln.Close()
		return nil
	}
	go l.serveReverse(r)
	return nil
}

// relayOkays passes on the two statuses the device's node answers a
// forward with, and with port the port that follows them, which it
// returns; a FAIL is passed on as one.
func relayOkays(conn io.Writer, up io.Reader, port bool) (string, error) {
	for range 2 {
		if err := readStatus(up); err != nil {
			var f failure
			if errors.As(err, &f) {
				_ = writeFail(conn, string(f))
			}
			return "", err
		}
	}
	if !port {
		return "", okays(conn)
	}
	p, err := readMessage(up)
	if err != nil {
		return "", err
	}
	return p, okays(conn, p)
}

// serveReverse carries each connection the device made to a reverse to
// the borrower's port.
func (l *adbLent) serveReverse(r *adbReverse) {
	port, _ := tcpPort(r.host)
	for {
		c, err := r.ln.Accept(l.ctx)
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			here, err := dialLoopback(l.ctx, port)
			if err != nil {
				return
			}
			_ = transport.Splice(l.ctx, c, here)
		}()
	}
}

// dialLoopback connects to port on this machine's loopback, IPv4 first,
// as "localhost" means to most servers, then IPv6.
func dialLoopback(ctx context.Context, port int) (net.Conn, error) {
	var d net.Dialer
	var first error
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return c, nil
		}
		first = cmp.Or(first, err)
	}
	return nil, first
}

// listReverses lists the device's reverses, each with the borrower's port
// it reaches in place of its node's.
func (l *adbLent) listReverses(conn, up io.ReadWriteCloser) error {
	if err := writeMessage(up, "reverse:list-forward"); err != nil {
		return err
	}
	if err := relayStatus(conn, up); err != nil {
		return err
	}
	list, err := readMessage(up)
	if err != nil {
		return err
	}
	l.mu.Lock()
	var b strings.Builder
	for line := range strings.Lines(list) {
		f := strings.Fields(line)
		if len(f) == 3 {
			if r, ok := l.reverses[f[1]]; ok {
				f[2] = r.host
			}
			line = strings.Join(f, " ") + "\n"
		}
		b.WriteString(line)
	}
	l.mu.Unlock()
	return writeMessage(conn, b.String())
}

// killReverses sends req, which removes the reverses whose device side
// matches, and closes those of them it made.
func (l *adbLent) killReverses(conn, up io.ReadWriteCloser, req string, match func(string) bool) error {
	if err := writeMessage(up, req); err != nil {
		return err
	}
	if _, err := relayOkays(conn, up, false); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for dev, r := range l.reverses {
		if match(dev) {
			r.ln.Close()
			delete(l.reverses, dev)
		}
	}
	return nil
}
