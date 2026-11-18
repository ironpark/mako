package gui

// The Android app sorts sessions into the desktop sidebar's sections and
// spaces with a port of groupProjects and placeOf (app/android,
// ui/SessionGroups.kt); its unit tests check the port against what these
// make of a workspace and sessions, written here as fixtures. Run with
// MAKO_ANDROID_FIXTURES=1 to write them again after a change here;
// otherwise this fails while the app's are not what the sidebar makes.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	shared "github.com/ironpark/mako/internal/workspace"
)

func TestAndroidGroupingFixture(t *testing.T) {
	at := &protocol.HLC{WallTime: 1_800_000_000_000, NodeId: "n1"}
	later := &protocol.HLC{WallTime: 1_800_000_000_001, NodeId: "n1"}
	s := func(v string) *protocol.StringReg { return &protocol.StringReg{Value: v, Clock: at} }
	in := func(rank string) *protocol.ElementMeta {
		return &protocol.ElementMeta{AddedAt: at, Rank: rank, RankClock: at}
	}
	set := func(keys ...string) map[string]*protocol.ElementMeta {
		out := map[string]*protocol.ElementMeta{}
		for i, k := range keys {
			out[k] = in(string(rune('a' + i)))
		}
		return out
	}
	root := func(id, remote, owner, rank string, locs ...string) *protocol.ProjectRootEntry {
		return &protocol.ProjectRootEntry{Id: id, CreatedAt: at, Rank: s(rank), Remote: s(remote), Owner: s(owner), Locations: set(locs...)}
	}
	st := &protocol.WorkspaceState{
		DefaultSpace: s("misc"),
		Spaces: map[string]*protocol.SpaceEntry{
			"work": {Id: "work", CreatedAt: at, Name: s("Work"), Rank: s("a"), Owners: set("github.com/acme"),
				Projects: set("repo:github.com/me/blog"), Hidden: set("repo:github.com/acme/legacy")},
			"me":   {Id: "me", CreatedAt: at, Name: s("Me"), Rank: s("b"), Owners: set("github.com/me", "github.com/acme")},
			"misc": {Id: "misc", CreatedAt: at, Name: s("Misc"), Rank: s("c")},
			"old":  {Id: "old", CreatedAt: at, DeletedAt: later, Name: s("Old"), Rank: s("0"), Projects: set("pNotes")},
		},
		Projects: map[string]*protocol.ProjectEntry{
			"pApi": {Id: "pApi", CreatedAt: at, Name: s("API"), Rank: s("a"), Roots: map[string]*protocol.ProjectRootEntry{
				"github.com/acme/server": func() *protocol.ProjectRootEntry {
					r := root("github.com/acme/server", "github.com/acme/server", "github.com/acme", "a", "n1\x00/w/server")
					r.Declined = set("n4\x00/x/server")
					return r
				}(),
				"n1\x00/w/web": root("n1\x00/w/web", "", "", "b", "n1\x00/w/web"),
			}},
			// No new clones join it.
			"pLib": {Id: "pLib", CreatedAt: at, Name: s(""), Rank: s("b"), NoJoin: &protocol.BoolReg{Value: true, Clock: at},
				Roots: map[string]*protocol.ProjectRootEntry{"github.com/acme/lib": root("github.com/acme/lib", "github.com/acme/lib", "github.com/acme", "a", "n1\x00/w/lib")}},
			// Two roots claim one remote: a clone of it joins neither.
			"pTwinA": {Id: "pTwinA", CreatedAt: at, Name: s("TwinA"), Rank: s("c"),
				Roots: map[string]*protocol.ProjectRootEntry{"github.com/me/twin": root("github.com/me/twin", "github.com/me/twin", "github.com/me", "a", "n1\x00/w/twin")}},
			"pTwinB": {Id: "pTwinB", CreatedAt: at, Name: s("TwinB"), Rank: s("d"),
				Roots: map[string]*protocol.ProjectRootEntry{"github.com/me/twin": root("github.com/me/twin", "github.com/me/twin", "github.com/me", "a", "n2\x00/w/twin")}},
			// A folder and one inside it: the deeper holds what is in it.
			"pHome": {Id: "pHome", CreatedAt: at, Name: s("Home"), Rank: s("e"),
				Roots: map[string]*protocol.ProjectRootEntry{"n2\x00/home/me": root("n2\x00/home/me", "", "", "a", "n2\x00/home/me")}},
			"pNotes": {Id: "pNotes", CreatedAt: at, Name: s("Notes"), Rank: s("f"),
				Roots: map[string]*protocol.ProjectRootEntry{"n2\x00/home/me/notes": root("n2\x00/home/me/notes", "", "", "a", "n2\x00/home/me/notes")}},
			// A project made of a folder's section that the folder left.
			"n3\x00/tmp/x": {Id: "n3\x00/tmp/x", CreatedAt: at, Name: s("Was x"), Rank: s("g"),
				Roots: map[string]*protocol.ProjectRootEntry{"r": root("r", "", "", "a", "n3\x00/elsewhere")}},
			"pGone": {Id: "pGone", CreatedAt: at, DeletedAt: later, Name: s("Gone"), Rank: s("h"),
				Roots: map[string]*protocol.ProjectRootEntry{"n1\x00/w/gone": root("n1\x00/w/gone", "", "", "a", "n1\x00/w/gone")}},
		},
		Names: map[string]*protocol.StringReg{"repo:github.com/me/blog": s("Blog"), "n3\x00/scratch": s("Scratch")},
	}
	type sess struct {
		ID, Node, Project, Repo, Remote, Owner string
	}
	sessions := []sess{
		{"a", "n1", "/w/server/cmd", "/w/server", "github.com/acme/server", "github.com/acme"},
		{"b", "n2", "/srv/server", "/srv/server", "github.com/acme/server", "github.com/acme"}, // joins
		{"c", "n4", "/x/server", "/x/server", "github.com/acme/server", "github.com/acme"},     // declined
		{"d", "n1", "/w/web/src", "", "", ""},
		{"e", "n2", "/tmp/web-fix", "/w/web", "", ""}, // a worktree, but on another node
		{"f", "n1", "/w/lib", "/w/lib", "github.com/acme/lib", "github.com/acme"},
		{"g", "n2", "/w/lib", "/w/lib", "github.com/acme/lib", "github.com/acme"}, // no join
		{"h", "n3", "/w/twin", "/w/twin", "github.com/me/twin", "github.com/me"},  // claimed twice
		{"i", "n2", "/home/me/notes/2026", "", "", ""},
		{"j", "n2", "/home/me/src/app", "/home/me/src/app", "", ""}, // a repository inside home
		{"k", "n2", "/home/me/Downloads", "", "", ""},
		{"l", "n3", "/tmp/x", "", "", ""},
		{"m", "n3", "/scratch", "", "", ""},
		{"n", "n1", "/b/blog", "/b/blog", "github.com/me/blog", "github.com/me"},
		{"o", "n2", "/c/blog", "/c/blog", "github.com/me/blog", "github.com/me"},
		{"p", "n1", "/w/legacy", "/w/legacy", "github.com/acme/legacy", "github.com/acme"},
		{"q", "n1", "/w/gone", "", "", ""},
		{"r", "n1", "/a/app", "", "", ""},
		{"s", "n2", "/b/app", "", "", ""},
		{"t", "n2", "/w/twin", "/w/twin", "github.com/me/twin", "github.com/me"},
	}
	var infos []*protocol.SessionInfo
	for _, x := range sessions {
		infos = append(infos, &protocol.SessionInfo{Id: x.ID, NodeId: x.Node, Project: x.Project, Repo: x.Repo, Remote: x.Remote, Owner: x.Owner})
	}
	var doc workspaceDoc
	if err := json.Unmarshal(shared.View(st), &doc); err != nil {
		t.Fatal(err)
	}
	l := doc.spaceList
	groups := groupProjects(infos, nil, nil, l.Projects, l.Names)
	set2 := spaceSetOf(l, "")
	type section struct {
		Key      string   `json:"key"`
		Label    string   `json:"label"`
		Owners   []string `json:"owners"`
		Sessions []string `json:"sessions"`
		Roots    []int    `json:"roots"`
		Space    int      `json:"space"`
	}
	var out []section
	for _, g := range groups {
		owners := groupOwners(g)
		if owners == nil {
			owners = []string{}
		}
		sec := section{Key: g.Key, Label: groupName(g), Owners: owners, Sessions: []string{}, Roots: []int{}}
		for _, x := range g.Sessions {
			sec.Sessions = append(sec.Sessions, x.GetId())
			if r, ok := g.RootOf[x.GetId()]; ok {
				sec.Roots = append(sec.Roots, r)
			} else {
				sec.Roots = append(sec.Roots, -1)
			}
		}
		sec.Space, _ = set2.placeOf(g.Key, owners)
		out = append(out, sec)
	}
	body, _ := json.MarshalIndent(map[string]any{"sessions": sessions, "sections": out}, "", " ")
	data, _ := proto.MarshalOptions{Deterministic: true}.Marshal(st)
	androidFixture(t, "grouping-state.pb", data)
	androidFixture(t, "grouping.json", body)
}

// androidFixture writes data as the app's fixture name, or checks that the
// one the app has is data.
func androidFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join("../../app/android/app/src/test/resources/workspace", name)
	if os.Getenv("MAKO_ANDROID_FIXTURES") != "" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if have, err := os.ReadFile(path); err != nil || !bytes.Equal(have, data) {
		t.Errorf("the app's fixture %s is not what the sidebar makes (%v): run MAKO_ANDROID_FIXTURES=1 go test ./internal/workspace ./internal/gui -run Android", name, err)
	}
}
