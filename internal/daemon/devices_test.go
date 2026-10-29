package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/device"
	"github.com/ironpark/mako/internal/device/adbfake"
	"github.com/ironpark/mako/internal/protocol"
)

// withADB gives d an adb server of its own, with devices.
func withADB(t *testing.T, d *Daemon, share bool, devices ...string) *adbfake.Server {
	srv := adbfake.Start(t, devices...)
	d.kinds[protocol.DeviceADB] = &device.ADB{Server: srv.Addr}
	if share {
		d.cfg.Devices.Share = []string{protocol.DeviceADB}
	}
	return srv
}

// A node borrows a device of another's, which its programs reach at the
// lease's port as the one device there; the device's node lists it as
// lent, until it is given back.
func TestANodeBorrowsAnothersDevice(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, b, a) // a drives b
	letIn(t, a, b)
	withADB(t, a, false)
	srv := withADB(t, b, true, "S0", "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "a linked to b", func() bool { n := nodeOf(ctx, t, c, b.cfg.NodeID); return n != nil && n.GetTrustsUs() })

	list, err := c.Devices(ctx, &protocol.ListDevices{Mesh: true})
	if err != nil || len(list.GetDevices()) != 2 || list.GetDevices()[1].Ref() != protocol.DeviceRef(b.cfg.NodeID, "adb", "S1") || !list.GetDevices()[1].GetShared() {
		t.Fatalf("listed %v, %v", list, err)
	}

	lease, err := c.Borrow(ctx, &protocol.Borrow{DeviceNodeId: b.cfg.NodeID, Kind: "adb", Serial: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.GetEnv()["ANDROID_SERIAL"] != "S1" || lease.GetEnv()["ADB_SERVER_SOCKET"] != "tcp:"+lease.GetAddr() {
		t.Fatalf("lent with %v", lease.GetEnv())
	}
	again, err := c.Borrow(ctx, &protocol.Borrow{DeviceNodeId: b.cfg.NodeID, Kind: "adb", Serial: "S1"})
	if err != nil || again.GetId() != lease.GetId() {
		t.Fatalf("borrowed again as %v, %v", again, err)
	}

	got, err := adbfake.Request(lease.GetAddr(), "host:devices")
	if err != nil || got != "OKAY000aS1\tdevice\n" {
		t.Fatalf("devices: %q, %v", got, err)
	}
	got, _ = adbfake.Request(lease.GetAddr(), "host:transport-any", "shell:echo hi")
	if got != "OKAYOKAYS1:echo hi" {
		t.Fatalf("shell: %q", got)
	}
	if got, _ := adbfake.Request(lease.GetAddr(), "host:kill"); !strings.HasPrefix(got, "FAIL") || srv.Killed() {
		t.Fatalf("kill: %q", got)
	}

	dev := b.localDevices(ctx).GetDevices()[1]
	if len(dev.GetHolders()) != 1 || dev.GetHolders()[0].GetName() != "a" {
		t.Fatalf("b lends S1 to %v", dev.GetHolders())
	}
	leases, err := c.Leases(ctx, &protocol.ListLeases{})
	if err != nil || len(leases.GetLeases()) != 1 || leases.GetLeases()[0].GetState() != protocol.LeaseHeld {
		t.Fatalf("leases %v, %v", leases, err)
	}

	// The claim's stream ends, as when the network drops: a claims the
	// device again, under the same claim, which b resumes.
	a.leases.mu.Lock()
	a.leases.by[lease.GetId()].stream.Close()
	a.leases.mu.Unlock()
	waitFor(t, "a did not see the claim end", func() bool {
		l, err := c.Leases(ctx, &protocol.ListLeases{})
		return err == nil && l.GetLeases()[0].GetState() == protocol.LeaseReconnecting
	})
	waitForWithin(t, 10*time.Second, "a did not claim S1 again", func() bool {
		l, err := c.Leases(ctx, &protocol.ListLeases{})
		return err == nil && l.GetLeases()[0].GetState() == protocol.LeaseHeld && func() bool {
			b.claims.mu.Lock()
			defer b.claims.mu.Unlock()
			return len(b.claims.by) == 1 && b.claims.by[a.leases.by[lease.GetId()].claim.GetClaimId()].live == 1
		}()
	})
	if got, _ := adbfake.Request(lease.GetAddr(), "host:transport-any", "shell:id"); got != "OKAYOKAYS1:id" {
		t.Fatalf("after claiming again: %q", got)
	}

	back, err := c.GiveBack(ctx, &protocol.GiveBack{LeaseId: lease.GetId()})
	if err != nil || back.GetState() != protocol.LeaseReturned {
		t.Fatalf("given back as %v, %v", back, err)
	}
	if _, err := adbfake.Request(lease.GetAddr(), "host:devices"); err == nil {
		t.Error("the port still answers once the device was given back")
	}
	// The claim lasts its grace after its stream ends; given back, the
	// stream ended.
	waitFor(t, "b still holds the claim's stream", func() bool {
		b.claims.mu.Lock()
		defer b.claims.mu.Unlock()
		for _, cl := range b.claims.by {
			if cl.live > 0 {
				return false
			}
		}
		return true
	})
}

// A node lends only the kinds its configuration shares, and only devices
// attached to it.
func TestANodeLendsOnlyWhatItShares(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	withADB(t, b, false, "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "a linked to b", func() bool { n := nodeOf(ctx, t, c, b.cfg.NodeID); return n != nil && n.GetTrustsUs() })

	_, err := c.Borrow(ctx, &protocol.Borrow{DeviceNodeId: b.cfg.NodeID, Kind: "adb", Serial: "S1"})
	if err == nil || !strings.Contains(err.Error(), "devices.share") {
		t.Fatalf("borrowed what b does not share: %v", err)
	}
	b.cfg.Devices.Share = []string{protocol.DeviceADB}
	if _, err := c.Borrow(ctx, &protocol.Borrow{DeviceNodeId: b.cfg.NodeID, Kind: "adb", Serial: "S9"}); err == nil {
		t.Fatal("borrowed a device not attached")
	}
}

// A device of the node itself is lent through the same port, with no
// peer between.
func TestANodeBorrowsItsOwnDevice(t *testing.T) {
	a := meshDaemon(t, "a")
	withADB(t, a, true, "S0", "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease, err := c.Borrow(ctx, &protocol.Borrow{Kind: "adb", Serial: "S0"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := adbfake.Request(lease.GetAddr(), "host:devices"); err != nil || got != "OKAY000aS0\tdevice\n" {
		t.Fatalf("devices: %q, %v", got, err)
	}
}

// adbPort is the port at the end of an adb forward's answer.
func adbPort(t *testing.T, answer string) string {
	t.Helper()
	i := strings.LastIndex(answer, "OKAYOKAY")
	if i < 0 || len(answer) < i+12 {
		t.Fatalf("no port in %q", answer)
	}
	return answer[i+12:]
}

// A borrower's adb forward listens on the borrower, and its reverse
// reaches the borrower, through the device's node.
func TestForwardsListenWhereTheBorrowerIs(t *testing.T) {
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	srv := withADB(t, b, true, "S0", "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "a linked to b", func() bool { n := nodeOf(ctx, t, c, b.cfg.NodeID); return n != nil && n.GetTrustsUs() })

	lease, err := c.Borrow(ctx, &protocol.Borrow{Device: "b/S1"})
	if err != nil {
		t.Fatal(err)
	}
	addr := lease.GetAddr()

	// forward, asked as adb does: the transport first.
	got, _ := adbfake.Request(addr, "host:tport:serial:S1", "host:forward:tcp:0;tcp:7")
	port := adbPort(t, got)
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(conn, "hi")
	if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || line != "hi\n" {
		t.Fatalf("through the forward: %q, %v", line, err)
	}
	conn.Close()
	if got, _ := adbfake.Request(addr, "host:list-forward"); !strings.Contains(got, "S1 tcp:"+port+" tcp:7\n") {
		t.Fatalf("forwards listed: %q", got)
	}
	if slices.ContainsFunc(srv.Requests(), func(r string) bool { return strings.Contains(r, "forward:") }) {
		t.Fatal("the forward was made on the device's node")
	}
	if got, _ := adbfake.Request(addr, "host:killforward-all"); got != "OKAYOKAY" {
		t.Fatalf("killforward-all: %q", got)
	}
	if conn, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
		conn.Close()
		t.Fatal("the forward still listens")
	}

	// reverse: the device reaches a server of the borrower's.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			fmt.Fprint(conn, "pong:"+line)
			conn.Close()
		}
	}()
	here := ln.Addr().(*net.TCPAddr).Port
	got, _ = adbfake.Request(addr, "host:transport:S1", fmt.Sprintf("reverse:forward:tcp:9998;tcp:%d", here))
	if got != "OKAYOKAYOKAY00049998" {
		t.Fatalf("reverse: %q", got)
	}
	select {
	case line := <-srv.Reversed():
		if line != "pong:ping\n" {
			t.Fatalf("through the reverse: %q", line)
		}
	case <-ctx.Done():
		t.Fatal("the reverse reached nothing")
	}
}

// A lease follows its device: detached while it is not attached to its
// node, held again once it is.
func TestALeaseFollowsItsDevice(t *testing.T) {
	every := deviceWatchEvery
	deviceWatchEvery = 20 * time.Millisecond
	t.Cleanup(func() { deviceWatchEvery = every })
	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	srv := withADB(t, b, true, "S0", "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "a linked to b", func() bool { n := nodeOf(ctx, t, c, b.cfg.NodeID); return n != nil && n.GetTrustsUs() })
	if _, err := c.Borrow(ctx, &protocol.Borrow{Device: "S1"}); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		l, err := c.Leases(ctx, &protocol.ListLeases{})
		if err != nil || len(l.GetLeases()) != 1 {
			return ""
		}
		return l.GetLeases()[0].GetState()
	}
	srv.SetDevices("S0")
	waitFor(t, "the lease is not detached", func() bool { return state() == protocol.LeaseDetached })
	srv.SetDevices("S0", "S1")
	waitFor(t, "the lease is not held again", func() bool { return state() == protocol.LeaseHeld })
}

// A session holds the devices it started with until it ends; giving a
// device back by hand leaves it to the session.
func TestSessionsHoldTheirDevices(t *testing.T) {
	a := meshDaemon(t, "a")
	withADB(t, a, true, "S0", "S1")
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	env, err := a.deviceEnv(ctx, "s1", []string{"S1"})
	if err != nil || env["ANDROID_SERIAL"] != "S1" {
		t.Fatalf("env %v, %v", env, err)
	}
	if _, err := a.deviceEnv(ctx, "s2", []string{"S0", "S1"}); err == nil {
		t.Fatal("a session took two adb devices")
	}
	a.releaseSession("s2")
	if _, err := c.GiveBack(ctx, &protocol.GiveBack{Ref: "S1"}); err == nil {
		t.Fatal("gave back by hand what only a session holds")
	}
	if _, err := c.Borrow(ctx, &protocol.Borrow{Device: "S1"}); err != nil {
		t.Fatal(err)
	}
	back, err := c.GiveBack(ctx, &protocol.GiveBack{Ref: "S1"})
	if err != nil || back.GetState() != protocol.LeaseHeld || !slices.Equal(back.GetSessions(), []string{"s1"}) {
		t.Fatalf("given back as %v, %v", back, err)
	}
	a.releaseSession("s1")
	if l, _ := c.Leases(ctx, &protocol.ListLeases{}); len(l.GetLeases()) != 0 {
		t.Fatalf("still borrowed: %v", l)
	}
}

// A session started with a device has its environment, and gives the
// device back as it ends.
func TestASessionStartsWithItsDevice(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	cfg := &config.Config{NodeID: "node", Name: "here", Agents: map[string]agentspec.Agent{
		"test": {Command: []string{"/bin/sh"}},
	}, Devices: config.Devices{Share: []string{protocol.DeviceADB}}}
	d := New(cfg, nil)
	d.mcp = nil
	if err := d.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)
	withADB(t, d, true, "S1")

	s, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "test", Project: project, Devices: []string{"S1"},
		Args: []string{"-c", `echo "$ANDROID_SERIAL $ADB_SERVER_SOCKET" > lent; exec cat`}})
	if err != nil {
		t.Fatal(err)
	}
	leases := d.leases.list()
	if len(leases) != 1 || !slices.Equal(leases[0].GetSessions(), []string{s.ID()}) || leases[0].GetBorrowed() {
		t.Fatalf("leases %v", leases)
	}
	want := "S1 tcp:" + leases[0].GetAddr() + "\n"
	waitFor(t, "the session has no device in its environment", func() bool {
		got, _ := os.ReadFile(filepath.Join(project, "lent"))
		return string(got) == want
	})
	if err := d.closeSession(s.ID()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session's device was not given back", func() bool { return len(d.leases.list()) == 0 })

	if _, err := d.openSession(t.Context(), &protocol.OpenSession{Agent: "test", Project: project, Devices: []string{"S9"}}); err == nil {
		t.Fatal("a session started with a device not attached")
	}
}
