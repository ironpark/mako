// Package agentui is the blocks agents show in the window (the show_ui,
// update_ui and close_ui tools of the mako MCP server): trees of the same
// vocabulary as a plugin's render result, read by the same reader under
// the same limits (internal/uiblock), each a block agent:<session>/<id>
// of the session that showed it, closed with it. The blocks, and where
// each is placed, are the daemon's alone, in memory: a node-local overlay
// on the window's layout document, never written into the workspace, so
// no other node has a place for a session it does not run. A user's
// action on a block goes back to its agent as a message (Event).
package agentui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

// The bounds of a session's blocks.
const (
	MaxPerSession = 4
	maxTitle      = 80
	// Updates is the shortest time between two updates of a block on a
	// stream, as for a plugin's; updates within it go as one.
	Updates = 100 * time.Millisecond
	// updatesPerSecond bounds update_ui calls of a block; eventsPerSecond
	// the front-ends' events, as for a plugin's.
	updatesPerSecond = 10
	eventsPerSecond  = 20
)

var (
	// blockID is a block's ID within its session, as the block names it.
	blockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	// sessionID is a session's, as this node's daemon makes them.
	sessionID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// Place is where a block is placed, beyond its default, the right panel:
// as the Index-th child (-1 for the last) of the node Parent of the
// window layout document Document, Size px along it. A zero Place is
// the default.
type Place struct {
	Document string  `json:"document,omitempty"`
	Parent   string  `json:"parent,omitempty"`
	Index    int     `json:"index,omitempty"`
	Size     float64 `json:"size,omitempty"`
}

// Store is the node's agents' blocks.
type Store struct {
	mu      sync.Mutex
	blocks  map[string]*block // by name
	changed chan struct{}     // closed when any block next changes
	notify  func()            // tells the front-ends the list changed
	now     func() time.Time
}

type block struct {
	name, session, id string
	agent, title      string
	place             Place
	tree              *uiblock.Tree
	order             uint64
	updates, events   *rate
}

// New is a store that calls notify whenever a block comes, goes or moves.
func New(notify func()) *Store {
	if notify == nil {
		notify = func() {}
	}
	return &Store{blocks: map[string]*block{}, changed: make(chan struct{}), notify: notify, now: time.Now}
}

// Name is the block of a session's ID.
func Name(session, id string) string { return "agent:" + session + "/" + id }

// split takes a block's name apart.
func split(name string) (session, id string, ok bool) {
	rest, ok := strings.CutPrefix(name, "agent:")
	if !ok {
		return "", "", false
	}
	session, id, ok = strings.Cut(rest, "/")
	return session, id, ok && sessionID.MatchString(session) && blockID.MatchString(id)
}

// bumpLocked wakes the streams.
func (s *Store) bumpLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Show shows a block of the session's: its tree as JSON, which is read as
// a plugin's render result is. An empty id takes the next free one. It
// returns the block's ID.
func (s *Store) Show(session, agent, id, title string, tree json.RawMessage) (string, error) {
	if !sessionID.MatchString(session) {
		return "", errors.New("the session cannot show blocks")
	}
	if err := checkTitle(title); err != nil {
		return "", err
	}
	t, err := uiblock.Read(tree)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	var next uint64
	for _, b := range s.blocks {
		if b.session == session {
			n++
		}
		next = max(next, b.order)
	}
	if n >= MaxPerSession {
		return "", fmt.Errorf("a session shows at most %d blocks; close one with close_ui", MaxPerSession)
	}
	if id == "" {
		for i := 1; ; i++ {
			if _, taken := s.blocks[Name(session, fmt.Sprintf("ui-%d", i))]; !taken {
				id = fmt.Sprintf("ui-%d", i)
				break
			}
		}
	}
	if !blockID.MatchString(id) {
		return "", fmt.Errorf("id %q is not letters, digits, _ and - of at most 32, starting with a letter or digit", id)
	}
	name := Name(session, id)
	if _, ok := s.blocks[name]; ok {
		return "", fmt.Errorf("block %s is shown already; change it with update_ui", id)
	}
	s.blocks[name] = &block{name: name, session: session, id: id, agent: agent, title: title, tree: t, order: next + 1,
		updates: newRate(updatesPerSecond, time.Second), events: newRate(eventsPerSecond, time.Second)}
	s.bumpLocked()
	go s.notify()
	return id, nil
}

// Update replaces a block's tree, and its title unless title is nil. Only
// the session that showed a block names it.
func (s *Store) Update(session, id string, title *string, tree json.RawMessage) error {
	if title != nil {
		if err := checkTitle(*title); err != nil {
			return err
		}
	}
	t, err := uiblock.Read(tree)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[Name(session, id)]
	if !ok {
		return fmt.Errorf("this session shows no block %q", id)
	}
	if !b.updates.take(s.now()) {
		return fmt.Errorf("a block is updated at most %d times a second", updatesPerSecond)
	}
	b.tree = t
	if title != nil && *title != b.title {
		b.title = *title
		go s.notify()
	}
	s.bumpLocked()
	return nil
}

// Place moves a block of the session's to p.
func (s *Store) Place(session, id string, p Place) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[Name(session, id)]
	if !ok {
		return fmt.Errorf("this session shows no block %q", id)
	}
	b.place = p
	s.bumpLocked()
	go s.notify()
	return nil
}

// Close closes a block of the session's.
func (s *Store) Close(session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := Name(session, id)
	if _, ok := s.blocks[name]; !ok {
		return fmt.Errorf("this session shows no block %q", id)
	}
	delete(s.blocks, name)
	s.bumpLocked()
	go s.notify()
	return nil
}

// CloseSession closes every block of the session, as it ended.
func (s *Store) CloseSession(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	closed := false
	for name, b := range s.blocks {
		if b.session == session {
			delete(s.blocks, name)
			closed = true
		}
	}
	if closed {
		s.bumpLocked()
		go s.notify()
	}
}

// Has says the session shows the block of the id.
func (s *Store) Has(session, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blocks[Name(session, id)]
	return ok
}

// List is the blocks, in the order shown, as the front-ends place them.
func (s *Store) List() []*protocol.AgentBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	bs := make([]*block, 0, len(s.blocks))
	for _, b := range s.blocks {
		bs = append(bs, b)
	}
	slices.SortFunc(bs, func(a, b *block) int { return int(a.order) - int(b.order) })
	out := make([]*protocol.AgentBlock, len(bs))
	for i, b := range bs {
		out[i] = &protocol.AgentBlock{Name: b.name, SessionId: b.session, Agent: b.agent, Title: b.title,
			Document: b.place.Document, Parent: b.place.Parent, Index: int32(b.place.Index), Size: b.place.Size}
	}
	return out
}

// BlockProps makes the store a layoutdoc.Catalog of its blocks, which
// take no props.
func (s *Store) BlockProps(name string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocks[name]; !ok {
		return nil, false
	}
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, true
}

// view is a block as a stream sends it.
func (s *Store) view(name string) uiblock.View {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blocks[name]
	if !ok {
		return uiblock.View{Gone: true, Changed: s.changed}
	}
	return uiblock.View{Tree: b.tree.Root, Session: b.session, Agent: b.agent, Title: b.title,
		State: protocol.PluginRunning, Changed: s.changed}
}

// Watch follows a block, as a plugin's is followed, until ctx ends or
// the block closes.
func (s *Store) Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error {
	if _, _, ok := split(name); !ok {
		return fmt.Errorf("%q is not an agent's block", name)
	}
	s.mu.Lock()
	_, ok := s.blocks[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no block %s on this node", name)
	}
	return uiblock.Follow(ctx, name, Updates, func() uiblock.View { return s.view(name) }, send)
}

func checkTitle(t string) error {
	if len([]rune(t)) > maxTitle || strings.ContainsFunc(t, uiblock.Spoofing) {
		return fmt.Errorf("a title is at most %d characters, without control or reordering characters", maxTitle)
	}
	return nil
}

// rate lets n events through a window, and refuses the rest.
type rate struct {
	n      int
	window time.Duration
	times  []time.Time
}

func newRate(n int, window time.Duration) *rate { return &rate{n: n, window: window} }

func (r *rate) take(now time.Time) bool {
	for len(r.times) > 0 && now.Sub(r.times[0]) > r.window {
		r.times = r.times[1:]
	}
	if len(r.times) >= r.n {
		return false
	}
	r.times = append(r.times, now)
	return true
}
