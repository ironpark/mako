package plugin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// fakeLayout stands for the daemon's layout: a patch is checked at once,
// and a change waits for answer, which a test sends.
type fakeLayout struct {
	mu      sync.Mutex
	changes []string
	answer  chan error
	forgot  []string
}

func (f *fakeLayout) Prepare(document, ops, ifRev string) (LayoutChange, error) {
	if !strings.HasPrefix(ops, "[") {
		return LayoutChange{}, errors.New("ops is a JSON array")
	}
	return LayoutChange{Document: "default", Doc: []byte(ops), Summary: "resize sidebar"}, nil
}

func (f *fakeLayout) Allowed(plugin, document string) bool { return false }

func (f *fakeLayout) Change(ctx context.Context, plugin string, c LayoutChange) (string, error) {
	f.mu.Lock()
	f.changes = append(f.changes, plugin+":"+string(c.Doc))
	f.mu.Unlock()
	select {
	case err := <-f.answer:
		if err != nil {
			return "", err
		}
		return "7.0.n", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeLayout) Forget(plugin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, plugin)
}

const layoutPlugin = `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("x") });
    m.commands.register("go", async () => {
      const r = await mako.layout.patchLayout({ ops: JSON.stringify([{ op: "resize", id: "sidebar", size: 300 }]) });
      await note("asked " + r.asked);
    });
  },
  on: {
    layoutChanged: (e) => note("changed " + e.document + " " + e.revision),
    layoutRefused: (e) => note("refused " + e.error),
  },
};`

func TestAPluginsLayoutChangeComesBackAsAnEvent(t *testing.T) {
	lay := &fakeLayout{answer: make(chan error, 1)}
	r := startRig(t, Config{Limits: testLimits, Layout: lay})
	r.write("lay", map[string]string{ManifestName: manifest("lay", []string{"layout:write", "storage"}, ""), "main.js": layoutPlugin})
	r.state("lay", protocol.PluginWaiting)
	r.grant("lay")
	if err := r.run("lay"); err != nil {
		t.Fatal(err)
	}
	// One waits at a time.
	if err := r.run("lay"); err == nil || !strings.Contains(err.Error(), "waits for the user") {
		t.Fatalf("a second change: %v", err)
	}
	lay.answer <- nil
	r.waitSeen("lay", "asked true;changed default 7.0.n")
	if err := r.run("lay"); err != nil {
		t.Fatal(err)
	}
	lay.answer <- errors.New("the user declined the change of the window layout")
	r.waitSeen("lay", "asked true;changed default 7.0.n;asked true;refused the user declined the change of the window layout")
	// Revoked or removed, what the user allowed it goes.
	if _, err := r.act(&protocol.PluginAction{PluginId: "lay", Op: protocol.PluginDeny}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Remove("lay"); err != nil {
		t.Fatal(err)
	}
	lay.mu.Lock()
	forgot := strings.Join(lay.forgot, ",")
	lay.mu.Unlock()
	if forgot != "lay,lay" {
		t.Fatalf("forgot %q", forgot)
	}
}

func TestPluginLayoutChangesOutOfBoundsAreRefused(t *testing.T) {
	lay := &fakeLayout{answer: make(chan error, 100)}
	for range 100 {
		lay.answer <- nil
	}
	r := startRig(t, Config{Limits: testLimits, Layout: lay})
	cases := map[string]struct{ load, want string }{
		"ops":   {`await mako.layout.patchLayout({ ops: "{}" })`, "JSON array"},
		"big":   {`await mako.layout.patchLayout({ ops: "[" + " ".repeat(70000) + "]" })`, "64 KiB"},
		"flood": {`for (let i = 0; i < 20; i++) await mako.layout.patchLayout({ ops: "[]" }).catch((e) => { if (!/waits for the user/.test(e.message)) throw e })`, "times a minute"},
	}
	for id, c := range cases {
		r.write(id, map[string]string{ManifestName: manifest(id, []string{"layout:write"}, ""), "main.js": block(c.load+`; s.msg = "got through"`, `ui.text(s.msg)`)})
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		tree := r.watch("plugin:"+id+"/ui").until(c.want, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error:") })
		if !hasText(tree, c.want) {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	healthy(t, r)
}
