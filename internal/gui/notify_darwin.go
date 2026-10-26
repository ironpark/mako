package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>

static void setDockBadge(const char *label) {
	@autoreleasepool {
		NSString *s = label[0] ? [NSString stringWithUTF8String:label] : nil;
		[s retain];
		dispatch_async(dispatch_get_main_queue(), ^{
			[[NSApp dockTile] setBadgeLabel:s];
			[s release];
		});
	}
}
*/
import "C"

import (
	"os/exec"
	"unsafe"
)

// setBadge puts label on the app's Dock icon, "" for none.
func setBadge(label string) {
	cs := C.CString(label)
	defer C.free(unsafe.Pointer(cs))
	C.setDockBadge(cs)
}

// notify posts a desktop notification. Without an app bundle of its own
// to post as, the app asks the system's scripting to post it.
func notify(title, body string) {
	_ = exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run", title, body).Run()
}
