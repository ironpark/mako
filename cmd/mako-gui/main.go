// Command mako-gui is the desktop front-end for the mako daemon mesh.
package main

import (
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/gui"
)

func main() {
	// The app's own daemon runs this program as its agents' hook.
	if daemon.HookMain() {
		return
	}
	daemonAddr := flag.String("daemon", "", "daemon address (default 127.0.0.1:4242)")
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
