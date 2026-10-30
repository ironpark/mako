// Command mako-gui is the desktop front-end for the mako daemon mesh.
package main

import (
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/debug"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/shellenv"
)

// memoryLimit is the soft limit of the Go runtime's memory: the heap, the
// stacks and the runtime's own, not the window's textures or a C library's.
const memoryLimit = 192 << 20

func main() {
	// The app's own daemon runs this program as its agents' hook.
	if daemon.HookMain() {
		return
	}
	// Opened from the Finder or a desktop's menu, the app and the daemon
	// in it take the login shell's PATH, to find the agents it runs.
	shellenv.Load()
	keepCrashes()
	// A window of busy terminals makes garbage fast, and the collector
	// lets the heap grow to twice what is live between collections: some
	// 200 MB for 95 live. A soft limit collects sooner as the heap nears
	// it, kept clear of the live heap (see limit.go), unless GOMEMLIMIT
	// asks for another.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
		go fitLimit()
	}
	go relieve()
	daemonAddr := flag.String("daemon", "", "the daemon's socket (default: the configuration directory's)")
	renderDir := flag.String("render-dir", "", "render the window to PNGs in this directory, then exit (development)")
	pprofAddr := flag.String("pprof", os.Getenv("MAKO_PPROF"), "serve Go's profiles on this address, as 127.0.0.1:6060 (development)")
	flag.Parse()
	if *pprofAddr != "" {
		go func() { fmt.Fprintln(os.Stderr, "mako-gui: pprof:", http.ListenAndServe(*pprofAddr, nil)) }()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mako-gui:", err)
		os.Exit(1)
	}
	if *renderDir != "" {
		if err := gui.Preview(cfg, *daemonAddr, *renderDir, 1280, 800); err != nil {
			fmt.Fprintln(os.Stderr, "mako-gui:", err)
			os.Exit(1)
		}
		return
	}
	if err := gui.Run(cfg, *daemonAddr); err != nil {
		fmt.Fprintln(os.Stderr, "mako-gui:", err)
		os.Exit(1)
	}
}
