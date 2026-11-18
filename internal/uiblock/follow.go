package uiblock

import (
	"context"
	"errors"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// ErrGone ends a stream whose block went.
var ErrGone = errors.New("the block went")

// View is a block as a stream sends it, at a moment: its tree, who it
// comes from and how that stands, and a channel closed when any of it
// next changes.
type View struct {
	Tree *protocol.UiNode
	// Plugin names a plugin's block's plugin; Session and Agent an
	// agent's block's session and its agent's name.
	Plugin, Session, Agent string
	Title, State, Err      string
	Stale                  bool
	Changed                <-chan struct{}
	Gone                   bool
}

func (v View) sameAs(o View) bool {
	return v.Plugin == o.Plugin && v.Session == o.Session && v.Agent == o.Agent && v.Title == o.Title &&
		v.State == o.State && v.Err == o.Err && v.Stale == o.Stale
}

// Follow sends a block of the name to send, as view tells it: its tree
// whole, then the ops from each tree to the next, no more often than
// every, so that a burst of changes goes as one. It ends when ctx does,
// send fails, or the block goes, after it says so.
func Follow(ctx context.Context, name string, every time.Duration, view func() View, send func(*protocol.BlockUpdate) error) error {
	var last View
	var seq uint64
	var sent time.Time
	for {
		v := view()
		upd := &protocol.BlockUpdate{Block: name, Plugin: v.Plugin, SessionId: v.Session, Agent: v.Agent,
			Title: v.Title, State: v.State, Error: v.Err, Stale: v.Stale, Gone: v.Gone}
		switch {
		case seq == 0 || last.Tree == nil && v.Tree != nil:
			upd.Tree = v.Tree
		case v.Tree != nil && v.Tree != last.Tree:
			upd.Ops = protocol.DiffUi(last.Tree, v.Tree)
		}
		if seq == 0 || upd.Tree != nil || len(upd.Ops) > 0 || !v.sameAs(last) || v.Gone {
			seq++
			upd.Seq = seq
			if err := send(upd); err != nil {
				return err
			}
			last, sent = v, time.Now()
		}
		if v.Gone {
			return ErrGone
		}
		select {
		case <-v.Changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		if wait := every - time.Since(sent); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
