package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// A plugin with layout:write changes the window's layout through the
// flow an agent's patch_layout takes, which the daemon carries out
// (LayoutHost): the change staged on this node and shown, the user asked
// unless they allowed this plugin such changes, the plugin named as its
// origin; a document's root alone, never its variants. The user may take
// minutes to answer, longer than a call may run, so the call checks the
// patch, hands it on and returns, and the outcome comes to the plugin as
// an event of its own, as a start of an agent's does.

const (
	layoutPatchesPerMin = 10
	// layoutChangeFor bounds a change, the user's answer included.
	layoutChangeFor = 15 * time.Minute
)

// The kinds of event that tell a plugin how its change of the layout went.
const (
	EventLayoutChanged = "layoutChanged"
	EventLayoutRefused = "layoutRefused"
)

// LayoutChange is a patch of a window layout document, checked against
// the document as it stood: the document after, the revision it was made
// of, and what it changes, in a line.
type LayoutChange struct {
	Document, Base, Summary string
	Doc                     []byte
}

// LayoutHost is the daemon's side of layout:write.
type LayoutHost interface {
	// Prepare checks a patch, the JSON of patch_layout's ops, against the
	// document as it stands, ifRev its revision ("" for any).
	Prepare(document, ops, ifRev string) (LayoutChange, error)
	// Allowed says the user lets the plugin change the document without
	// asking.
	Allowed(plugin, document string) bool
	// Change makes the change as the user has it, waiting for them as need
	// be, and returns the document's revision after.
	Change(ctx context.Context, plugin string, c LayoutChange) (string, error)
	// Forget drops what the user let the plugin change without asking, as
	// it is removed.
	Forget(plugin string)
}

func (h *host) LayoutPatchLayout(_ context.Context, req *hostapi.PatchLayoutRequest) (*hostapi.PatchLayoutResponse, error) {
	p := h.p
	lh := p.m.cfg.Layout
	if lh == nil {
		return nil, errors.New("this node's layout is not a plugin's to change")
	}
	if !p.layoutRate.take() {
		return nil, fmt.Errorf("the plugin changed the layout more than %d times a minute", layoutPatchesPerMin)
	}
	if len(req.GetOps()) > 64<<10 {
		return nil, errors.New("a patch is at most 64 KiB of JSON")
	}
	c, err := lh.Prepare(req.GetDocument(), req.GetOps(), req.GetIfRev())
	if err != nil {
		return nil, err
	}
	if !p.layoutBusy.CompareAndSwap(false, true) {
		return nil, errors.New("another change of the plugin's waits for the user")
	}
	id := "l" + nonce()
	asked := !lh.Allowed(p.id, c.Document)
	go func() {
		defer p.layoutBusy.Store(false)
		ctx, cancel := context.WithTimeout(p.m.ctx, layoutChangeFor)
		defer cancel()
		ev := &hostapi.SessionEvent{Kind: EventLayoutChanged, RequestId: id, Document: c.Document}
		rev, err := lh.Change(ctx, p.id, c)
		if err != nil {
			ev.Kind, ev.Error = EventLayoutRefused, err.Error()
		} else {
			ev.Revision = rev
		}
		p.post(func() { p.deliver(ev, hostapi.PermLayoutWrite) })
	}()
	return &hostapi.PatchLayoutResponse{RequestId: id, Asked: asked}, nil
}
