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

import "unsafe"

// setBadge puts label on the app's Dock icon, "" for none.
func setBadge(label string) {
	cs := C.CString(label)
	defer C.free(unsafe.Pointer(cs))
	C.setDockBadge(cs)
}
