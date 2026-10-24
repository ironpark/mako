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

func (r *registry) list() []protocol.SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.SessionInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
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

// peerTable tracks daemons discovered on the mesh.
type peerTable struct {
	mu    sync.RWMutex
	nodes map[string]protocol.Node
}

func newPeerTable() *peerTable {
	return &peerTable{nodes: map[string]protocol.Node{}}
}

// upsert records a node this daemon observed directly (discovery, a probe, an
// inbound hello) and stamps it as seen now. It reports whether the node was
// new or moved.
func (p *peerTable) upsert(node protocol.Node) bool {
	if node.ID == "" {
		return false
	}
	node.LastSeen = time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, existed := p.nodes[node.ID]
	if existed {
		// A configured peer stays static however else it is seen, so prune
		// never drops it; details the new sighting lacks are kept.
		if prev.Source == "static" {
			node.Source = "static"
		}
		if node.OS == "" {
			node.OS, node.Arch, node.Version = prev.OS, prev.Arch, prev.Version
		}
		if node.Agents == nil {
			node.Agents = prev.Agents
		}
	}
	p.nodes[node.ID] = node
	return !existed || prev.Addr != node.Addr
}

// merge folds in a node learned second-hand through gossip. The sender's
// LastSeen is kept rather than refreshed, so peers relaying a dead node to
// each other cannot keep it alive past peerTTL. A node already known keeps
// its address and source and only advances its LastSeen.
func (p *peerTable) merge(node protocol.Node) {
	if node.ID == "" || node.LastSeen.IsZero() {
		return
	}
	if now := time.Now(); node.LastSeen.After(now) {
		node.LastSeen = now // do not trust a peer clock running ahead
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, existed := p.nodes[node.ID]
	if !existed {
		node.Source = "gossip"
		p.nodes[node.ID] = node
		return
	}
	if node.LastSeen.After(prev.LastSeen) {
		prev.LastSeen = node.LastSeen
		if node.Agents != nil {
			prev.Agents = node.Agents // what it launches, as of that sighting
		}
		p.nodes[node.ID] = prev
	}
}

func (p *peerTable) get(id string) (protocol.Node, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	node, ok := p.nodes[id]
	return node, ok
}

func (p *peerTable) list() []protocol.Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]protocol.Node, 0, len(p.nodes))
	for _, node := range p.nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// prune drops nodes not seen within ttl.
func (p *peerTable) prune(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, node := range p.nodes {
		if node.Source != "self" && node.Source != "static" && node.LastSeen.Before(cutoff) {
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
