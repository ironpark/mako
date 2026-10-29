package device

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// ADB lends the Android devices and emulators of the node's adb server. A
// borrower's adb client talks to the server through mako, which answers
// it as a server with that one device: it lists that device alone, sends
// to it what the client meant for any device, refuses another, and keeps
// the client from stopping the server, which other programs use. See
// docs/devices.md.
type ADB struct {
	// Server is the adb server's address, host:port; empty is 127.0.0.1
	// at ANDROID_ADB_SERVER_PORT, or 5037.
	Server string

	mu      sync.Mutex
	started time.Time // when this last tried to start the server; guarded by mu
}

// adbDial is how long reaching the adb server takes at most, and
// adbStartEvery how often List tries to start one that is not running.
const (
	adbDial       = 5 * time.Second
	adbStartEvery = 30 * time.Second
)

func (a *ADB) Exclusive() bool { return false }

func (a *ADB) addr() string {
	if a.Server != "" {
		return a.Server
	}
	return net.JoinHostPort("127.0.0.1", cmp.Or(os.Getenv("ANDROID_ADB_SERVER_PORT"), "5037"))
}

// errNoServer is no adb server answering.
var errNoServer = errors.New("no adb server is running")

func (a *ADB) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: adbDial}
	conn, err := d.DialContext(ctx, "tcp", a.addr())
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", errNoServer, a.addr(), err)
	}
	return conn, nil
}

// query asks the server a host request that answers with one message.
func (a *ADB) query(ctx context.Context, req string) (string, error) {
	conn, err := a.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := writeMessage(conn, req); err != nil {
		return "", err
	}
	if err := readStatus(conn); err != nil {
		return "", err
	}
	return readMessage(conn)
}

// List asks the server for its devices, and starts it first if it is not
// running and this node's adb is the default one.
func (a *ADB) List(ctx context.Context) ([]*protocol.Device, error) {
	version, err := a.query(ctx, "host:version")
	if errors.Is(err, errNoServer) && a.start(ctx) {
		version, err = a.query(ctx, "host:version")
	}
	if errors.Is(err, errNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list, err := a.query(ctx, "host:devices-l")
	if err != nil {
		return nil, err
	}
	devices := parseDevices(list)
	if v, err := strconv.ParseUint(version, 16, 32); err == nil {
		for _, d := range devices {
			d.Attrs["adb_version"] = strconv.FormatUint(v, 10)
		}
	}
	return devices, nil
}

// start runs `adb start-server`, at most every adbStartEvery, when the
// server is the default one; it reports whether it ran.
func (a *ADB) start(ctx context.Context) bool {
	if a.Server != "" {
		return false
	}
	a.mu.Lock()
	due := time.Since(a.started) >= adbStartEvery
	if due {
		a.started = time.Now()
	}
	a.mu.Unlock()
	if !due {
		return false
	}
	adb, err := findADB()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, adb, "start-server").Run() == nil
}

// findADB is where this node's adb is: in the SDK ANDROID_HOME or
// ANDROID_SDK_ROOT names, else on the PATH, else where Android Studio puts
// the SDK.
func findADB() (string, error) {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(env); v != "" {
			if p, err := exec.LookPath(filepath.Join(v, "platform-tools", "adb")); err == nil {
				return p, nil
			}
		}
	}
	return agent.Find("adb",
		"Library/Android/sdk/platform-tools",       // macOS
		"Android/Sdk/platform-tools",               // Linux
		"AppData/Local/Android/Sdk/platform-tools") // Windows
}

// parseDevices reads `host:devices-l`: a line a device, its serial and
// state, then key:value pairs.
func parseDevices(list string) []*protocol.Device {
	var out []*protocol.Device
	for line := range strings.Lines(list) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		d := &protocol.Device{Kind: protocol.DeviceADB, Serial: f[0], State: f[1]}
		props := map[string]string{}
		for _, kv := range f[2:] {
			if k, v, ok := strings.Cut(kv, ":"); ok {
				props[k] = v
			}
		}
		d.Name = strings.ReplaceAll(cmp.Or(props["model"], props["product"], d.Serial), "_", " ")
		d.Attrs = map[string]string{}
		if id := props["transport_id"]; id != "" {
			d.Attrs["transport_id"] = id
		}
		out = append(out, d)
	}
	return out
}

// transportID is the transport ID the server gives serial now.
func (a *ADB) transportID(ctx context.Context, serial string) (string, error) {
	list, err := a.query(ctx, "host:devices-l")
	if err != nil {
		return "", err
	}
	for _, d := range parseDevices(list) {
		if d.GetSerial() == serial {
			return d.GetAttrs()["transport_id"], nil
		}
	}
	return "", fmt.Errorf("%s is not attached", serial)
}

func (a *ADB) Env(addr string, dev *protocol.Device) map[string]string {
	env := map[string]string{
		"ADB_SERVER_SOCKET": "tcp:" + addr,
		"ANDROID_SERIAL":    dev.GetSerial(),
	}
	// Tools that find the server by its port alone, as Android Studio's
	// manually managed server does, look on localhost.
	if _, port, err := net.SplitHostPort(addr); err == nil {
		env["ANDROID_ADB_SERVER_PORT"] = port
	}
	return env
}

// adbVersion reads the protocol version from `adb version`: the last
// number of "Android Debug Bridge version 1.0.41".
var adbVersion = regexp.MustCompile(`Android Debug Bridge version \d+\.\d+\.(\d+)`)

// Check compares this node's adb with the device's server: an adb client
// that finds a server of another version stops it to start its own, which
// mako refuses, so the client fails.
func (a *ADB) Check(ctx context.Context, dev *protocol.Device) []string {
	theirs := dev.GetAttrs()["adb_version"]
	if theirs == "" {
		return nil
	}
	adb, err := findADB()
	if err != nil {
		return []string{"adb is not installed on this node; only tools that bring their own will reach the device"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adb, "version").Output()
	m := adbVersion.FindSubmatch(out)
	if err != nil || m == nil {
		return nil
	}
	if ours := string(m[1]); ours != theirs {
		return []string{fmt.Sprintf("this node's adb speaks version %s and the device's adb server %s: adb here will fail to use it, as it would stop that server to start its own, which mako refuses; use platform-tools of the same version on both nodes", ours, theirs)}
	}
	return nil
}

// Serve answers one connection of a borrower's adb client: it reads the
// client's request, changes it to be for serial, or refuses it, and from
// there carries the connection to the server, filtering the device lists
// the server sends back.
func (a *ADB) Serve(ctx context.Context, serial string, conn io.ReadWriteCloser) error {
	defer conn.Close()
	req, err := readMessage(conn)
	if err != nil {
		return err
	}
	p, err := plan(req, serial, func() (string, error) { return a.transportID(ctx, serial) })
	if err != nil {
		return writeFail(conn, "mako: "+err.Error())
	}
	server, err := a.dial(ctx)
	if err != nil {
		return writeFail(conn, "mako: "+err.Error())
	}
	defer server.Close()
	if p.selectFirst {
		// A device service asked of no transport goes to any device; it
		// is sent to serial's.
		if err := writeMessage(server, "host:transport:"+serial); err != nil {
			return err
		}
		if err := readStatus(server); err != nil {
			return writeFail(conn, err.Error())
		}
	}
	if err := writeMessage(server, p.req); err != nil {
		return err
	}
	if p.filter == nil {
		return transport.Splice(ctx, conn, server)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close(); server.Close() })
	defer stop()
	if p.track {
		// A listing kept up to date ends when the client goes.
		go func() { _, _ = io.Copy(io.Discard, conn); server.Close() }()
	}
	if err := relayStatus(conn, server); err != nil {
		return err
	}
	for {
		msg, err := readMessage(server)
		if err != nil {
			if p.track {
				return nil
			}
			return err
		}
		if err := writeMessage(conn, p.filter(msg)); err != nil || !p.track {
			return err
		}
	}
}

// relayStatus passes the server's OKAY on to the client, or its FAIL as
// an error.
func relayStatus(client io.Writer, server io.Reader) error {
	if err := readStatus(server); err != nil {
		var f failure
		if errors.As(err, &f) {
			_ = writeFail(client, string(f))
		}
		return err
	}
	_, err := io.WriteString(client, "OKAY")
	return err
}

// adbPlan is what to send the server for a client's request, and what to
// do with what comes back.
type adbPlan struct {
	req         string
	selectFirst bool // select serial's transport first
	// filter, when not nil, is applied to the message the server answers
	// with, or with track to each message it sends until it closes.
	filter func(string) string
	track  bool
}

// deviceServices are the host requests for one device, which a request
// of no device sends to any.
var deviceServices = []string{"get-state", "get-serialno", "get-devpath", "features", "reconnect", "forward:", "killforward:", "wait-for-"}

// plan is how a client's request goes to the server, as one for serial
// alone, or why it does not.
func plan(req, serial string, transportID func() (string, error)) (adbPlan, error) {
	const hostSerial = "host-serial:"
	onSerial := func(rest string) adbPlan { return adbPlan{req: hostSerial + serial + ":" + rest} }
	sameID := func(id string) (adbPlan, error) {
		ours, err := transportID()
		if err != nil {
			return adbPlan{}, err
		}
		if id != ours {
			return adbPlan{}, fmt.Errorf("transport %s is not the device lent, %s", id, serial)
		}
		return adbPlan{req: req}, nil
	}
	notLent := fmt.Errorf("only %s is lent to this node", serial)

	switch req {
	case "host:kill":
		return adbPlan{}, errors.New("the adb server belongs to the device's node, and is not stopped from here")
	case "host:killforward-all":
		return adbPlan{}, errors.New("that would remove every device's forwards on the device's node; remove them one at a time")
	case "host:devices", "host:devices-l", "host:list-forward":
		return adbPlan{req: req, filter: keepLines(serial)}, nil
	case "host:track-devices", "host:track-devices-l":
		return adbPlan{req: req, filter: keepLines(serial), track: true}, nil
	case "host:track-devices-proto-binary":
		return adbPlan{req: req, filter: keepProto(serial), track: true}, nil
	case "host:transport:" + serial, "host:tport:serial:" + serial:
		return adbPlan{req: req}, nil
	case "host:transport-any", "host:transport-usb", "host:transport-local":
		return adbPlan{req: "host:transport:" + serial}, nil
	case "host:tport:any", "host:tport:usb", "host:tport:local":
		return adbPlan{req: "host:tport:serial:" + serial}, nil
	}
	switch {
	case strings.HasPrefix(req, "host:track-devices"):
		return adbPlan{}, errors.New("this listing of devices is not filtered; use track-devices or its binary form")
	case strings.HasPrefix(req, "host:transport-id:"):
		return sameID(strings.TrimPrefix(req, "host:transport-id:"))
	case strings.HasPrefix(req, "host:transport:"), strings.HasPrefix(req, "host:tport:"):
		return adbPlan{}, notLent
	case strings.HasPrefix(req, hostSerial+serial+":"):
		return adbPlan{req: req}, nil
	case strings.HasPrefix(req, hostSerial):
		return adbPlan{}, notLent
	case strings.HasPrefix(req, "host-usb:"), strings.HasPrefix(req, "host-local:"):
		_, rest, _ := strings.Cut(req, ":")
		return onSerial(rest), nil
	case strings.HasPrefix(req, "host-transport-id:"):
		id, _, ok := strings.Cut(strings.TrimPrefix(req, "host-transport-id:"), ":")
		if !ok {
			return adbPlan{}, fmt.Errorf("%q names no request", req)
		}
		return sameID(id)
	case strings.HasPrefix(req, "host:"):
		cmd := strings.TrimPrefix(req, "host:")
		for _, s := range deviceServices {
			prefix := strings.HasSuffix(s, ":") || strings.HasSuffix(s, "-")
			if cmd == s || prefix && strings.HasPrefix(cmd, s) {
				return onSerial(cmd), nil
			}
		}
		return adbPlan{req: req}, nil
	}
	return adbPlan{req: req, selectFirst: true}, nil
}

// keepLines keeps the lines of a listing that are serial's: those whose
// first field it is.
func keepLines(serial string) func(string) string {
	return func(list string) string {
		var b strings.Builder
		for line := range strings.Lines(list) {
			if f := strings.Fields(line); len(f) > 0 && f[0] == serial {
				b.WriteString(line)
			}
		}
		return b.String()
	}
}
