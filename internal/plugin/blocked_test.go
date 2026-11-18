package plugin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// quietInstalls blocks what a test says, and never tells the manager: a
// plugin learns of a block only as it is entered, as it would if a
// Recheck were lost.
type quietInstalls struct {
	mu      sync.Mutex
	blocked string
}

func (q *quietInstalls) Status(string, string) InstallStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	return InstallStatus{Blocked: q.blocked}
}

func (q *quietInstalls) Settle(string) bool { return false }

func (q *quietInstalls) block(why string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.blocked = why
}

// runtimeUp says, on the plugin's actor, whether its runtime is up.
func (r *rig) runtimeUp(id string) bool {
	r.t.Helper()
	p, ok := r.m.plugin(id)
	if !ok {
		r.t.Fatalf("no plugin %s", id)
	}
	up := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.do(ctx, func() error { up = p.rt != nil; return nil }); err != nil {
		r.t.Fatal(err)
	}
	return up
}

// A plugin blocked while its runtime is up runs none of its code from
// then on, whichever way it is entered, even untold: an event, a
// command, a setting, a block watched, its folder read again. Its
// runtime is closed at the first.
func TestABlockedPluginRunsNothingMore(t *testing.T) {
	inst := &quietInstalls{}
	r := startRig(t, Config{Limits: testLimits, Installs: inst})
	r.write("counter", map[string]string{
		ManifestName: manifest("counter", []string{"storage"}, `
[contributes.settings]
label = { type = "string", label = "Label", default = "the" }
`),
		"main.js": counter,
	})
	r.state("counter", protocol.PluginWaiting)
	r.grant("counter")
	w := r.watch("plugin:counter/ui")
	w.until("count 0", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "the count 0") })
	if _, err := r.event("plugin:counter/ui", "/add", protocol.UiTap, ""); err != nil {
		t.Fatal(err)
	}
	w.until("count 1", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "the count 1") })

	inst.block("blocked by a test: it steals")
	if _, err := r.event("plugin:counter/ui", "/add", protocol.UiTap, ""); err == nil || !strings.Contains(err.Error(), "it steals") {
		t.Fatalf("an event of a blocked plugin: %v", err)
	}
	if r.runtimeUp("counter") {
		t.Fatal("the blocked plugin's runtime is up")
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginRun, Command: "go"}); err == nil || !strings.Contains(err.Error(), "it steals") {
		t.Fatalf("a command of a blocked plugin: %v", err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginSet, Key: "label", Value: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginEnable}); err == nil {
		t.Fatal("a blocked plugin was enabled")
	}
	w2 := r.watch("plugin:counter/ui")
	w2.until("that it is blocked", func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool { return u.GetState() == protocol.PluginBlocked })
	r.write("counter", map[string]string{"main.js": counter + "\n// changed"})
	r.info("counter", func(p *protocol.PluginInfo) bool { return p.GetState() == protocol.PluginBlocked && !p.GetStale() })
	if r.runtimeUp("counter") {
		t.Fatal("the blocked plugin's runtime is up")
	}
	if n := strings.Count(r.logs.String(), "plugin started"); n != 1 {
		t.Fatalf("the plugin started %d times; once, before it was blocked", n)
	}

	// Unblocked, and told, it runs again.
	inst.block("")
	r.m.Recheck()
	w.until("the label set meanwhile", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetState() == protocol.PluginRunning && hasText(n, "a count 1")
	})
}

// A block of a plugin the node does not have is refused with a code a
// front-end tells from a failure that passes: asking again will not
// change it.
func TestABlockOfNoPluginIsCoded(t *testing.T) {
	r := newRig(t, testLimits, nil)
	var coded *protocol.CodedError
	err := r.m.Watch(context.Background(), "plugin:absent/ui", func(*protocol.BlockUpdate) error { return nil })
	if !errors.As(err, &coded) || coded.Code != protocol.CodeNoPlugin {
		t.Fatalf("watching a plugin the node lacks: %v", err)
	}
	_, err = r.event("plugin:absent/ui", "/x", protocol.UiTap, "")
	if !errors.As(err, &coded) || coded.Code != protocol.CodeNoPlugin {
		t.Fatalf("an event of a plugin the node lacks: %v", err)
	}
}
