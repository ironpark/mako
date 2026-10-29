package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

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
