package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/ironpark/mako/internal/config"
)

// crashLogMax is how large the crash log grows before a run starts it
// afresh.
const crashLogMax = 4 << 20

// keepCrashes writes why the app died to crash.log in the configuration
// directory: opened from the Finder, its standard error goes nowhere, and
// a Go panic, or a fault in a system library called through purego, said
// why only there. Each run starts with a line naming it, so a trace reads
// against the run it ended. Unless GOTRACEBACK says otherwise, a crash
// then aborts, for macOS to write a report with every thread's native
// stack, which a fault inside Metal or AppKit needs.
func keepCrashes() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "crash.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > crashLogMax {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "=== mako-gui %s, pid %d, started %s\n", revision(), os.Getpid(), time.Now().Format(time.RFC3339))
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return
	}
	// SetCrashOutput keeps its own copy of the file.
	f.Close()
	if os.Getenv("GOTRACEBACK") == "" {
		debug.SetTraceback("crash")
	}
}

// exitWith reports err, why the app is stopping, on standard error and in
// crash.log, where keepCrashes writes a crash: opened from the Finder, the
// app's standard error goes nowhere, and an error the window's run ends
// with, effects that would not settle, say, was lost there. It then exits.
func exitWith(err error) {
	fmt.Fprintln(os.Stderr, "mako-gui:", err)
	if dir, derr := config.Dir(); derr == nil {
		if f, ferr := os.OpenFile(filepath.Join(dir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
			fmt.Fprintf(f, "exit, pid %d, at %s: %v\n", os.Getpid(), time.Now().Format(time.RFC3339), err)
			f.Close()
		}
	}
	os.Exit(1)
}

// revision is the commit the program was built from, as the build
// recorded it.
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "unknown", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(len(s.Value), 7)]
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
