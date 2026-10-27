// Package notify posts desktop notifications the platform's own way:
// UserNotifications on macOS, toasts on Windows, and elsewhere the
// freedesktop notification service over D-Bus. It reports the one the user
// clicks.
package notify

import "sync/atomic"

var clicked atomic.Pointer[func(string)]

// Init readies the platform's notifications, asking the user's leave where
// the platform wants it. onClick runs, on a goroutine of the platform's,
// with the target of a notification the user clicks.
func Init(onClick func(target string)) {
	clicked.Store(&onClick)
	setup()
}

// Post shows a notification about target, which a later one about the same
// target replaces where the platform can, and which a click hands to Init's
// onClick. It may block a moment; call it off the UI's goroutine.
func Post(title, body, target string) { post(title, body, target) }

func click(target string) {
	if f := clicked.Load(); f != nil && target != "" {
		(*f)(target)
	}
}
