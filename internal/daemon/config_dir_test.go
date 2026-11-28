package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
)

// A daemon built without a configuration directory, as makod and the
// app's embedded daemon are, keeps the one Run resolves, so what it reads
// and writes there later finds it.
func TestRunKeepsTheConfigDirItResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", home)
	want, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.P2P.Disabled = true
	d := New(cfg, slog.New(slog.DiscardHandler))
	if d.dir != "" {
		t.Fatalf("New set dir %q", d.dir)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)
	go func() { runErr <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-runErr
	})
	select {
	case <-d.Ready():
	case err := <-runErr:
		t.Fatalf("Run exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not become ready")
	}
	if d.dir != want {
		t.Fatalf("dir = %q, want %q", d.dir, want)
	}
}
