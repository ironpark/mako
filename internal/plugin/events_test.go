package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// fakeLogs is a session's log as the daemon reads it for a page: windows
// by the offset read up to.
type fakeLogs struct {
	mu      sync.Mutex
	windows map[int64]*protocol.Chat // by end
	latest  int64
	reads   int
}

func (f *fakeLogs) read(_ context.Context, id string, before int64) (*protocol.Chat, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if id != "s1" {
		return nil, 0, fmt.Errorf("no session %s", id)
	}
	if before == 0 {
		before = f.latest
	}
	c, ok := f.windows[before]
	if !ok {
		return nil, 0, fmt.Errorf("no window ending at %d", before)
	}
	return c, before, nil
}

func messages(from, n int, text func(i int) string) []*protocol.ChatMessage {
	out := make([]*protocol.ChatMessage, n)
	for i := range n {
		out[i] = &protocol.ChatMessage{Role: "assistant", Text: text(from + i)}
	}
	return out
}

var oneSession = []*protocol.SessionInfo{{Id: "s1", Name: "one", Agent: "claude", State: protocol.StateRunning, Status: "idle", NodeId: "n"}}

// pager reads every page of s1's transcript, and shows what it read.
const pager = `
	let before = "", all = [], cursors = [], trunc = false;
	for (let i = 0; i < 10; i++) {
		const page = await mako.sessions.readTranscript({ sessionId: "s1", before, limit: LIMIT });
		all = page.messages.map((m) => m.text.length > 20 ? "long" + m.text.length : m.text).concat(all);
		trunc = trunc || page.truncated;
		cursors.push(page.before);
		before = page.before;
		if (!before) break;
	}
	s.msg = "read " + all.join(",") + " cursors " + cursors.join("|") + " truncated " + trunc;`

func TestPluginReadsATranscriptPageByPage(t *testing.T) {
	logs := &fakeLogs{latest: 1000, windows: map[int64]*protocol.Chat{
		1000: {Supported: true, Start: 500, Messages: messages(10, 45, func(i int) string { return fmt.Sprint(i) })},
		500:  {Supported: true, Start: 0, Messages: messages(0, 10, func(i int) string { return fmt.Sprint(i) })},
	}}
	r := startRig(t, Config{Limits: testLimits, Sessions: func() []*protocol.SessionInfo { return oneSession }, Transcript: logs.read})
	r.write("pager", map[string]string{ManifestName: manifest("pager", []string{"sessions:read"}, ""),
		"main.js": block(strings.Replace(pager, "LIMIT", "20", 1), `ui.text(s.msg)`)})
	r.state("pager", protocol.PluginWaiting)
	r.grant("pager")
	want := make([]string, 55)
	for i := range want {
		want[i] = fmt.Sprint(i)
	}
	tree := r.watch("plugin:pager/ui").until("every page", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "read ") || hasText(n, "error")
	})
	got := texts(tree)[0]
	if wantText := "read " + strings.Join(want, ",") + " cursors 1000.20|1000.40|500.0| truncated false"; got != wantText {
		t.Fatalf("got  %s\nwant %s", got, wantText)
	}
}

func TestATranscriptPageIsBounded(t *testing.T) {
	big := strings.Repeat("é", 6000) // 12 000 bytes, cut at 8 KiB on a rune
	logs := &fakeLogs{latest: 9, windows: map[int64]*protocol.Chat{
		9: {Supported: true, Messages: messages(0, 60, func(int) string { return big })},
	}}
	r := startRig(t, Config{Limits: testLimits, Sessions: func() []*protocol.SessionInfo { return oneSession }, Transcript: logs.read})
	r.write("pager", map[string]string{ManifestName: manifest("pager", []string{"sessions:read"}, ""),
		"main.js": block(`const page = await mako.sessions.readTranscript({ sessionId: "s1", limit: 500 });
			s.msg = "n " + page.messages.length + " len " + page.messages[0].text.length + " before " + page.before + " truncated " + page.truncated;`, `ui.text(s.msg)`)})
	r.state("pager", protocol.PluginWaiting)
	r.grant("pager")
	// 50 asked at most; 8 KiB of é is 4 096 of them; 256 KiB holds 31.
	r.watch("plugin:pager/ui").until("a bounded page", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "n 31 len 4096 before 9.31 truncated true")
	})
}

func TestTranscriptReadsOutOfBoundsAreRefused(t *testing.T) {
	logs := &fakeLogs{latest: 9, windows: map[int64]*protocol.Chat{9: {Supported: true, Messages: messages(0, 3, func(i int) string { return fmt.Sprint(i) })}}}
	r := startRig(t, Config{Limits: testLimits, Sessions: func() []*protocol.SessionInfo { return oneSession }, Transcript: logs.read})
	cases := map[string]struct{ load, want string }{
		"elsewhere": {`await mako.sessions.readTranscript({ sessionId: "remote" })`, "no session"},
		"cursor":    {`await mako.sessions.readTranscript({ sessionId: "s1", before: "../../etc" })`, "before is a cursor"},
		"negative":  {`await mako.sessions.readTranscript({ sessionId: "s1", before: "-5.0" })`, "before is a cursor"},
		"skip":      {`await mako.sessions.readTranscript({ sessionId: "s1", before: "9.99999" })`, "before is a cursor"},
		"flood":     {`for (let i = 0; i < 40; i++) await mako.sessions.readTranscript({ sessionId: "s1" })`, "times a minute"},
		"event":     {`await mako.sessions.subscribe({ events: ["everything"] })`, "no kind of event"},
	}
	for id, c := range cases {
		r.write(id, map[string]string{ManifestName: manifest(id, []string{"sessions:read"}, ""), "main.js": block(c.load+`; s.msg = "got through"`, `ui.text(s.msg)`)})
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		tree := r.watch("plugin:"+id+"/ui").until(c.want, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error:") })
		if !hasText(tree, c.want) {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	healthy(t, r)
}

// listener is a plugin whose command subscribes to kinds, and whose
// handlers keep what they were told in its store, under "seen".
func listener(id string, kinds string) map[string]string {
	return map[string]string{
		ManifestName: manifest(id, []string{"sessions:read", "storage"}, ""),
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("x") });
    m.commands.register("go", async () => { await mako.sessions.subscribe({ events: ` + kinds + ` }); });
  },
  on: {
    sessionStarted: (e) => note("started " + e.session.id),
    sessionEnded: (e) => note("ended " + e.session.id),
    sessionSlept: (e) => note("slept " + e.session.id),
    sessionStatus: (e) => note("status " + e.session.id + " " + e.previousStatus + ">" + e.session.status),
    agentMessage: (e) => note("message " + e.message.fromSessionId + ">" + e.message.toSessionId + " " + e.message.text),
  },
};`,
	}
}

// seen is what the plugin's handlers kept, read from its store's file.
func (r *rig) seen(id string) string {
	b, err := os.ReadFile(filepath.Join(r.data, id, "storage.json"))
	if err != nil {
		return ""
	}
	var kv map[string]string
	_ = json.Unmarshal(b, &kv)
	return kv["seen"]
}

func (r *rig) waitSeen(id, want string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.seen(id) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("%s saw %q, want %q", id, r.seen(id), want)
}

// waitSeenEnd waits for what the plugin kept to end with want.
func (r *rig) waitSeenEnd(id, want string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.HasSuffix(r.seen(id), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("%s saw %q, want it to end with %q", id, r.seen(id), want)
}

type sessionList struct {
	mu   sync.Mutex
	list []*protocol.SessionInfo
}

func (l *sessionList) get() []*protocol.SessionInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list
}

func (l *sessionList) set(r *rig, list ...*protocol.SessionInfo) {
	l.mu.Lock()
	l.list = list
	l.mu.Unlock()
	r.m.SessionsChanged(list)
}

func sess(id, state, status string) *protocol.SessionInfo {
	return &protocol.SessionInfo{Id: id, State: state, Status: status, NodeId: "n"}
}

func TestEventsReachThePluginsSubscribed(t *testing.T) {
	lim := testLimits
	lim.Idle = 100 * time.Millisecond
	sessions := &sessionList{}
	r := startRig(t, Config{Limits: lim, Sessions: sessions.get})
	r.write("all", listener("all", `["sessionStarted", "sessionEnded", "sessionSlept", "sessionStatus", "agentMessage"]`))
	r.write("some", listener("some", `["sessionStatus"]`))
	r.write("never", listener("never", `["sessionStatus"]`)) // never subscribes: its command is not run
	for _, id := range []string{"all", "some", "never"} {
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
	}
	if wanted, _ := r.m.EventsWanted(); wanted {
		t.Fatal("events are wanted before any plugin subscribed")
	}
	for _, id := range []string{"all", "some"} {
		if _, err := r.act(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: "go"}); err != nil {
			t.Fatal(err)
		}
	}
	if wanted, _ := r.m.EventsWanted(); !wanted {
		t.Fatal("events are not wanted once plugins subscribed")
	}
	// The plugins go to sleep; an event starts them again.
	r.state("all", protocol.PluginReady)
	r.state("some", protocol.PluginReady)
	sessions.set(r, sess("a", protocol.StateRunning, "idle")) // the first listing is how things stood
	sessions.set(r, sess("a", protocol.StateRunning, "working"), sess("b", protocol.StateRunning, "idle"))
	r.waitSeen("all", "status a idle>working;started b")
	r.waitSeen("some", "status a idle>working")
	sessions.set(r, sess("a", protocol.StateStopped, "idle"), sess("b", protocol.StateExited, "idle"))
	r.waitSeen("all", "status a idle>working;started b;slept a;status a working>idle;ended b")
	r.m.AgentMessage(7, "a", "c", "hello", false)
	r.waitSeen("all", "status a idle>working;started b;slept a;status a working>idle;ended b;message a>c hello")
	r.waitSeen("some", "status a idle>working;status a working>idle")
	if r.seen("never") != "" {
		t.Fatalf("a plugin not subscribed was told %q", r.seen("never"))
	}
	// What a plugin subscribed to outlives its runtime, as it slept above,
	// and the daemon: it is kept in its data folder.
	if !strings.Contains(readFile(t, filepath.Join(r.data, "some", "subscriptions.json")), "sessionStatus") {
		t.Fatal("the subscription was not kept")
	}
}

func TestEventsAreBoundedAndGated(t *testing.T) {
	sessions := &sessionList{}
	r := startRig(t, Config{Limits: testLimits, Sessions: sessions.get})
	r.write("flood", listener("flood", `["sessionStatus"]`))
	r.write("off", listener("off", `["sessionStatus"]`))
	for _, id := range []string{"flood", "off"} {
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		if _, err := r.act(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: "go"}); err != nil {
			t.Fatal(err)
		}
	}
	// A disabled plugin is told nothing, and not started for it; nor is
	// one whose permissions were revoked.
	if _, err := r.act(&protocol.PluginAction{PluginId: "off", Op: protocol.PluginDisable}); err != nil {
		t.Fatal(err)
	}
	r.state("off", protocol.PluginDisabled)
	sessions.set(r, sess("a", protocol.StateRunning, "s0"))
	for i := 1; i <= 100; i++ {
		sessions.set(r, sess("a", protocol.StateRunning, fmt.Sprint("s", i)))
	}
	// At most eventsPerSecond in a second: the rest are dropped, and the
	// log says so once.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && strings.Count(r.seen("flood"), "status") < eventsPerSecond {
		time.Sleep(10 * time.Millisecond)
	}
	if n := strings.Count(r.seen("flood"), "status"); n < 1 || n > 2*eventsPerSecond {
		t.Fatalf("the plugin was told %d events of 100 in a burst", n)
	}
	if !strings.Contains(r.logs.String(), "plugin missed events") {
		t.Fatal("the dropped events were not logged")
	}
	if r.seen("off") != "" || r.runtimeUp("off") {
		t.Fatalf("a disabled plugin was told %q", r.seen("off"))
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "flood", Op: protocol.PluginDeny}); err != nil {
		t.Fatal(err)
	}
	r.state("flood", protocol.PluginDenied)
	before := r.seen("flood")
	sessions.set(r, sess("a", protocol.StateRunning, "after"))
	r.m.AgentMessage(1, "x", "a", "hi", false)
	healthy(t, r)
	if r.seen("flood") != before || r.runtimeUp("flood") {
		t.Fatal("a refused plugin was told an event")
	}
}

func TestHasGrantZeroAllocAndEquivalence(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("permchk", map[string]string{
		ManifestName: manifest("permchk", []string{"storage", "net:api.example.com"}, ""),
		"main.js":    block(``, `ui.text("ok")`),
	})
	r.state("permchk", protocol.PluginWaiting)
	r.grant("permchk")

	p, ok := r.m.plugin("permchk")
	if !ok {
		t.Fatal("plugin permchk missing")
	}
	v := p.current()
	g := grantsOf(v.man, p.m.grant.get(p.id).Granted)

	permsToCheck := []string{
		hostapi.PermNone,
		hostapi.PermStorage,
		hostapi.PermNet,
		hostapi.PermNet + ":api.example.com",
		hostapi.PermNet + ":evil.example.com",
		hostapi.PermSessionsRead,
		hostapi.PermDbSync,
	}
	for _, perm := range permsToCheck {
		if got, want := p.hasGrant(perm), g.Has(perm); got != want {
			t.Fatalf("hasGrant(%q) = %v, grantsOf.Has = %v", perm, got, want)
		}
	}

	allocs := testing.AllocsPerRun(100, func() {
		if !p.hasGrant(hostapi.PermSessionsRead) {
			_ = p.hasGrant(hostapi.PermNet)
		}
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs for hasGrant, got %v", allocs)
	}
}
