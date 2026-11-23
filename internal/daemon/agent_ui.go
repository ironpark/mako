package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agentui"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// The tools an agent shows UI in the window with (docs/agent-mcp.md), the
// plan's ui.show, ui.update and ui.close, named as the server's others
// are. A block's tree is a plugin's render result, read the same way; it
// is the session's own, agent:<session>/<id>, and closes with it. The
// user's actions on it come back to the agent as messages of the window's
// (agentUIEvent). Where a block goes is the daemon's alone, in memory
// (internal/agentui): by default the right panel's last tab; elsewhere
// only as the user allows, as for a layout change, and never written into
// the workspace.

const (
	toolShowUI   = "show_ui"
	toolUpdateUI = "update_ui"
	toolCloseUI  = "close_ui"
)

type showUIArgs struct {
	ID    string         `json:"id,omitempty" jsonschema:"The block's ID among yours: letters, digits, _ and -, at most 32. Left out, the next free ui-N."`
	Title string         `json:"title,omitempty" jsonschema:"The block's title, over it in its frame."`
	Tree  map[string]any `json:"tree" jsonschema:"The block's tree, as a plugin's render returns one (docs/plugins.md): {kind, key, text, tone, ..., onTap: [name, ...args], children: [...]}."`
	Place *placeArg      `json:"place,omitempty" jsonschema:"Where to put it other than the right panel, as the user allows: a row, column or stack of the layout document (get_layout)."`
}

// placeArg is where show_ui is asked to put a block.
type placeArg struct {
	Document string  `json:"document,omitempty" jsonschema:"The layout document's name; default when left out."`
	Parent   string  `json:"parent" jsonschema:"The id of the row, column or stack the block goes in."`
	Index    *int    `json:"index,omitempty" jsonschema:"Where among its children; the end when left out."`
	Size     float64 `json:"size,omitempty" jsonschema:"The block's size in px along its parent; none in a stack."`
}

// place is p as the store keeps it: index -1 for the end.
func (p placeArg) place() agentui.Place {
	out := agentui.Place{Document: p.Document, Parent: p.Parent, Index: -1, Size: p.Size}
	if p.Index != nil && *p.Index >= 0 {
		out.Index = *p.Index
	}
	return out
}

type showUIResult struct {
	Block  string `json:"block"`
	ID     string `json:"id"`
	Placed string `json:"placed"`
	Note   string `json:"note,omitempty"`
}

type updateUIArgs struct {
	ID    string         `json:"id" jsonschema:"The block's ID, as show_ui returned it."`
	Title *string        `json:"title,omitempty" jsonschema:"A new title; kept when left out."`
	Tree  map[string]any `json:"tree" jsonschema:"The block's whole new tree; the window gets the changes."`
}

type closeUIArgs struct {
	ID string `json:"id" jsonschema:"The block's ID, as show_ui returned it."`
}

type uiResult struct {
	Block string `json:"block"`
}

// placedRight is where a block goes by default.
const placedRight = "right panel"

// addUITools gives the agent's server the UI tools.
func (d *Daemon) addUITools(server *sdk.Server, peer *agentPeer) {
	sdk.AddTool(server, &sdk.Tool{Name: toolShowUI, Description: "Show a block of UI in the mako window, framed with your session's name: a tree of the vocabulary a plugin's render uses (docs/plugins.md: column, row, list, text, pill, button, input, toggle, select, markdown, ...; tones, never colours). It goes in the window's right panel, shown; place puts it elsewhere once the user allows. When the user taps a node with onTap, or changes one with onChange or onSubmit, you get a message from the window, one line of JSON: the block, the node, the message you attached to it, the value, and the values of the block's inputs, toggles and selects, so a form's submit button carries every field. The block closes when your session ends."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in showUIArgs) (*sdk.CallToolResult, showUIResult, error) {
			s, err := d.projectCaller(peer)
			if err != nil {
				return nil, showUIResult{}, err
			}
			tree, err := treeArg(in.Tree)
			if err != nil {
				return nil, showUIResult{}, err
			}
			info := s.Info()
			id, err := d.agentUI.Show(s.ID(), cmpOr(info.GetName(), info.GetAgent()), in.ID, in.Title, tree)
			if err != nil {
				return nil, showUIResult{}, err
			}
			d.closeUIWith(s)
			out := showUIResult{Block: agentui.Name(s.ID(), id), ID: id, Placed: placedRight}
			if in.Place != nil && in.Place.Parent != "" {
				if err := d.placeAgentBlock(ctx, s, id, in.Place.place()); err != nil {
					out.Note = fmt.Sprintf("It stays in the right panel: %v", err)
				} else {
					out.Placed = in.Place.Parent
				}
			}
			return nil, out, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: toolUpdateUI, Description: "Replace the tree of a block you show with show_ui, and its title if given: the window gets the changes. At most 10 times a second."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in updateUIArgs) (*sdk.CallToolResult, uiResult, error) {
			s, err := d.projectCaller(peer)
			if err != nil {
				return nil, uiResult{}, err
			}
			tree, err := treeArg(in.Tree)
			if err != nil {
				return nil, uiResult{}, err
			}
			if err := d.agentUI.Update(s.ID(), in.ID, in.Title, tree); err != nil {
				return nil, uiResult{}, err
			}
			return nil, uiResult{Block: agentui.Name(s.ID(), in.ID)}, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: toolCloseUI, Description: "Close a block you show with show_ui; the window lays itself out as before it."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in closeUIArgs) (*sdk.CallToolResult, uiResult, error) {
			s, err := d.projectCaller(peer)
			if err != nil {
				return nil, uiResult{}, err
			}
			if err := d.agentUI.Close(s.ID(), in.ID); err != nil {
				return nil, uiResult{}, err
			}
			return nil, uiResult{Block: agentui.Name(s.ID(), in.ID)}, nil
		})
}

// treeArg is a tool's tree as the JSON a plugin's render gives.
func treeArg(tree map[string]any) (json.RawMessage, error) {
	if tree == nil {
		return nil, errors.New("tree is required: a node, {kind, ...}")
	}
	return json.Marshal(tree)
}

// closeUIWith closes a session's blocks once it ends, watching it once.
func (d *Daemon) closeUIWith(s *session.Session) {
	d.uiWatched.watch(s.ID(), func(stop <-chan struct{}) {
		select {
		case <-s.Done():
		case <-stop:
		}
		if d.agentUI != nil {
			d.agentUI.CloseSession(s.ID())
		}
	})
}

// placeAgentBlock moves a block to p, once the user allows, as a change
// of the layout document: the change staged shows the block there
// meanwhile, and the allowance is a layout change's, this time, for the
// document, or always. The document itself is never changed: the place
// is the block's, in the overlay.
func (d *Daemon) placeAgentBlock(ctx context.Context, s *session.Session, id string, p agentui.Place) error {
	name, err := layoutName(p.Document)
	if err != nil {
		return err
	}
	cur, _, _, err := d.layoutNow(name)
	if err != nil {
		return err
	}
	block := agentui.Name(s.ID(), id)
	node := map[string]any{"id": block, "block": block}
	if p.Size > 0 {
		node["size"] = p.Size
	}
	op := layoutdoc.Op{Op: "insert", Parent: p.Parent, Node: node}
	if p.Index >= 0 {
		op.Index = &p.Index
	}
	next, err := layoutdoc.Apply(cur, []layoutdoc.Op{op}, d.blockCatalog())
	if err != nil {
		return err
	}
	if !d.layouts.allows(name) {
		if !d.layouts.watched() {
			return errLayoutNoWindow
		}
		doc, err := json.Marshal(next)
		if err != nil {
			return err
		}
		info := s.Info()
		summary := fmt.Sprintf("place its block %s in %s", id, p.Parent)
		staged, err := d.layouts.begin(&protocol.LayoutStage{
			Id: approvalID(), Document: name, Doc: doc, SessionId: s.ID(),
			Agent: cmpOr(info.GetName(), info.GetAgent()), Tool: toolShowUI, Summary: summary,
		})
		if err != nil {
			return err
		}
		defer func() {
			d.layouts.end(staged)
			d.changes.fireWorkspace()
		}()
		d.layouts.setPhase(staged, protocol.LayoutApprove, time.Time{})
		d.changes.fireWorkspace()
		_, after := d.layouts.clock()
		option, err := d.askLayout(ctx, layoutOrigin{session: s}, staged, layoutChange{tool: toolShowUI, name: name, doc: doc, summary: summary}, after)
		if err != nil {
			return err
		}
		d.layouts.allow(option, name)
	}
	if p.Index < 0 {
		p.Index = -1
	}
	p.Document = name
	return d.agentUI.Place(s.ID(), id, p)
}

// agentUIEvent gives an agent the user's action on its block, as a
// message of the window's in its inbox, which is typed in at its prompt
// as any message is.
func (d *Daemon) agentUIEvent(ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	sid, text, err := d.agentUI.Event(ev)
	if err != nil {
		return nil, err
	}
	s, ok := d.sessions.get(sid)
	if !ok {
		return nil, errors.New("the agent's session is gone")
	}
	if _, err := d.projectAgents.messages.event(sid, text, d.projectScope(s.Info())); err != nil {
		return nil, err
	}
	return &protocol.PluginDone{}, nil
}

// onceSet runs a watcher once a key, until the key is forgotten, and tracks
// active watchers so daemon shutdown can stop and await them.
type onceSet struct {
	mu      sync.Mutex
	keys    map[string]bool
	stopped bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

func (o *onceSet) watch(key string, fn func(stop <-chan struct{})) {
	o.mu.Lock()
	if o.stopped || o.keys[key] {
		o.mu.Unlock()
		return
	}
	if o.keys == nil {
		o.keys = map[string]bool{}
	}
	if o.stop == nil {
		o.stop = make(chan struct{})
	}
	stop := o.stop
	o.keys[key] = true
	o.wg.Add(1)
	o.mu.Unlock()

	go func() {
		defer o.wg.Done()
		defer o.Forget(key)
		fn(stop)
	}()
}

func (o *onceSet) Forget(key string) {
	o.mu.Lock()
	delete(o.keys, key)
	o.mu.Unlock()
}

func (o *onceSet) stopAndWait() {
	o.mu.Lock()
	if !o.stopped {
		o.stopped = true
		if o.stop != nil {
			close(o.stop)
		}
	}
	o.mu.Unlock()
	o.wg.Wait()
}

// handleListAgentBlocks lists the blocks the agents of the node the
// request names show, this one's by default.
func (d *Daemon) handleListAgentBlocks(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, _ bool) error {
	return serve(d, ctx, stream, env, protocol.TypeAgentBlocks, func(*protocol.ListAgentBlocks) (*protocol.AgentBlocks, error) {
		return &protocol.AgentBlocks{Blocks: d.agentUI.List()}, nil
	})
}
