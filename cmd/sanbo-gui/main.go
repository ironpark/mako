// Command sanbo-gui is the desktop front-end for the sanbo daemon mesh.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/gui"
)

func main() {
	daemonAddr := flag.String("daemon", "", "daemon address (default 127.0.0.1:4242)")
	renderDir := flag.String("render-dir", "", "render the window to PNGs in this directory, then exit (development)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sanbo-gui:", err)
		os.Exit(1)
	}
	if *renderDir != "" {
		if err := gui.Preview(cfg, *daemonAddr, *renderDir, 1280, 800); err != nil {
			fmt.Fprintln(os.Stderr, "sanbo-gui:", err)
			os.Exit(1)
		}
		return
	}
	if err := gui.Run(cfg, *daemonAddr); err != nil {
		fmt.Fprintln(os.Stderr, "sanbo-gui:", err)
		os.Exit(1)
	}
}
