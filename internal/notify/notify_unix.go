//go:build !darwin && !windows

package notify

import (
	"sync"

	"github.com/godbus/dbus/v5"
)

// The freedesktop notification service.
const (
	service = "org.freedesktop.Notifications"
	path    = dbus.ObjectPath("/org/freedesktop/Notifications")
)

// notifier is the session bus's notification service, and the
// notifications this app has up on it.
var notifier struct {
	sync.Mutex
	conn    *dbus.Conn
	targets map[uint32]string // a notification up to its target
	shown   map[string]uint32 // a target to its notification, which the next replaces
}

func setup() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return
	}
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(service)); err != nil {
		conn.Close()
		return
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	notifier.Lock()
	notifier.conn = conn
	notifier.targets = map[uint32]string{}
	notifier.shown = map[string]uint32{}
	notifier.Unlock()
	go listen(signals)
}

// listen follows the service's word on this app's notifications: which
// was clicked, and which went.
func listen(signals <-chan *dbus.Signal) {
	for sig := range signals {
		if len(sig.Body) < 2 {
			continue
		}
		id, _ := sig.Body[0].(uint32)
		notifier.Lock()
		target := notifier.targets[id]
		if sig.Name == service+".NotificationClosed" {
			delete(notifier.targets, id)
			if notifier.shown[target] == id {
				delete(notifier.shown, target)
			}
		}
		notifier.Unlock()
		if action, _ := sig.Body[1].(string); sig.Name == service+".ActionInvoked" && action == "default" {
			click(target)
		}
	}
}

// post posts on the session bus; without one there is no service to post
// to.
func post(title, body, target string) {
	notifier.Lock()
	conn, replaces := notifier.conn, notifier.shown[target]
	notifier.Unlock()
	if conn == nil {
		return
	}
	var id uint32
	err := conn.Object(service, path).Call(service+".Notify", 0,
		"mako", replaces, "", title, body, []string{"default", "Open"}, map[string]dbus.Variant{}, int32(-1)).Store(&id)
	if err != nil {
		return
	}
	notifier.Lock()
	notifier.targets[id] = target
	notifier.shown[target] = id
	notifier.Unlock()
}
