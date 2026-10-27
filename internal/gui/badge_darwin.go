package gui

import (
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The Dock badge is set through the Objective-C runtime with purego, so
// the package builds without cgo. AppKit wants it set on the main thread,
// where libdispatch runs badgeWork; the label it sets is the last one
// asked for, which is the one that matters.

var (
	badgeLabel atomic.Pointer[string]
	badgeWork  = sync.OnceValue(func() uintptr {
		return purego.NewCallback(func(uintptr) uintptr {
			label := ""
			if p := badgeLabel.Load(); p != nil {
				label = *p
			}
			var s objc.ID
			if label != "" {
				s = objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), label)
			}
			app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
			app.Send(objc.RegisterName("dockTile")).Send(objc.RegisterName("setBadgeLabel:"), s)
			return 0
		})
	})
	// dispatchMain is dispatch_async_f and the main queue, or zero where
	// libdispatch cannot be found.
	dispatchMain = sync.OnceValues(func() (asyncF, mainQueue uintptr) {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return 0, 0
		}
		asyncF, _ = purego.Dlsym(lib, "dispatch_async_f")
		// dispatch_get_main_queue() is a macro for this symbol's address.
		mainQueue, _ = purego.Dlsym(lib, "_dispatch_main_q")
		return asyncF, mainQueue
	})
)

// setBadge puts label on the app's Dock icon, "" for none.
func setBadge(label string) {
	asyncF, mainQueue := dispatchMain()
	if asyncF == 0 || mainQueue == 0 {
		return
	}
	badgeLabel.Store(&label)
	purego.SyscallN(asyncF, mainQueue, 0, badgeWork())
}
