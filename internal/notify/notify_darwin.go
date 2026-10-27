package notify

import (
	"os/exec"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// UserNotifications is reached through the Objective-C runtime with purego,
// so the package builds without cgo.

const (
	optionBanner = 1 << 4 // UNNotificationPresentationOptionBanner
	optionList   = 1 << 3 // UNNotificationPresentationOptionList
	optionSound  = 1 << 1 // UNNotificationPresentationOptionSound
	authAlert    = 1 << 2 // UNAuthorizationOptionAlert
	authSound    = 1 << 1 // UNAuthorizationOptionSound
)

var (
	selAlloc        = objc.RegisterName("alloc")
	selInit         = objc.RegisterName("init")
	selNew          = objc.RegisterName("new")
	selMainBundle   = objc.RegisterName("mainBundle")
	selBundleID     = objc.RegisterName("bundleIdentifier")
	selCurrent      = objc.RegisterName("currentNotificationCenter")
	selSetDelegate  = objc.RegisterName("setDelegate:")
	selRequestAuth  = objc.RegisterName("requestAuthorizationWithOptions:completionHandler:")
	selString       = objc.RegisterName("stringWithUTF8String:")
	selUTF8         = objc.RegisterName("UTF8String")
	selSetTitle     = objc.RegisterName("setTitle:")
	selSetBody      = objc.RegisterName("setBody:")
	selSetSound     = objc.RegisterName("setSound:")
	selDefaultSound = objc.RegisterName("defaultSound")
	selSetUserInfo  = objc.RegisterName("setUserInfo:")
	selSetThread    = objc.RegisterName("setThreadIdentifier:")
	selDictionary   = objc.RegisterName("dictionaryWithObject:forKey:")
	selRequestWith  = objc.RegisterName("requestWithIdentifier:content:trigger:")
	selAddRequest   = objc.RegisterName("addNotificationRequest:withCompletionHandler:")
	selRelease      = objc.RegisterName("release")
	selNotification = objc.RegisterName("notification")
	selRequest      = objc.RegisterName("request")
	selContent      = objc.RegisterName("content")
	selUserInfo     = objc.RegisterName("userInfo")
	selObjectForKey = objc.RegisterName("objectForKey:")
	selActionID     = objc.RegisterName("actionIdentifier")
	selIsEqual      = objc.RegisterName("isEqualToString:")
	selIsKindOf     = objc.RegisterName("isKindOfClass:")
	selPool         = objc.RegisterName("drain")
)

// loaded opens the framework, whose classes the runtime then knows.
var loaded = sync.OnceValue(func() bool {
	_, err := purego.Dlopen("/System/Library/Frameworks/UserNotifications.framework/UserNotifications", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	return err == nil
})

// bundled reports whether the app runs from its bundle: UserNotifications
// posts only for an app with a bundle identifier.
var bundled = sync.OnceValue(func() bool {
	bundle := objc.ID(objc.GetClass("NSBundle")).Send(selMainBundle)
	return loaded() && bundle.Send(selBundleID) != 0
})

func nsString(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(selString, s)
}

func goString(s objc.ID) string {
	p := objc.Send[*byte](s, selUTF8)
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// callBlock calls a block Objective-C handed over, through its invoke
// function, the third word of its layout.
func callBlock(block objc.Block, args ...uintptr) {
	if block == 0 {
		return
	}
	// Read through the variable's address: the block is a pointer to C
	// memory held as an integer.
	layout := *(**[3]uintptr)(unsafe.Pointer(&block))
	invoke := layout[2]
	purego.SyscallN(invoke, append([]uintptr{uintptr(block)}, args...)...)
}

// delegate shows notifications while the app is in front too, and hands
// click the target of the one the user clicks.
var delegate objc.ID

func setup() {
	if !bundled() {
		return
	}
	class, err := objc.RegisterClass("MakoNotifyDelegate", objc.GetClass("NSObject"),
		[]*objc.Protocol{objc.GetProtocol("UNUserNotificationCenterDelegate")}, nil,
		[]objc.MethodDef{
			{
				Cmd: objc.RegisterName("userNotificationCenter:willPresentNotification:withCompletionHandler:"),
				Fn: func(self objc.ID, cmd objc.SEL, center, notification objc.ID, done objc.Block) {
					callBlock(done, optionBanner|optionList|optionSound)
				},
			},
			{
				Cmd: objc.RegisterName("userNotificationCenter:didReceiveNotificationResponse:withCompletionHandler:"),
				Fn: func(self objc.ID, cmd objc.SEL, center, response objc.ID, done objc.Block) {
					defer callBlock(done)
					info := response.Send(selNotification).Send(selRequest).Send(selContent).Send(selUserInfo)
					target := info.Send(selObjectForKey, nsString("target"))
					isDefault := objc.Send[bool](response.Send(selActionID), selIsEqual, nsString("com.apple.UNNotificationDefaultActionIdentifier"))
					if isDefault && target != 0 && objc.Send[bool](target, selIsKindOf, objc.GetClass("NSString")) {
						click(goString(target))
					}
				},
			},
		})
	if err != nil {
		return
	}
	center := objc.ID(objc.GetClass("UNUserNotificationCenter")).Send(selCurrent)
	delegate = objc.ID(class).Send(selNew)
	center.Send(selSetDelegate, delegate)
	granted := objc.NewBlock(func(objc.Block, bool, objc.ID) {})
	center.Send(selRequestAuth, uintptr(authAlert|authSound), granted)
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
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selNew)
	defer pool.Send(selPool)
	t := nsString(target)
	content := objc.ID(objc.GetClass("UNMutableNotificationContent")).Send(selAlloc).Send(selInit)
	defer content.Send(selRelease)
	content.Send(selSetTitle, nsString(title))
	content.Send(selSetBody, nsString(body))
	content.Send(selSetSound, objc.ID(objc.GetClass("UNNotificationSound")).Send(selDefaultSound))
	content.Send(selSetUserInfo, objc.ID(objc.GetClass("NSDictionary")).Send(selDictionary, t, nsString("target")))
	content.Send(selSetThread, t)
	// One identifier for one target: the next replaces the last.
	request := objc.ID(objc.GetClass("UNNotificationRequest")).Send(selRequestWith, t, content, objc.ID(0))
	objc.ID(objc.GetClass("UNUserNotificationCenter")).Send(selCurrent).Send(selAddRequest, request, objc.ID(0))
}
