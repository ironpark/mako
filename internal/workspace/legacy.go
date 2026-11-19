package workspace

import (
	"cmp"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// A workspace kept before it was a CRDT: the window's document (see
// document.go), with the edit it was, the later winning whole.

// docFile is workspace.json as a window kept it before, or a Workspace's
// doc: the document and the edit it is.
type docFile struct {
	// Version is the edit's time in Unix nanoseconds, or below
	// countedEdits a count of the edits of a workspace begun from nothing.
	Version int64    `json:"version"`
	NodeID  string   `json:"node_id"`
	Doc     document `json:"doc"`
}

// countedEdits is where counted versions end and timed ones begin: 2001,
// in Unix nanoseconds.
const countedEdits = 1_000_000_000 * 1_000_000_000

// FromLegacy is the CRDT state of a workspace written down as JSON, as a
// window's workspace.json or a Workspace's doc has it. Every entry is
// stamped with the document's edit: at its time, to the millisecond, by
// the node that made it, or, for a workspace whose edits were counted, at
// WallTime 0 with the count. Orders become ranks spread evenly, and a
// root's ID is its remote, or without one its first location's key. Two
// nodes converting the same document come to the same state, so that the
// first merge of theirs changes nothing.
//
// A document stamped further ahead of physNow than crdt.MaxClockDrift was
// edited by a clock that was wrong, which would have its entries win over
// every edit until real time caught up; it is taken as stamped at physNow.
// What is not valid UTF-8, which a state could not be encoded with, is
// left out.
func FromLegacy(doc []byte, physNow int64) (*protocol.WorkspaceState, error) {
	var f docFile
	if err := json.Unmarshal(doc, &f); err != nil {
		return nil, err
	}
	at := legacyStamp(f.Version, f.NodeID)
	if crdt.Quarantined(at, physNow) {
		at.WallTime, at.Logical = physNow, 0
	}
	return convert(f.Doc, at), nil
}

// legacyStamp is the stamp of a document's edit.
func legacyStamp(version int64, node string) crdt.HLC {
	if !utf8.ValidString(node) {
		node = ""
	}
	if version < countedEdits {
		return crdt.HLC{Logical: uint32(min(max(version, 1), math.MaxUint32)), NodeID: node}
	}
	return crdt.HLC{WallTime: version / 1e6, NodeID: node}
}

// valid reports whether every string can go into a state.
func valid(ss ...string) bool {
	for _, s := range ss {
		if !utf8.ValidString(s) {
			return false
		}
	}
	return true
}

// convert is doc as a state, every entry stamped at.
func convert(doc document, at crdt.HLC) *protocol.WorkspaceState {
	c := converter{at: at}
	st := &protocol.WorkspaceState{NoAll: c.boolean(doc.NoAll), DefaultSpace: c.str(doc.Default)}
	ids := uniqueIDs(len(doc.Spaces), func(i int) string { return doc.Spaces[i].ID })
	for i, sp := range doc.Spaces {
		if ids[i] == "" {
			continue
		}
		if st.Spaces == nil {
			st.Spaces = map[string]*protocol.SpaceEntry{}
		}
		st.Spaces[sp.ID] = &protocol.SpaceEntry{
			Id:        sp.ID,
			CreatedAt: c.stamp(),
			Name:      c.str(sp.Name),
			Rank:      c.str(crdt.Initial(i, len(doc.Spaces))),
			Owners:    c.set(sp.Owners),
			Projects:  c.set(sp.Projects),
			Hidden:    c.set(sp.Hidden),
			Color:     c.str(sp.Color),
			Layout:    c.str(sp.Layout),
		}
	}
	ids = uniqueIDs(len(doc.Projects), func(i int) string { return doc.Projects[i].ID })
	for i, p := range doc.Projects {
		if ids[i] == "" {
			continue
		}
		if st.Projects == nil {
			st.Projects = map[string]*protocol.ProjectEntry{}
		}
		st.Projects[p.ID] = &protocol.ProjectEntry{
			Id:        p.ID,
			CreatedAt: c.stamp(),
			Name:      c.str(p.Name),
			NoJoin:    c.boolean(p.NoJoin),
			GiveDirs:  c.boolean(p.GiveDirs),
			Roots:     c.roots(p.Roots),
			Rank:      c.str(crdt.Initial(i, len(doc.Projects))),
		}
	}
	for key, name := range doc.Names {
		if valid(key) {
			if st.Names == nil {
				st.Names = map[string]*protocol.StringReg{}
			}
			st.Names[key] = c.str(name)
		}
	}
	for key, look := range doc.Looks {
		if valid(key) {
			if st.Looks == nil {
				st.Looks = map[string]*protocol.LookEntry{}
			}
			st.Looks[key] = &protocol.LookEntry{Icon: c.str(look.Icon), Color: c.str(look.Color)}
		}
	}
	pins := make([]string, len(doc.PinnedProjects))
	for i, p := range doc.PinnedProjects {
		pins[i] = p.Section + "\x00" + p.Root
	}
	st.PinnedProjects = c.set(pins)
	st.PinnedSessions = c.set(doc.Pinned)
	st.Layouts = c.layouts(doc.Layouts, doc.TabNodes)
	for name, body := range doc.WindowLayouts {
		if valid(name) && json.Valid(body) {
			if st.WindowLayouts == nil {
				st.WindowLayouts = map[string]*protocol.LayoutEntry{}
			}
			st.WindowLayouts[name] = &protocol.LayoutEntry{Doc: slices.Clone([]byte(body)), Clock: c.stamp()}
		}
	}
	return st
}

// uniqueIDs is the IDs of n entries, "" for one whose ID is empty, not
// valid, or an earlier one's: the first of an ID wins.
func uniqueIDs(n int, id func(int) string) []string {
	out := make([]string, n)
	seen := map[string]bool{}
	for i := range n {
		if s := id(i); s != "" && valid(s) && !seen[s] {
			seen[s] = true
			out[i] = s
		}
	}
	return out
}

// converter makes the entries of a converted document.
type converter struct{ at crdt.HLC }

func (c converter) stamp() *protocol.HLC { return pbHLC(c.at) }

// str is a register of v; nil for a v that is not valid.
func (c converter) str(v string) *protocol.StringReg {
	if !valid(v) {
		return nil
	}
	return &protocol.StringReg{Value: v, Clock: c.stamp()}
}

func (c converter) boolean(v bool) *protocol.BoolReg {
	return &protocol.BoolReg{Value: v, Clock: c.stamp()}
}

// set is the elements, added and ranked in their order, each once.
func (c converter) set(elems []string) map[string]*protocol.ElementMeta {
	ids := uniqueIDs(len(elems), func(i int) string { return elems[i] })
	var out map[string]*protocol.ElementMeta
	for i, e := range ids {
		if e == "" {
			continue
		}
		if out == nil {
			out = map[string]*protocol.ElementMeta{}
		}
		out[e] = &protocol.ElementMeta{AddedAt: c.stamp(), Rank: crdt.Initial(i, len(ids)), RankClock: c.stamp()}
	}
	return out
}

// roots are a project's roots, by the IDs rootID gives them.
func (c converter) roots(roots []docRoot) map[string]*protocol.ProjectRootEntry {
	var out map[string]*protocol.ProjectRootEntry
	for i, r := range roots {
		id := rootID(r)
		if _, taken := out[id]; taken || id == "" {
			id += "#" + strconv.Itoa(i)
		}
		if !valid(id) {
			continue
		}
		if out == nil {
			out = map[string]*protocol.ProjectRootEntry{}
		}
		out[id] = &protocol.ProjectRootEntry{
			Id:        id,
			CreatedAt: c.stamp(),
			Rank:      c.str(crdt.Initial(i, len(roots))),
			Label:     c.str(r.Label),
			Remote:    c.str(r.Remote),
			Owner:     c.str(r.Owner),
			Locations: c.set(folderKeys(r.Locations)),
			Declined:  c.set(folderKeys(r.Declined)),
		}
	}
	return out
}

// rootID is a root's ID: the one the document gives it, as a state's
// view does; else its remote, or without one its first location's key, of
// those a state can hold, as it is made. Two roots of one project with the same,
// or a root with neither, take their index after it.
func rootID(r docRoot) string {
	if r.ID != "" && valid(r.ID) {
		return r.ID
	}
	if r.Remote != "" && valid(r.Remote) {
		return r.Remote
	}
	for _, key := range folderKeys(r.Locations) {
		if key != "" && valid(key) {
			return key
		}
	}
	return ""
}

func folderKeys(folders []docFolder) []string {
	out := make([]string, len(folders))
	for i, f := range folders {
		if f.Node != "" && f.Path != "" {
			out[i] = LocationKey(f.Node, f.Path)
		}
	}
	return out
}

// layouts are the pane trees, each with the nodes of its own tabs. A tree
// kept by its space's ID alone is the whole space's, "id|"; where both
// keys are there, the newer, "id|", is taken.
func (c converter) layouts(trees map[string]*docPane, nodes map[string]string) map[string]*protocol.LayoutEntry {
	var out map[string]*protocol.LayoutEntry
	keys := slices.SortedFunc(maps.Keys(trees), func(a, b string) int {
		// The keys with a context first, so they win over the old ones.
		return cmp.Or(-cmp.Compare(strings.Count(a, "|"), strings.Count(b, "|")), cmp.Compare(a, b))
	})
	for _, key := range keys {
		tree := trees[key]
		if !strings.Contains(key, "|") {
			key += "|"
		}
		if _, taken := out[key]; taken || tree == nil || !valid(key) {
			continue
		}
		doc, err := json.Marshal(tree)
		if err != nil {
			continue
		}
		e := &protocol.LayoutEntry{Doc: doc, Clock: c.stamp()}
		for _, tab := range tree.tabs() {
			if node, ok := nodes[tab]; ok && valid(tab, node) {
				if e.TabNodes == nil {
					e.TabNodes = map[string]string{}
				}
				e.TabNodes[tab] = node
			}
		}
		if out == nil {
			out = map[string]*protocol.LayoutEntry{}
		}
		out[key] = e
	}
	return out
}
