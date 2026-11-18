package daemon

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/workspace"
)

// layoutChange is a change of a window layout document an agent asks for.
type layoutChange struct {
	tool    string // set_layout, patch_layout or undo_layout
	name    string // the document
	doc     []byte // the document after, nil for none (an undo to none)
	base    string // the revision it was made of
	summary string // what it changes, in a line
	undo    bool   // goes back to the version the history keeps last
}

// layoutRev is a document's revision: the stamp of its entry, "" for no
// document.
func layoutRev(l *protocol.LayoutEntry) string {
	if l == nil {
		return ""
	}
	c := l.GetClock()
	return fmt.Sprintf("%d.%d.%s", c.GetWallTime(), c.GetLogical(), c.GetNodeId())
}

// windowLayout is the document of the name the workspace holds, and its
// revision: nil and "" for none.
func (d *Daemon) windowLayout(name string) ([]byte, string) {
	l := workspace.WindowLayout(d.workspace.get(false).GetState(), name)
	return l.GetDoc(), layoutRev(l)
}

// layoutOrigin is who asks for a change: the agent of a session, or a
// plugin, which no session holds and which the user allows apart.
type layoutOrigin struct {
	session *session.Session
	plugin  string
}

// allowed says the user lets the origin change the document without
// asking.
func (d *Daemon) layoutAllowed(o layoutOrigin, name string) bool {
	if o.plugin != "" {
		return d.layouts.allowsPlugin(o.plugin, name)
	}
	return d.layouts.allows(name)
}

// changeLayout makes the change the agent of session s asks for, as the
// user has it; see changeLayoutFor.
func (d *Daemon) changeLayout(ctx context.Context, s *session.Session, c layoutChange) (string, error) {
	return d.changeLayoutFor(ctx, layoutOrigin{session: s}, c)
}

// changeLayoutFor makes the change the origin asks for, as the user has
// it: at once, when they allowed it such changes; else once they allow
// it, the window showing it meanwhile. A whole document set waits
// besides, shown, for the user to keep it, and is reverted after
// layoutKeepFor without an answer. With no window of this node's open to
// show it, a change the user is to see is refused. It returns the
// document's revision after.
func (d *Daemon) changeLayoutFor(ctx context.Context, o layoutOrigin, c layoutChange) (string, error) {
	allowed := d.layoutAllowed(o, c.name)
	keep := c.tool == toolSetLayout
	if (!allowed || keep) && !d.layouts.watched() {
		return "", errLayoutNoWindow
	}
	pb := &protocol.LayoutStage{Id: approvalID(), Document: c.name, Doc: c.doc, Tool: c.tool, Summary: c.summary}
	if o.plugin != "" {
		// An older window names the plugin as it would an agent.
		pb.PluginId, pb.Agent = o.plugin, "plugin "+o.plugin
	} else {
		info := o.session.Info()
		pb.SessionId, pb.Agent = o.session.ID(), cmpOr(info.GetName(), info.GetAgent())
	}
	staged, err := d.layouts.begin(pb)
	if err != nil {
		return "", err
	}
	defer func() {
		d.layouts.end(staged)
		d.changes.fireWorkspace()
	}()
	now, after := d.layouts.clock()
	if !allowed {
		d.layouts.setPhase(staged, protocol.LayoutApprove, time.Time{})
		d.changes.fireWorkspace()
		option, err := d.askLayout(ctx, o, staged, c, after)
		if err != nil {
			return "", err
		}
		if o.plugin != "" {
			d.layouts.allowPlugin(o.plugin, option, c.name)
		} else {
			d.layouts.allow(option, c.name)
		}
	}
	if keep {
		d.layouts.setPhase(staged, protocol.LayoutKeep, now().Add(layoutKeepFor))
		d.changes.fireWorkspace()
		// The user decides, whether the agent waits on or not: the
		// deadline bounds it.
		select {
		case a := <-staged.answers:
			if a.decision != protocol.LayoutKeep {
				return "", errLayoutReverted
			}
		case <-after(layoutKeepFor):
			return "", errLayoutReverted
		}
	}
	return d.commitLayout(c)
}

// askLayout asks the user to allow the change, in the windows' bar, and
// for an agent's as an approval on its session too, and returns the
// option it was allowed with: "" for once.
func (d *Daemon) askLayout(ctx context.Context, o layoutOrigin, staged *stagedLayout, c layoutChange, after func(time.Duration) <-chan time.Time) (string, error) {
	id := staged.pb.GetId()
	var sessionDone <-chan struct{} // a plugin's change waits on no session
	if s := o.session; s != nil {
		input := ""
		if c.doc != nil {
			input = indentInput(c.doc)
		}
		seq, _, ok := s.HoldApproval(&protocol.Approval{
			Id: id, Tool: c.tool, Detail: c.summary, Input: input, Since: timestamppb.Now(), Kind: protocol.ToolLayout,
			Options: []*protocol.ApprovalOption{{Id: protocol.LayoutAllowSpace}, {Id: protocol.LayoutAllowAlways}},
		})
		if !ok {
			return "", fmt.Errorf("the session cannot ask the user now")
		}
		defer s.FinishApproval(seq, id, protocol.AgentWorking)
		sessionDone = s.Done()
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	give := after(layoutAskFor)
	for {
		select {
		case a := <-staged.answers:
			if a.decision != protocol.DecisionAllow {
				if a.message != "" {
					return "", fmt.Errorf("%w: %s", errLayoutDenied, a.message)
				}
				return "", errLayoutDenied
			}
			return a.option, nil
		case <-tick.C:
			if !d.layouts.watched() {
				return "", errLayoutNoWindow
			}
		case <-give:
			return "", fmt.Errorf("the user did not answer within %v; nothing was changed", layoutAskFor)
		case <-sessionDone:
			return "", fmt.Errorf("the session ended")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// commitLayout writes the change into the workspace, unless the document
// changed since the change was made of it, and returns its revision
// after. An undo's version is taken off the history; any other's edit
// leaves the version before on it.
func (d *Daemon) commitLayout(c layoutChange) (string, error) {
	entry := &protocol.LayoutEntry{Doc: c.doc}
	if c.doc == nil {
		entry = &protocol.LayoutEntry{Deleted: true}
	}
	delta := &protocol.WorkspaceState{WindowLayouts: map[string]*protocol.LayoutEntry{c.name: entry}}
	changed, err := d.editWorkspace(delta, func(st *protocol.WorkspaceState) error {
		if layoutRev(workspace.WindowLayout(st, c.name)) != c.base {
			return errLayoutChanged
		}
		return nil
	}, !c.undo)
	if err != nil {
		return "", err
	}
	if c.undo {
		d.layouts.drop(c.name)
	}
	if changed {
		d.workspaceChanged()
	}
	_, rev := d.windowLayout(c.name)
	return rev, nil
}

// editWorkspace stamps and merges a delta of this node's, if check passes
// against the state it is merged into, and reports whether the state
// changed. With remember, the version a window layout had before the
// delta changed it is kept for undo.
func (d *Daemon) editWorkspace(delta *protocol.WorkspaceState, check func(*protocol.WorkspaceState) error, remember bool) (bool, error) {
	names := make([]string, 0, len(delta.GetWindowLayouts()))
	for name := range delta.GetWindowLayouts() {
		names = append(names, name)
	}
	slices.Sort(names)
	before, changed, err := d.workspace.editChecked(delta, check)
	if err != nil || !changed || !remember {
		return changed, err
	}
	after := d.workspace.get(false).GetState()
	for _, name := range names {
		prev, next := workspace.WindowLayout(before, name), workspace.WindowLayout(after, name)
		if (prev == nil) != (next == nil) || !bytes.Equal(prev.GetDoc(), next.GetDoc()) {
			d.layouts.remember(name, prev.GetDoc())
		}
	}
	return changed, nil
}

// frontWorkspace is the workspace as the node's own front-ends have it:
// the state, its digest, and the change staged here.
func (d *Daemon) frontWorkspace() *protocol.Workspace {
	ws := d.workspace.get(true)
	ws.Stage = d.layouts.current()
	return ws
}

// handleLayoutDecision answers a staged change from a front-end of this
// node's: keep or revert it, or forget what the user allowed.
func (d *Daemon) handleLayoutDecision(ctx context.Context, env *protocol.Envelope, fromPeer bool) (*protocol.Workspace, error) {
	if fromPeer {
		return nil, fmt.Errorf("a layout change is answered on its own node")
	}
	r, err := protocol.Body[*protocol.LayoutDecision](env)
	if err != nil {
		return nil, err
	}
	switch r.GetDecision() {
	case protocol.LayoutForget:
		d.layouts.forget()
	case protocol.LayoutKeep, protocol.LayoutRevert, protocol.DecisionAllow, protocol.DecisionDeny:
		if err := d.layouts.answer(r.GetStageId(), layoutAnswer{decision: r.GetDecision(), option: r.GetOption()}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown decision %q", r.GetDecision())
	}
	return d.frontWorkspace(), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
