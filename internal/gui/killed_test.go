package gui

import (
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A poll begun before a kill went through still lists the session; it
// stays out of the list until the daemon leaves it out, so the list does
// not lose it, show it again and lose it again.
func TestKilledSessionStaysGoneFromStalePolls(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	at := time.Now()
	m.killed["a"] = at
	list := func(ids ...string) []*protocol.SessionInfo {
		var out []*protocol.SessionInfo
		for _, id := range ids {
			out = append(out, &protocol.SessionInfo{Id: id})
		}
		return out
	}
	ids := func(ss []*protocol.SessionInfo) (out []string) {
		for _, s := range ss {
			out = append(out, s.GetId())
		}
		return out
	}
	if got := ids(m.dropKilled(list("a", "b"), at.Add(time.Second))); len(got) != 1 || got[0] != "b" {
		t.Fatalf("a stale poll brought the killed session back: %v", got)
	}
	// The daemon has let it go: forgotten, so an ID used again shows.
	m.dropKilled(list("b"), at.Add(2*time.Second))
	if _, ok := m.killed["a"]; ok {
		t.Fatal("a session the daemon no longer lists is still remembered")
	}
	// One the kill did not end shows again once killedLinger has passed.
	m.killed["c"] = at
	if got := ids(m.dropKilled(list("c"), at.Add(killedLinger+time.Second))); len(got) != 1 {
		t.Fatalf("a session that outlived its kill stays hidden: %v", got)
	}
}
