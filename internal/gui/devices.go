package gui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// Devices of other nodes, borrowed for this one (docs/devices.md): the
// sidebar's menu lists those the mesh lends, borrows one, copies the
// environment its tools find it by, and gives it back.

// deviceList is what the window shows of devices: the mesh's, and those
// its daemon borrows.
type deviceList struct {
	Devices []*protocol.Device
	Leases  []*protocol.Lease
}

// devicesEvery is how often the window asks the mesh for its devices, each
// node asking its adb server, though nothing said they changed.
const devicesEvery = 15 * time.Second

// fetchDevices asks the daemon for the mesh's devices and its leases, when
// they changed or devicesEvery passed; nil when it did not ask, or the
// daemon is too old to say.
func (m *model) fetchDevices(ctx context.Context, c *client.Client, self *protocol.Node) *deviceList {
	if self.GetRevision() < protocol.RevisionDevices {
		return nil
	}
	if !m.devicesStale.Swap(false) && time.Since(m.devicesAt) < devicesEvery {
		return nil
	}
	m.devicesAt = time.Now()
	list, err := c.Devices(ctx, &protocol.ListDevices{Mesh: true})
	if err != nil {
		return nil
	}
	leases, err := c.Leases(ctx, &protocol.ListLeases{})
	if err != nil {
		return nil
	}
	devices := list.GetDevices()
	slices.SortFunc(devices, func(a, b *protocol.Device) int { return strings.Compare(a.Ref(), b.Ref()) })
	return &deviceList{Devices: devices, Leases: leases.GetLeases()}
}

// borrowDevice borrows dev for this node, and says where its tools reach
// it.
func (m *model) borrowDevice(dev *protocol.Device) {
	name := deviceName(dev)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var lease *protocol.Lease
		c, err := m.client(ctx)
		if err == nil {
			lease, err = c.Borrow(ctx, &protocol.Borrow{Device: dev.Ref()})
		}
		m.post(func() {
			if err != nil {
				m.toaster.Push(ui.Toast(Tf("Could not borrow %s", name), nodeErr(err)).Destructive())
				return
			}
			body := Tf("adb here reaches it at %s. Copy its environment from the devices menu.", lease.GetAddr())
			if w := lease.GetWarnings(); len(w) > 0 {
				body += "\n" + strings.Join(w, "\n")
			}
			m.toaster.Push(ui.Toast(Tf("%s is lent here", name), body))
		})
		m.devicesStale.Store(true)
		m.pollNow()
	}()
}

// giveBackDevice gives back a device this node borrowed by hand.
func (m *model) giveBackDevice(l *protocol.Lease) {
	name := deviceName(l.GetDevice())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err == nil {
			_, err = c.GiveBack(ctx, &protocol.GiveBack{LeaseId: l.GetId()})
		}
		if err != nil {
			m.post(func() {
				m.toaster.Push(ui.Toast(Tf("Could not give %s back", name), nodeErr(err)).Destructive())
			})
		}
		m.devicesStale.Store(true)
		m.pollNow()
	}()
}

// deviceName names a device for people: its name, and its serial when
// that says more.
func deviceName(d *protocol.Device) string {
	if d.GetName() == "" || d.GetName() == d.GetSerial() {
		return d.GetSerial()
	}
	return fmt.Sprintf("%s (%s)", d.GetName(), d.GetSerial())
}

// shellEnv is env as a POSIX shell sets it, a line a variable.
func shellEnv(env map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return b.String()
}

// leaseState says how a lease stands, when it is not simply held.
func leaseState(l *protocol.Lease) string {
	switch l.GetState() {
	case protocol.LeaseDetached:
		return T("not attached")
	case protocol.LeaseReconnecting:
		return T("reconnecting")
	}
	return ""
}

// devicesMenu is the sidebar's menu of the devices of other nodes, there
// while any node lends one, or this one borrows one.
func devicesMenu(m *model) ggui.Widget {
	return ggui.View(m.Devices, func(list deviceList) ggui.Widget {
		self := ggui.Peek(m.Conn).Self.GetId()
		nodes := ggui.Peek(m.Nodes)
		borrowed := map[string]bool{}
		var items []ggui.Widget
		for _, l := range list.Leases {
			dev := l.GetDevice()
			borrowed[dev.Ref()] = true
			if len(items) > 0 {
				items = append(items, ui.MenuDivider())
			}
			label := Tf("%s of %s", deviceName(dev), nodeName(nodes, dev.GetNodeId()))
			if s := leaseState(l); s != "" {
				label += " · " + s
			}
			items = append(items, ui.MenuItem(label, nil).Disabled(true))
			env := shellEnv(l.GetEnv())
			items = append(items, ui.MenuItem(T("Copy its adb environment"), func() { ggui.CurrentClipboard().Write(env) }))
			if l.GetBorrowed() {
				items = append(items, ui.MenuItem(T("Give it back"), func() { m.giveBackDevice(l) }))
			}
			if n := len(l.GetSessions()); n > 0 {
				items = append(items, ui.MenuItem(Tf("Held by %d sessions", n), nil).Disabled(true))
			}
		}
		lendable := slices.DeleteFunc(slices.Clone(list.Devices), func(d *protocol.Device) bool {
			return d.GetNodeId() == self || !d.GetShared() || borrowed[d.Ref()] || d.GetState() == protocol.DeviceDetached
		})
		slices.SortStableFunc(lendable, func(a, b *protocol.Device) int {
			return cmp.Compare(nodeName(nodes, a.GetNodeId()), nodeName(nodes, b.GetNodeId()))
		})
		if len(lendable) > 0 && len(items) > 0 {
			items = append(items, ui.MenuDivider())
		}
		for _, d := range lendable {
			items = append(items, ui.MenuItem(Tf("Borrow %s of %s", deviceName(d), nodeName(nodes, d.GetNodeId())), func() { m.borrowDevice(d) }))
		}
		if len(items) == 0 {
			return ggui.Box()
		}
		name := Tf("%d devices borrowed", len(list.Leases))
		if len(list.Leases) == 0 {
			name = Tf("%d devices to borrow", len(lendable))
		}
		return iconMenu(ui.MenuOf(icon("smartphone"), items...).Name(name), 6)
	})
}
