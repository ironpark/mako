package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/atomicfile"
	"github.com/ironpark/mako/internal/protocol"
)

// The window layout documents are the workspace's (window_layouts, one
// register each), changed by the window as any of it is and by agents
// through the MCP tools (agent_layout.go). An agent's change is not the
// workspace's until the user has it: it waits on this node alone, staged,
// for the user to allow it, unless they allowed such changes before, and,
// for a whole document set, to keep it once shown, which it reverts by
// itself after layoutKeepFor. The node's windows draw the staged document
// meanwhile, as Workspace.stage tells them; only a kept change is written
// into the workspace, and so reaches the peers. So a change reverted, or
// never allowed, was never anywhere but here.
//
// What is kept here alone, never synced: the stage; what the user allowed
// agents to change without asking (layout-allow.json); and each
// document's earlier versions, as this node's edits left them, which
// undo_layout goes back to.

// layoutKeepFor is how long a document set and shown waits to be kept.
const layoutKeepFor = 10 * time.Second

// layoutAskFor is how long an agent's change waits for the user to allow
// it before it is refused.
const layoutAskFor = 10 * time.Minute

// layoutHistory is how many earlier versions of a document are kept.
const layoutHistory = 20

var (
	errLayoutBusy     = errors.New("another change of the window layout is waiting for the user; try again once it is answered")
	errLayoutNoWindow = errors.New("no window of mako is open on this node for the user to see the change; nothing was changed")
	errLayoutDenied   = errors.New("the user declined the change of the window layout")
	errLayoutReverted = errors.New("the user did not keep the window layout, and it was reverted")
	errLayoutChanged  = errors.New("the document changed since it was read; read it again with get_layout")
	errLayoutNoUndo   = errors.New("this node has no earlier version of the document to go back to")
)

// windowLayouts is the node's side of the window layout documents.
type windowLayouts struct {
	mu      sync.Mutex
	windows int // the node's windows watching, which show a stage
	stage   *stagedLayout
	history map[string][]layoutVersion // by document, the latest last
	allowed layoutAllowances
	path    string // layout-allow.json; "" keeps nothing on disk
	// now and after are the clock a stage is timed by: the time, and a
	// timer. Tests set their own.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// layoutVersion is a document as it was: its JSON, nil for none.
type layoutVersion struct{ doc []byte }

// layoutAllowances is what the user allowed agents to change without
// asking: every document, or those named, as a space uses one; and what
// each plugin may, apart, as the user allowed it.
type layoutAllowances struct {
	Always    bool                        `json:"always,omitempty"`
	Documents []string                    `json:"documents,omitempty"`
	Plugins   map[string]layoutAllowances `json:"plugins,omitempty"`
}

// stagedLayout is a change waiting for the user: what the windows are
// told, and the answers to it.
type stagedLayout struct {
	pb      *protocol.LayoutStage // guarded by windowLayouts.mu
	answers chan layoutAnswer
}

// layoutAnswer is the user's: allow (with an option) or deny in phase
// approve, keep or revert in phase keep.
type layoutAnswer struct{ decision, option, message string }

// load reads what the user allowed, kept in dir.
func (w *windowLayouts) load(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.path = filepath.Join(dir, "layout-allow.json")
	if data, err := os.ReadFile(w.path); err == nil {
		_ = json.Unmarshal(data, &w.allowed)
	}
}

func (w *windowLayouts) clock() (func() time.Time, func(time.Duration) <-chan time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now, after := w.now, w.after
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = time.After
	}
	return now, after
}

// hold counts a window of this node's as showing the changes staged, until
// it calls release, as its watch stream asks.
func (w *windowLayouts) hold() (release func()) {
	w.mu.Lock()
	w.windows++
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		w.windows--
		w.mu.Unlock()
	}
}

// watched reports whether a window of this node's shows the changes.
func (w *windowLayouts) watched() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.windows > 0
}

// allows reports whether agents may change the document without asking.
func (w *windowLayouts) allows(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.allowed.Always || slices.Contains(w.allowed.Documents, name)
}

// allow lets agents change the document, or every one, without asking
// from now on, as the user's option says; "" allows nothing more.
func (w *windowLayouts) allow(option, name string) {
	w.mu.Lock()
	switch option {
	case protocol.LayoutAllowAlways:
		w.allowed.Always = true
	case protocol.LayoutAllowSpace:
		if !slices.Contains(w.allowed.Documents, name) {
			w.allowed.Documents = append(w.allowed.Documents, name)
		}
	default:
		w.mu.Unlock()
		return
	}
	w.saveLocked()
	w.mu.Unlock()
}

// allowsPlugin reports whether the plugin may change the document without
// asking.
func (w *windowLayouts) allowsPlugin(id, name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.allowed.Plugins[id]
	return a.Always || slices.Contains(a.Documents, name)
}

// allowPlugin lets the plugin change the document, or every one, without
// asking from now on, as the user's option says; "" allows nothing more.
func (w *windowLayouts) allowPlugin(id, option, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.allowed.Plugins[id]
	switch option {
	case protocol.LayoutAllowAlways:
		a.Always = true
	case protocol.LayoutAllowSpace:
		if !slices.Contains(a.Documents, name) {
			a.Documents = append(a.Documents, name)
		}
	default:
		return
	}
	if w.allowed.Plugins == nil {
		w.allowed.Plugins = map[string]layoutAllowances{}
	}
	w.allowed.Plugins[id] = a
	w.saveLocked()
}

// forgetPlugin has the plugin ask again for every change, as it is
// removed: another of its ID is not given what it was.
func (w *windowLayouts) forgetPlugin(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.allowed.Plugins[id]; ok {
		delete(w.allowed.Plugins, id)
		w.saveLocked()
	}
}

// forget has agents and plugins ask again for every change.
func (w *windowLayouts) forget() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.allowed = layoutAllowances{}
	w.saveLocked()
}

func (w *windowLayouts) saveLocked() {
	if w.path == "" {
		return
	}
	if data, err := json.Marshal(w.allowed); err == nil {
		_ = atomicfile.Write(w.path, data)
	}
}

// remember keeps prev as the document's version before an edit of this
// node's, which undo goes back to.
func (w *windowLayouts) remember(name string, prev []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.history == nil {
		w.history = map[string][]layoutVersion{}
	}
	h := append(w.history[name], layoutVersion{doc: slices.Clone(prev)})
	if len(h) > layoutHistory {
		h = slices.Delete(h, 0, len(h)-layoutHistory)
	}
	w.history[name] = h
}

// last is the version undo goes back to, without taking it.
func (w *windowLayouts) last(name string) (layoutVersion, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.history[name]
	if len(h) == 0 {
		return layoutVersion{}, false
	}
	return h[len(h)-1], true
}

// drop takes the last version, as undo went back to it.
func (w *windowLayouts) drop(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h := w.history[name]; len(h) > 0 {
		w.history[name] = h[:len(h)-1]
	}
}

// begin stages a change, one at a time.
func (w *windowLayouts) begin(pb *protocol.LayoutStage) (*stagedLayout, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stage != nil {
		return nil, errLayoutBusy
	}
	w.stage = &stagedLayout{pb: pb, answers: make(chan layoutAnswer, 1)}
	return w.stage, nil
}

// end takes the change off the stage.
func (w *windowLayouts) end(s *stagedLayout) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stage == s {
		w.stage = nil
	}
}

// setPhase moves the change to phase, to wait until deadline, 0 for none.
func (w *windowLayouts) setPhase(s *stagedLayout, phase string, deadline time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pb := proto.CloneOf(s.pb)
	pb.Phase, pb.Deadline = phase, 0
	if !deadline.IsZero() {
		pb.Deadline = deadline.UnixMilli()
	}
	s.pb = pb
}

// current is the change staged, for the node's front-ends; nil for none.
func (w *windowLayouts) current() *protocol.LayoutStage {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stage == nil || w.stage.pb.GetPhase() == "" {
		return nil
	}
	return w.stage.pb
}

// owns reports whether id is the staged change's, and so an approval of
// a layout rather than of a tool call.
func (w *windowLayouts) owns(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stage != nil && w.stage.pb.GetId() == id
}

// answer passes the user's answer to the staged change of the ID, as its
// phase takes it: allow or deny while it is to be approved, keep or
// revert once applied.
func (w *windowLayouts) answer(id string, a layoutAnswer) error {
	w.mu.Lock()
	s := w.stage
	ok := s != nil && s.pb.GetId() == id
	phase := s.pb.GetPhase()
	w.mu.Unlock()
	if !ok {
		return errors.New("the layout change was answered or withdrawn")
	}
	switch {
	case phase == protocol.LayoutApprove && (a.decision == protocol.DecisionAllow || a.decision == protocol.DecisionDeny || a.decision == protocol.DecisionTerminal):
	case phase == protocol.LayoutKeep && (a.decision == protocol.LayoutKeep || a.decision == protocol.LayoutRevert):
	default:
		return errors.New("the layout change is not waiting for that answer")
	}
	select {
	case s.answers <- a:
	default: // answered twice at once; the first stands
	}
	return nil
}
