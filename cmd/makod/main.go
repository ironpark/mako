// Command makod is the mako daemon: it runs terminal agents on this machine
// and joins the mesh of daemons on the user's other computers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/shellenv"
)

func main() {
	if daemon.HookMain() {
		return
	}
	// Started by launchd or systemd rather than a terminal, it takes the
	// login shell's PATH, to find the agents it runs.
	shellenv.Load()
	var (
		listen  = flag.String("listen", "", "libp2p listen address, host:port (default from config)")
		name    = flag.String("name", "", "node name (default from config)")
		verbose = flag.Bool("v", false, "verbose logging")
		version = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("makod", config.Version)
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.P2P.Listen = *listen
	}
	if *name != "" {
		cfg.Name = *name
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := daemon.New(cfg, log)
	if path, err := config.Path(); err == nil {
		d.FollowAgents(path)
	}
	if err := d.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("daemon stopped", "err", err)
		os.Exit(1)
	}
}
