// Package daemon implements the sanbo background service: it owns local agent
// sessions, tracks peer daemons, and serves front-ends and peers over QUIC.
package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// registry holds the daemon's local sessions.
type registry struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
}

func newRegistry() *registry {
	return &registry{sessions: map[string]*session.Session{}}
}

func (r *registry) add(s *session.Session) {
	r.mu.Lock()
	r.sessions[s.ID()] = s
	r.mu.Unlock()
}

func (r *registry) get(id string) (*session.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	return s, ok
}

func (r *registry) remove(id string) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

func (r *registry) list() []*protocol.SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*protocol.SessionInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.Info())
	}
	sortByStart(out)
	return out
}

// sortByStart orders sessions by when they started.
func sortByStart(sessions []*protocol.SessionInfo) {
	sort.Slice(sessions, func(i, j int) bool {
		return protocol.TimeOf(sessions[i].GetStartedAt()).Before(protocol.TimeOf(sessions[j].GetStartedAt()))
	})
}

func (r *registry) closeAll() {
	r.mu.Lock()
	sessions := make([]*session.Session, 0, len(r.sessions))
	for id, s := range r.sessions {
		sessions = append(sessions, s)
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
}

// peerTable tracks daemons discovered on the mesh. It keeps nodes of its own,
// copying what it is given and what it hands out, so no caller shares one.
type peerTable struct {
	mu    sync.RWMutex
	nodes map[string]*protocol.Node
}

func newPeerTable() *peerTable {
	return &peerTable{nodes: map[string]*protocol.Node{}}
}

// upsert records a node this daemon observed directly (discovery, a probe, an
// inbound hello) and stamps it as seen now. It reports whether the node was
// new or moved.
func (p *peerTable) upsert(node *protocol.Node) bool {
	if node.GetId() == "" {
		return false
	}
	node = proto.CloneOf(node)
	node.LastSeen = timestamppb.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, existed := p.nodes[node.GetId()]
	if existed {
		// A configured peer stays static however else it is seen, so prune
		// never drops it; details the new sighting lacks are kept.
		if prev.GetSource() == "static" {
			node.Source = "static"
		}
		if node.GetOs() == "" {
			node.Os, node.Arch, node.Version = prev.GetOs(), prev.GetArch(), prev.GetVersion()
		}
		if node.GetAgents() == nil {
			node.Agents = prev.GetAgents()
		}
	}
	p.nodes[node.GetId()] = node
	return !existed || prev.GetAddr() != node.GetAddr()
}

// merge folds in a node learned second-hand through gossip. The sender's
// LastSeen is kept rather than refreshed, so peers relaying a dead node to
// each other cannot keep it alive past peerTTL. A node already known keeps
// its address and source and only advances its LastSeen.
func (p *peerTable) merge(node *protocol.Node) {
	if node.GetId() == "" || node.GetLastSeen() == nil {
		return
	}
	node = proto.CloneOf(node)
	if now := time.Now(); protocol.TimeOf(node.GetLastSeen()).After(now) {
		node.LastSeen = timestamppb.New(now) // do not trust a peer clock running ahead
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, existed := p.nodes[node.GetId()]
	if !existed {
		node.Source = "gossip"
		p.nodes[node.GetId()] = node
		return
	}
	if protocol.TimeOf(node.GetLastSeen()).After(protocol.TimeOf(prev.GetLastSeen())) {
		prev.LastSeen = node.GetLastSeen()
		if node.GetAgents() != nil {
			prev.Agents = node.GetAgents() // what it launches, as of that sighting
		}
	}
}

func (p *peerTable) get(id string) (*protocol.Node, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	node, ok := p.nodes[id]
	return proto.CloneOf(node), ok
}

func (p *peerTable) list() []*protocol.Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*protocol.Node, 0, len(p.nodes))
	for _, node := range p.nodes {
		out = append(out, proto.CloneOf(node))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// prune drops nodes not seen within ttl.
func (p *peerTable) prune(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, node := range p.nodes {
		if node.GetSource() != "self" && node.GetSource() != "static" && protocol.TimeOf(node.GetLastSeen()).Before(cutoff) {
			delete(p.nodes, id)
		}
	}
}

func newSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("sanbo: no entropy for session id: %v", err))
	}
	return hex.EncodeToString(buf[:])
}
