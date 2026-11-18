package workspace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// The Android app reads the workspace with a port of this package's
// reading (app/android, workspace/), and gives the deltas a window's Diff
// gives; its unit tests check both against fixtures this test writes, so
// that the two cannot drift apart unseen. Run with MAKO_ANDROID_FIXTURES=1
// to write them again after a change here; otherwise it fails while the
// fixtures in the app are not what this package makes.

// androidFixtures is where the app's tests read the fixtures from.
const androidFixtures = "../../app/android/app/src/test/resources/workspace"

// androidFixture writes data as the fixture name, or checks that the one
// the app has is data.
func androidFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join(androidFixtures, name)
	if os.Getenv("MAKO_ANDROID_FIXTURES") != "" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if have, err := os.ReadFile(path); err != nil || !bytes.Equal(have, data) {
		t.Errorf("the app's fixture %s is not what this package makes (%v): run MAKO_ANDROID_FIXTURES=1 go test ./internal/workspace ./internal/gui -run Android", name, err)
	}
}

// fxH, fxS, fxIn and fxOut are a stamp, a register, and an element in a
// set and one taken out, as a daemon would have stamped them.
func fxH(wall int64, logical uint32, node string) *protocol.HLC {
	return &protocol.HLC{WallTime: wall, Logical: logical, NodeId: node}
}

func fxS(v string, c *protocol.HLC) *protocol.StringReg {
	return &protocol.StringReg{Value: v, Clock: c}
}

func fxIn(c *protocol.HLC, rank string) *protocol.ElementMeta {
	return &protocol.ElementMeta{AddedAt: c, Rank: rank, RankClock: c}
}

func fxOut(added, removed *protocol.HLC, rank string) *protocol.ElementMeta {
	return &protocol.ElementMeta{AddedAt: added, RemovedAt: removed, Rank: rank, RankClock: added}
}

// fxT is a time the fixture's stamps count from.
const fxT = 1_800_000_000_000

// androidState is a state full of what a reader can get wrong: entities
// deleted, made again and tied, references to deleted projects, ranks
// alike, missing and ordered differently in UTF-16, and set elements in
// and out.
func androidState() *protocol.WorkspaceState {
	t1, t2, t3 := fxH(fxT+1, 0, "n1"), fxH(fxT+2, 0, "n2"), fxH(fxT+3, 0, "n1")
	return &protocol.WorkspaceState{
		NoAll:        &protocol.BoolReg{Value: false, Clock: t1},
		DefaultSpace: fxS("home", t1),
		Spaces: map[string]*protocol.SpaceEntry{
			"work": {
				Id: "work", CreatedAt: t1, Name: fxS("Work", t1), Rank: fxS("V", t1), Color: fxS("blue", t1),
				Owners: map[string]*protocol.ElementMeta{
					"github.com/acme": fxIn(t1, "V"),
					"github.com/old":  fxOut(t1, t2, "k"),
				},
				Projects: map[string]*protocol.ElementMeta{
					"pApi":                   fxIn(t1, "F"),
					"pGone":                  fxIn(t1, "V"), // a deleted project's: left out
					"repo:github.com/me/web": fxIn(t1, "k"),
				},
				Hidden: map[string]*protocol.ElementMeta{"free:pNotes": fxIn(t1, "")},
			},
			"home": {
				Id: "home", CreatedAt: t1, Name: fxS("Home", t1), Rank: fxS("F", t1), Color: fxS("green", t1),
				Owners: map[string]*protocol.ElementMeta{"github.com/me": fxIn(t1, "")},
			},
			// Deleted after it was made, and renamed meanwhile: hidden.
			"gone": {Id: "gone", CreatedAt: t1, DeletedAt: t2, Name: fxS("Gone", t3), Rank: fxS("0V", t1)},
			// Made again after it was deleted: shown.
			"back": {Id: "back", CreatedAt: t3, DeletedAt: t2, Name: fxS("Back", t1), Rank: fxS("z", t1)},
			// Created and deleted at the same stamp: hidden, deletion wins a tie.
			"tie": {Id: "tie", CreatedAt: t2, DeletedAt: t2, Name: fxS("Tie", t1), Rank: fxS("y", t1)},
			// Created and deleted at the same time and count on two nodes: the
			// hash of the key and the node decides.
			"hashA": {Id: "hashA", CreatedAt: fxH(fxT+5, 1, "nA"), DeletedAt: fxH(fxT+5, 1, "nB"), Name: fxS("HashA", t1), Rank: fxS("x", t1)},
			"hashB": {Id: "hashB", CreatedAt: fxH(fxT+5, 1, "nB"), DeletedAt: fxH(fxT+5, 1, "nA"), Name: fxS("HashB", t1), Rank: fxS("x", t1)},
			"hashC": {Id: "hashC", CreatedAt: fxH(fxT+5, 1, "nA"), DeletedAt: fxH(fxT+5, 1, "nB"), Name: fxS("HashC", t1), Rank: fxS("x", t1)},
			"hashD": {Id: "hashD", CreatedAt: fxH(fxT+5, 1, "nB"), DeletedAt: fxH(fxT+5, 1, "nA"), Name: fxS("HashD", t1), Rank: fxS("x", t1)},
			"hashE": {Id: "hashE", CreatedAt: fxH(fxT+5, 1, "nA"), DeletedAt: fxH(fxT+5, 1, "nB"), Name: fxS("HashE", t1), Rank: fxS("x", t1)},
			"hashF": {Id: "hashF", CreatedAt: fxH(fxT+5, 1, "nB"), DeletedAt: fxH(fxT+5, 1, "nA"), Name: fxS("HashF", t1), Rank: fxS("x", t1)},
			// One rank, IDs that UTF-8 and UTF-16 order differently.
			"ｚ": {Id: "ｚ", CreatedAt: t1, Name: fxS("Fullwidth", t1), Rank: fxS("k", t1)},
			"😀": {Id: "😀", CreatedAt: t1, Name: fxS("Emoji", t1), Rank: fxS("k", t1)},
			// No rank: first.
			"norank": {Id: "norank", CreatedAt: t1, Name: fxS("", t1)},
		},
		Projects: map[string]*protocol.ProjectEntry{
			"pApi": {
				Id: "pApi", CreatedAt: t1, Name: fxS("API", t1), Rank: fxS("V", t1),
				GiveDirs: &protocol.BoolReg{Value: true, Clock: t1},
				Roots: map[string]*protocol.ProjectRootEntry{
					"github.com/acme/server": {
						Id: "github.com/acme/server", CreatedAt: t1, Rank: fxS("F", t1), Label: fxS("server", t1),
						Remote: fxS("github.com/acme/server", t1), Owner: fxS("github.com/acme", t1),
						Locations: map[string]*protocol.ElementMeta{
							"n1\x00/w/server":   fxIn(t1, "F"),
							"n2\x00/srv/server": fxIn(t2, "V"),
							"n1\x00/old/server": fxOut(t1, t2, "k"),
						},
						Declined: map[string]*protocol.ElementMeta{"n3\x00/tmp/server": fxIn(t1, "")},
					},
					"github.com/acme/web": {
						Id: "github.com/acme/web", CreatedAt: t1, Rank: fxS("k", t1),
						Remote: fxS("github.com/acme/web", t1), Owner: fxS("github.com/acme", t1),
						Locations: map[string]*protocol.ElementMeta{"n1\x00/w/web": fxIn(t1, "")},
					},
					"n1\x00/w/gone": {
						Id: "n1\x00/w/gone", CreatedAt: t1, DeletedAt: t2, Rank: fxS("0V", t1),
						Locations: map[string]*protocol.ElementMeta{"n1\x00/w/gone": fxIn(t1, "")},
					},
				},
			},
			"pNotes": {
				Id: "pNotes", CreatedAt: t1, Name: fxS("", t1),
				NoJoin: &protocol.BoolReg{Value: true, Clock: t1},
				Roots: map[string]*protocol.ProjectRootEntry{
					"n2\x00/home/me/notes": {
						Id: "n2\x00/home/me/notes", CreatedAt: t1,
						// Added and removed at one stamp: in.
						Locations: map[string]*protocol.ElementMeta{"n2\x00/home/me/notes": fxOut(t2, t2, "")},
					},
				},
			},
			"pGone": {
				Id: "pGone", CreatedAt: t1, DeletedAt: t3, Name: fxS("Gone", t1), Rank: fxS("0V", t1),
				Roots: map[string]*protocol.ProjectRootEntry{
					"n1\x00/w/gone": {Id: "n1\x00/w/gone", CreatedAt: t1, Locations: map[string]*protocol.ElementMeta{"n1\x00/w/gone": fxIn(t1, "")}},
				},
			},
		},
		Names: map[string]*protocol.StringReg{
			"repo:github.com/me/web": fxS("Website", t1),
			"n1\x00/tmp/scratch":     fxS("Scratch", t1),
			"pGone":                  fxS("Still gone", t2),
			"cleared":                fxS("", t2),
		},
		Looks: map[string]*protocol.LookEntry{
			"pApi":                   {Icon: fxS("rocket", t1), Color: fxS("green", t1)},
			"repo:github.com/me/web": {Icon: fxS("", t1), Color: fxS("purple", t1)},
			"pGone":                  {Icon: fxS("star", t1)},
			"blank":                  {Icon: fxS("", t1), Color: fxS("", t1)},
		},
		PinnedProjects: map[string]*protocol.ElementMeta{
			"pApi\x00github.com/acme/web": fxIn(t1, "k"),
			"repo:github.com/me/web\x00":  fxIn(t1, "F"),
			"pGone\x00":                   fxIn(t1, "0V"),
			"pNotes\x00":                  fxOut(t1, t2, "V"),
		},
		PinnedSessions: map[string]*protocol.ElementMeta{
			"s1": fxIn(t1, "V"),
			"s2": fxIn(t2, "F"),
			"s3": fxOut(t1, t2, "z"),
			"s4": fxOut(t2, t2, "k"),
			"s5": fxIn(t1, ""),
			// Ranked alike, as two nodes pinning at one spot do: by ID.
			"s7": fxIn(t1, "k"),
		},
		Layouts: map[string]*protocol.LayoutEntry{
			"work|project:pApi": {Doc: []byte(`{"ID":1,"Group":{"ID":1,"Tabs":["t1"],"Active":"t1"},"Vertical":false,"Ratio":0,"First":null,"Second":null}`), TabNodes: map[string]string{"t1": "n1"}, Clock: t1},
			"gone|":             {Doc: []byte(`{"ID":1,"Group":null,"Vertical":false,"Ratio":0,"First":null,"Second":null}`), Clock: t1},
		},
	}
}

// androidEdit is the delta Diff makes of st's document changed by fn, as a
// window that pins or unpins makes it.
func androidEdit(st *protocol.WorkspaceState, fn func(doc map[string]any)) *protocol.WorkspaceState {
	prev := View(st)
	var doc map[string]any
	if err := json.Unmarshal(prev, &doc); err != nil {
		panic(err)
	}
	fn(doc)
	next, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	d, err := Diff(st, prev, next)
	if err != nil {
		panic(err)
	}
	return d
}

// fxList is the list a document holds under key.
func fxList(doc map[string]any, key string) []any {
	l, _ := doc[key].([]any)
	return l
}

// fxWithout is l without what match matches.
func fxWithout(l []any, match func(any) bool) []any {
	var out []any
	for _, v := range l {
		if !match(v) {
			out = append(out, v)
		}
	}
	return out
}

// The fixtures: the state, its view, the deltas of pins and unpins, and
// ranks between pairs.
func TestAndroidFixtures(t *testing.T) {
	st := androidState()
	write := func(name string, m proto.Message) {
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		androidFixture(t, name, data)
	}
	write("workspace.pb", st)
	androidFixture(t, "view.json", View(st))
	pinSession := func(id string) func(map[string]any) {
		return func(doc map[string]any) { doc["pinned"] = append(fxList(doc, "pinned"), id) }
	}
	unpinSession := func(id string) func(map[string]any) {
		return func(doc map[string]any) {
			doc["pinned"] = fxWithout(fxList(doc, "pinned"), func(v any) bool { return v == id })
		}
	}
	pinScope := func(section, root string) func(map[string]any) {
		return func(doc map[string]any) {
			k := map[string]any{"section": section}
			if root != "" {
				k["root"] = root
			}
			doc["pinned_projects"] = append(fxList(doc, "pinned_projects"), k)
		}
	}
	unpinScope := func(section, root string) func(map[string]any) {
		return func(doc map[string]any) {
			doc["pinned_projects"] = fxWithout(fxList(doc, "pinned_projects"), func(v any) bool {
				m := v.(map[string]any)
				r, _ := m["root"].(string)
				return m["section"] == section && r == root
			})
		}
	}
	write("pin-session-new.pb", androidEdit(st, pinSession("s9")))
	write("pin-session-again.pb", androidEdit(st, pinSession("s3")))
	write("unpin-session.pb", androidEdit(st, unpinSession("s1")))
	write("pin-project.pb", androidEdit(st, pinScope("pNotes", "")))
	write("unpin-project.pb", androidEdit(st, unpinScope("repo:github.com/me/web", "")))
	// An empty set, whose first pin is ranked from nothing.
	empty := &protocol.WorkspaceState{}
	write("pin-session-first.pb", androidEdit(empty, pinSession("only")))
	// Ranks between pairs, which the app makes to pin last.
	var ranks []byte
	for _, c := range [][2]string{
		{"", ""}, {"V", ""}, {"", "V"}, {"F", "V"}, {"V", "W"}, {"z", ""}, {"zz", ""}, {"a", "a1"},
		{"a0", "b"}, {"0V", "0W"}, {"k", "k"}, {"y", "x"}, {"1", "2"}, {"Vz", "W"}, {"0001", ""}, {"", "01"},
	} {
		ranks = append(ranks, []byte(c[0]+"\t"+c[1]+"\t"+crdt.Between(c[0], c[1])+"\n")...)
	}
	androidFixture(t, "ranks.tsv", ranks)
}
