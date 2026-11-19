// Package crdt holds the conflict-free replicated types the workspace is
// made of: a hybrid logical clock to order edits, a last-writer-wins
// register, a last-writer-wins element set, and fractional ranks to order
// a list without indices. Each joins with another of its kind so that the
// order, repetition and grouping of the joins do not change the result,
// which is what lets nodes that saw the same edits in any order agree.
//
// It knows nothing of mako's messages; internal/workspace maps them on.
package crdt

import (
	"cmp"
	"hash/fnv"
	"math"
	"time"
)

// MaxClockDrift is how far ahead of a node's own clock a stamp it takes in
// may be. A stamp further ahead says the clock that made it is wrong, and
// taking it would let that node's edits win over everyone's until real
// time caught up; the entry it stamps waits, quarantined, until then.
const MaxClockDrift = 60 * time.Second

// maxDrift is MaxClockDrift in the milliseconds stamps are counted in.
const maxDrift = int64(MaxClockDrift / time.Millisecond)

// HLC is a hybrid logical clock's stamp: the physical time of an edit in
// Unix milliseconds, a counter for the edits within one millisecond or
// while the physical clock stands behind, and the node that made it.
//
// A stamp with WallTime 0 was made before its node first joined another's
// state, and is counted rather than timed (Clock); any timed stamp is
// later than every counted one.
type HLC struct {
	WallTime int64
	Logical  uint32
	NodeID   string
}

// IsZero reports whether h is the zero stamp, which is earlier than any
// other: what an entry never written carries.
func (h HLC) IsZero() bool { return h == HLC{} }

// After reports whether h is later than other, in the order of the entry
// key both stamp. Time decides, then the counter. Between two nodes' stamps
// of the same time and count, a hash of the key and the node decides, so
// that which node wins a tie differs from entry to entry rather than the
// same node always winning; the node IDs themselves decide only a tie of
// hashes. The order is total: of two different stamps, exactly one is
// after the other.
func (h HLC) After(other HLC, key string) bool { return Compare(h, other, key) > 0 }

// Compare orders a and b as After does: -1 when a is earlier, 1 when it is
// later and 0 when they are the same stamp.
func Compare(a, b HLC, key string) int {
	if a == b {
		return 0
	}
	// The zero stamp is the least, whatever the hash of an empty node ID
	// would say.
	if a.IsZero() {
		return -1
	}
	if b.IsZero() {
		return 1
	}
	if c := cmp.Or(cmp.Compare(a.WallTime, b.WallTime), cmp.Compare(a.Logical, b.Logical)); c != 0 {
		return c
	}
	return cmp.Or(cmp.Compare(tieHash(key, a.NodeID), tieHash(key, b.NodeID)), cmp.Compare(a.NodeID, b.NodeID))
}

// tieHash is the FNV-1a hash of key and node together.
func tieHash(key, node string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	h.Write([]byte(node))
	return h.Sum64()
}

// Quarantined reports whether a stamp is too far ahead of physNow, the
// receiving node's time in Unix milliseconds, to take in: further than
// MaxClockDrift. A counted stamp never is.
func Quarantined(h HLC, physNow int64) bool { return h.WallTime > horizon(physNow) }

// horizon is the latest wall time a node at physNow takes in, without
// overflowing for a physNow that stands for "whenever".
func horizon(physNow int64) int64 {
	if physNow > math.MaxInt64-maxDrift {
		return math.MaxInt64
	}
	return physNow + maxDrift
}

// Clock is one node's clock, which stamps that node's edits. Only the
// daemon owns one: every front-end's edit is stamped by its daemon, so a
// node's stamps rise one after another however many windows, apps and
// tools edit through it.
//
// Until it is anchored, the clock counts rather than times: its stamps are
// WallTime 0 and a counter. A node that has never joined another's state
// would otherwise have its first few edits, made by a clock that knows
// nothing of the mesh, win over a workspace the others have used for
// months, wherever both wrote the same key; counted, they lose to every
// timed stamp. The daemon anchors the clock when it first joins a peer's
// state, and keeps that it did.
//
// A Clock is not safe for concurrent use; its owner's lock guards it.
type Clock struct {
	node     string
	anchored bool
	// last is the latest timed stamp made or taken in, and count the
	// highest counted one.
	last  HLC
	count uint32
	// seen is the latest timed stamp taken in from elsewhere, which a
	// clock set back keeps above.
	seen HLC
}

// NewClock is a clock for node, anchored or not. Its owner brings it up to
// date with every stamp of the state it keeps, through Observe, before it
// stamps anything, so that it carries on from where it was.
func NewClock(node string, anchored bool) *Clock {
	return &Clock{node: node, anchored: anchored}
}

// NodeID is the node the clock stamps for.
func (c *Clock) NodeID() string { return c.node }

// Anchored reports whether the clock times its stamps.
func (c *Clock) Anchored() bool { return c.anchored }

// Anchor makes the clock time its stamps from now on.
func (c *Clock) Anchor() { c.anchored = true }

// Now is a fresh stamp, later than every stamp the clock made or took in
// before. physNow is the node's time in Unix milliseconds.
//
// A clock found further ahead of physNow than MaxClockDrift was set wrong
// and has been put right: it is set back to physNow, or to the latest
// stamp it took in that is still within reach, rather than stamping on
// from a time the others refuse. The entries it stamped while wrong are
// for its owner to stamp again; Ahead tells it beforehand.
func (c *Clock) Now(physNow int64) HLC {
	if !c.anchored {
		c.count++
		return HLC{Logical: c.count, NodeID: c.node}
	}
	if c.Ahead(physNow) {
		c.rewind(physNow)
	}
	// A timed stamp is never 0, which would be a counted one.
	pt := max(physNow, 1)
	switch {
	case pt > c.last.WallTime:
		c.last.WallTime, c.last.Logical = pt, 0
	case c.last.Logical == math.MaxUint32:
		c.last.WallTime, c.last.Logical = c.last.WallTime+1, 0
	default:
		c.last.Logical++
	}
	return HLC{WallTime: c.last.WallTime, Logical: c.last.Logical, NodeID: c.node}
}

// Ahead reports whether the clock stands further ahead of physNow than
// MaxClockDrift: it was set wrong, and the node's clock is now right.
func (c *Clock) Ahead(physNow int64) bool { return c.last.WallTime > horizon(physNow) }

// rewind sets the clock back to physNow, but not below what it took in
// from elsewhere and may still take in.
func (c *Clock) rewind(physNow int64) {
	c.last = HLC{WallTime: physNow}
	if !Quarantined(c.seen, physNow) && pair(c.seen).compare(pair(c.last)) > 0 {
		c.last = HLC{WallTime: c.seen.WallTime, Logical: c.seen.Logical}
	}
}

// Observe takes in a stamp received with another's state, or read back
// from the node's own, at physNow, and reports whether to keep the entry
// it stamps. A stamp within MaxClockDrift of physNow moves the clock up to
// it. One further ahead is to be quarantined: the entry is left out, and
// the clock does not move, so a peer with a wrong clock cannot drag every
// other node's stamps ahead with it. The stamp itself is never changed;
// were the receiver to change it, every node would hold a different value
// and their states would never agree.
func (c *Clock) Observe(remote HLC, physNow int64) bool {
	if Quarantined(remote, physNow) {
		return false
	}
	if remote.WallTime == 0 {
		c.count = max(c.count, remote.Logical)
		return true
	}
	if pair(remote).compare(pair(c.last)) > 0 {
		c.last = HLC{WallTime: remote.WallTime, Logical: remote.Logical}
	}
	if remote.NodeID != c.node && pair(remote).compare(pair(c.seen)) > 0 {
		c.seen = remote
	}
	return true
}

// Last is the latest timed stamp the clock made or took in.
func (c *Clock) Last() HLC {
	return HLC{WallTime: c.last.WallTime, Logical: c.last.Logical, NodeID: c.node}
}

// stamp is a stamp's time and count, ordered together.
type stamp struct {
	wall    int64
	logical uint32
}

// pair is h without its node.
func pair(h HLC) stamp { return stamp{h.WallTime, h.Logical} }

// compare orders stamps by time, then count.
func (a stamp) compare(b stamp) int {
	return cmp.Or(cmp.Compare(a.wall, b.wall), cmp.Compare(a.logical, b.logical))
}
