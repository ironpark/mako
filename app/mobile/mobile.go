// Package mobile runs a mako node inside the Android app: the same daemon
// makod is, with its configuration in the app's own files, so the phone
// joins the mesh over libp2p as any machine does. The app then talks to it
// on its socket, in the protocol's frames, as the desktop app does to its
// own. gomobile binds it (`make android` in app/), which limits what it
// exports to strings, errors and the like.
package mobile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
)

var (
	mu      sync.Mutex
	stop    context.CancelFunc
	stopped chan struct{}
	socket  string
	name    string
	logFile *os.File
)

// Start runs the node, with its configuration in dir and its temporary
// files in cache, under name the first time it runs. It returns once the
// node listens on its socket. A node already running is left as it is.
func Start(dir, cache, nodeName string) error {
	mu.Lock()
	defer mu.Unlock()
	if stop != nil {
		return nil
	}
	// A phone has no home or temporary directory of its own: the app's
	// stand in, which only the app can reach.
	for k, v := range map[string]string{"MAKO_HOME": dir, "HOME": dir, "TMPDIR": cache} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	name = nodeName
	return start()
}

// start runs the node from its configuration as it is saved; mu is held.
func start() error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	_, err = os.Stat(path)
	first := errors.Is(err, os.ErrNotExist)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if first { // named after the phone, which Android calls localhost
		cfg.Name = name
		if err := config.Save(cfg); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := daemon.New(cfg, slog.New(slog.NewTextHandler(logOut(), nil)))
	done := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		failed <- d.Run(ctx)
	}()
	select {
	case <-d.Ready():
	case err := <-failed:
		cancel()
		if err == nil {
			err = errors.New("the node stopped as it started")
		}
		return err
	}
	stop, stopped, socket = cancel, done, d.Socket()
	return nil
}

// Stop stops the node, and waits for it to.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	halt()
}

// halt stops the node; mu is held.
func halt() {
	if stop == nil {
		return
	}
	stop()
	<-stopped
	stop, stopped, socket = nil, nil, ""
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

// Socket is the socket the node listens on for the app, "" while it is
// not running.
func Socket() string {
	mu.Lock()
	defer mu.Unlock()
	return socket
}

// Peers is the addresses the node always links to, a line each.
func Peers() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return strings.Join(cfg.Peers, "\n"), nil
}

// AddPeer has the node link to addr: a host, with its libp2p port or the
// default one, or a multiaddr ending in its /p2p/ ID. The node starts
// again to take it.
func AddPeer(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("no address")
	}
	return changePeers(func(peers []string) []string {
		if slices.Contains(peers, addr) {
			return peers
		}
		return append(peers, addr)
	})
}

// RemovePeer stops the node linking to addr, which AddPeer added.
func RemovePeer(addr string) error {
	return changePeers(func(peers []string) []string {
		return slices.DeleteFunc(peers, func(p string) bool { return p == addr })
	})
}

// Rename names the node, as the mesh shows it.
func Rename(nodeName string) error {
	return change(func(cfg *config.Config) { cfg.Name = strings.TrimSpace(nodeName) })
}

func changePeers(edit func([]string) []string) error {
	return change(func(cfg *config.Config) { cfg.Peers = edit(cfg.Peers) })
}

// change edits the saved configuration, and starts a running node again
// to take it.
func change(edit func(*config.Config)) error {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	edit(cfg)
	if err := config.Save(cfg); err != nil {
		return err
	}
	if stop == nil {
		return nil
	}
	halt()
	return start()
}

// logOut is where the node logs: node.log beside its configuration, begun
// again each time it starts, or nowhere; mu is held.
func logOut() io.Writer {
	dir, err := config.Dir()
	if err != nil {
		return io.Discard
	}
	f, err := os.OpenFile(filepath.Join(dir, "node.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return io.Discard
	}
	logFile = f
	return f
}
