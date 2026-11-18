package store

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// A registry's blocklist applies to every plugin of the node, including
// one another registry gave: the plugin stops, says why, cannot be
// enabled or updated, and runs again once the registry is removed.
func TestBlocklistsApplyAcrossRegistries(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v1 := r.publish("1.makoplugin", build(t, files("victim", "1.0.0"), a))
	r.index("home", &registry.Index{Plugins: []*registry.Entry{entry("victim", a, v1)}})
	r.index("other", &registry.Index{})
	r.addRegistry("home")
	r.addRegistry("other")
	if _, err := r.install(protocol.InstallOp, "victim", "home"); err != nil {
		t.Fatal(err)
	}
	w := r.watch("plugin:victim/ui")
	w.until(t, "v1.0.0", func(_ *protocol.BlockUpdate, n *protocol.UiNode) bool { return text(n) == "v1.0.0" })

	r.index("other", &registry.Index{Blocklist: []*registry.Block{{ID: "victim", Reason: "it sends your tokens away"}}})
	r.s.Refresh(context.Background())
	info := r.state("victim", protocol.PluginBlocked)
	if !strings.Contains(info.GetBlocked(), "it sends your tokens away") || !strings.Contains(info.GetBlocked(), r.registryURL("other")) {
		t.Fatalf("blocked as %q", info.GetBlocked())
	}
	w.until(t, "that it is blocked", func(u *protocol.BlockUpdate, _ *protocol.UiNode) bool {
		return u.GetState() == protocol.PluginBlocked && strings.Contains(u.GetError(), "tokens")
	})
	if err := r.act("victim", protocol.PluginEnable); err == nil || !strings.Contains(err.Error(), "while it is blocked") {
		t.Fatalf("enabling a blocked plugin: %v", err)
	}
	if err := r.act("victim", protocol.PluginGrant); err != nil {
		t.Fatal(err)
	}
	r.state("victim", protocol.PluginBlocked)
	v2 := r.publish("2.makoplugin", build(t, files("victim", "2.0.0"), a))
	r.index("home", &registry.Index{Plugins: []*registry.Entry{entry("victim", a, v1, v2)}})
	r.refused(protocol.UpdateOp, "victim", "", "blocked by the registry")
	// A plugin that reads again, as a folder changes, stays stopped.
	if err := r.act("victim", protocol.PluginReload); err != nil {
		t.Fatal(err)
	}
	r.state("victim", protocol.PluginBlocked)

	if err := r.s.RemoveRegistry(r.registryURL("other")); err != nil {
		t.Fatal(err)
	}
	r.state("victim", protocol.PluginRunning)
	w.until(t, "it running again", func(u *protocol.BlockUpdate, n *protocol.UiNode) bool {
		return u.GetState() == protocol.PluginRunning && text(n) == "v1.0.0"
	})
}

// An entry blocks by ID, by ID and version, or by publisher key; the
// first of the node's registries to block one says why.
func TestBlocklistEntries(t *testing.T) {
	r := newRig(t, "")
	a, b := newKey(t), newKey(t)
	v1 := r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a))
	v2 := r.publish("2.makoplugin", build(t, files("p", "2.0.0"), a))
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	r.state("p", protocol.PluginReady)
	cases := []struct {
		block   registry.Block
		blocked bool
	}{
		{registry.Block{ID: "p", Version: "1.0.0", Reason: "bad build"}, true},
		{registry.Block{ID: "p", Version: "2.0.0", Reason: "bad build"}, false},
		{registry.Block{Key: pack.PublicKey(a), Reason: "key stolen"}, true},
		{registry.Block{Key: pack.PublicKey(b), Reason: "key stolen"}, false},
		{registry.Block{ID: "q", Reason: "other"}, false},
	}
	for _, c := range cases {
		blk := c.block
		r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1, v2)}, Blocklist: []*registry.Block{&blk}})
		r.s.Refresh(context.Background())
		want := protocol.PluginReady
		if c.blocked {
			want = protocol.PluginBlocked
		}
		info := r.state("p", want)
		if c.blocked && !strings.Contains(info.GetBlocked(), blk.Reason) {
			t.Fatalf("%+v: blocked as %q", blk, info.GetBlocked())
		}
		// A version blocked is not offered as an update.
		if blk.Version == "2.0.0" && info.GetUpdate() != "" {
			t.Fatalf("a blocked version is offered: %v", info)
		}
	}
	// A blocked listing is not installed.
	if err := r.s.Remove("p"); err != nil {
		t.Fatal(err)
	}
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}, Blocklist: []*registry.Block{{ID: "p", Reason: "no"}}})
	r.s.Refresh(context.Background())
	found := r.s.Search(context.Background(), "", "p")
	if len(found) != 1 || found[0].GetBlocked() == "" {
		t.Fatalf("listed as %v", found)
	}
	r.refused(protocol.InstallOp, "p", "", "blocked by the registry")
}

// A registry out of reach keeps the list it gave last; a registry
// whose index stops reading keeps it too, the error beside it.
func TestBlocklistsOutlastOutages(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a)))}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	r.index("reg", &registry.Index{Blocklist: []*registry.Block{{ID: "p", Reason: "bad"}}})
	r.s.Refresh(context.Background())
	r.state("p", protocol.PluginBlocked)
	r.serve("/reg/"+registry.IndexName, []byte(`{"format": "mako-registry/1", "plugins": [`))
	r.s.Refresh(context.Background())
	r.state("p", protocol.PluginBlocked)
	if regs := r.s.Registries(); len(regs) != 1 || !strings.Contains(regs[0].GetError(), "does not read") || regs[0].GetBlocked() != 1 {
		t.Fatalf("registries %v", regs)
	}
	// A daemon started again reads the list it kept.
	again := New(Config{DataDir: r.data, Loopback: true})
	if st := again.Status("p", "1.0.0"); !strings.Contains(st.Blocked, "bad") {
		t.Fatalf("after a restart: %v", st)
	}
}

// A blocklist read by whatever reads a registry, here a search as the
// store opens, stops a plugin it blocks at once: no event, command or
// timer is needed for its runtime to close.
func TestABlocklistLearntBySearchStopsThePlugin(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	f := files("victim", "1.0.0")
	f["main.js"] = []byte(`export default { activate(m) { m.blocks.register("ui", { state: { n: 0 },
  render: (s, ui) => ui.column([ui.text("n=" + s.n, { key: "count" }), ui.button("Add", { key: "add", onTap: ["add"] })]),
  on: { add(s) { s.n++; } } }) } }`)
	v1 := r.publish("1.makoplugin", build(t, f, a))
	r.index("home", &registry.Index{Plugins: []*registry.Entry{entry("victim", a, v1)}})
	r.addRegistry("home")
	// "other" is out of reach when added, so that a search reads it again.
	r.hook("/other/"+registry.IndexName, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
	r.addRegistry("other")
	if _, err := r.install(protocol.InstallOp, "victim", "home"); err != nil {
		t.Fatal(err)
	}
	w := r.watch("plugin:victim/ui")
	w.until(t, "n=0", func(_ *protocol.BlockUpdate, n *protocol.UiNode) bool { return text(n) == "n=0Add" })
	r.state("victim", protocol.PluginRunning)

	r.hook("/other/"+registry.IndexName, nil)
	r.index("other", &registry.Index{Blocklist: []*registry.Block{{ID: "victim", Reason: "steals tokens"}}})
	r.s.Search(context.Background(), "", "")
	r.state("victim", protocol.PluginBlocked)
	r.logged(t, "plugin stopped: blocked")
	if _, err := r.m.Event(context.Background(), &protocol.BlockEvent{Block: "plugin:victim/ui", Node: "/add", Kind: protocol.UiTap}); err == nil ||
		!strings.Contains(err.Error(), "steals tokens") {
		t.Fatalf("an event of the blocked plugin: %v", err)
	}
}
