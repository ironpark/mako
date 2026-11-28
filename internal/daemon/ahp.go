package daemon

import (
	"context"
	"path/filepath"
	"strconv"

	"github.com/ironpark/mako/internal/ahp"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// serveAHP serves the sessions this daemon reaches to Agent Host Protocol
// clients (docs/ahp.md) until ctx ends: on cfg.Listen, a loopback
// address, with the token in ahp-token in dir, and on ahp.sock in run.
// The bridge follows the sessions as a front-end of this daemon's own
// socket does.
func (d *Daemon) serveAHP(ctx context.Context, dir, run string, cfg config.AHP) {
	token, err := ahp.Token(filepath.Join(dir, "ahp-token"))
	if err != nil {
		d.log.Warn("ahp: no token", "err", err)
		return
	}
	b, err := ahp.New(ahp.Options{
		Dial:      func(ctx context.Context) (*client.Client, error) { return client.Dial(ctx, d.Socket()) },
		EpochFile: filepath.Join(dir, "ahp-epoch"),
		Version:   "mako revision " + strconv.Itoa(protocol.Revision),
		Log:       d.log,
	})
	if err != nil {
		d.log.Warn("ahp: not started", "err", err)
		return
	}
	defer b.Close()
	go func() {
		if err := b.Run(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("ahp: bridge stopped", "err", err)
		}
	}()
	d.log.Info("ahp: serving", "listen", cfg.Listen, "socket", filepath.Join(run, "ahp.sock"))
	if err := ahp.Serve(ctx, b, ahp.Endpoint{Listen: cfg.Listen, Socket: filepath.Join(run, "ahp.sock"), Token: token, Origins: cfg.Origins}); err != nil {
		d.log.Warn("ahp: not served", "listen", cfg.Listen, "err", err)
	}
}
