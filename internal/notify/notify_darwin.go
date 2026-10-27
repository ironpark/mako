package notify

/*
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdbool.h>
#include <stdlib.h>

bool makoNotifyBundled(void);
void makoNotifySetup(void);
void makoNotifyPost(const char *title, const char *body, const char *target);
*/
import "C"

import (
	"os/exec"
	"sync"
	"unsafe"
)

// bundled reports whether the app runs from its bundle: UserNotifications
// posts only for an app with a bundle identifier.
var bundled = sync.OnceValue(func() bool { return bool(C.makoNotifyBundled()) })

func setup() {
	if bundled() {
		C.makoNotifySetup()
	}
}

// post posts through UserNotifications, or without a bundle to post as,
// asks the system's scripting to, which cannot say what was clicked.
func post(title, body, target string) {
	if !bundled() {
		_ = exec.Command("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run", title, body).Run()
		return
	}
	ct, cb, cg := C.CString(title), C.CString(body), C.CString(target)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(cg))
	C.makoNotifyPost(ct, cb, cg)
}

//export makoNotifyClicked
func makoNotifyClicked(target *C.char) { click(C.GoString(target)) }
