package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// These tests try to break out of the sandbox or past a limit, as a
// plugin written to would. Each must be refused, the plugin stopped where
// it went over a limit, and the manager left working for the others.

// block is a plugin whose block "ui" loads and renders as given.
func block(load, render string) string {
	return `let mako; export default { activate(m) { mako = m;
		m.blocks.register("ui", { state: { msg: "" },
			async load(s) { try { ` + load + ` } catch (e) { s.msg = "error: " + e.message } },
			render: (s, ui) => ` + render + ` }) } }`
}

// rawBlock is block without the load's errors caught.
func rawBlock(load, render string) string {
	return `let mako; export default { activate(m) { mako = m;
		m.blocks.register("ui", { state: { msg: "" }, async load(s) { ` + load + ` },
			render: (s, ui) => ` + render + ` }) } }`
}

// healthy checks the manager still runs a plugin, a well one written now.
func healthy(t *testing.T, r *rig) {
	t.Helper()
	id := fmt.Sprintf("well%d", len(r.m.List()))
	r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": block(`s.msg = "well"`, `ui.text(s.msg)`)})
	w := r.watch("plugin:" + id + "/ui")
	w.until("a well plugin drawn", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "well") })
}

func TestCallsWithoutPermissionAreRefused(t *testing.T) {
	r := newRig(t, testLimits, nil)
	cases := map[string]string{
		"storage":       `await mako.storage.setItem({ key: "k", value: "v" })`,
		"sessions:read": `await mako.sessions.listSessions()`,
		"net":           `await mako.http.fetch({ url: "https://api.github.com/" })`,
		"exec":          `await mako.exec.runProcess({ argv: ["sh"] })`,
		"secrets":       `await mako.secrets.getSecret({ name: "x" })`,
	}
	for perm, call := range cases {
		id := "p" + strings.NewReplacer(":", "").Replace(perm)
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": block(call+`; s.msg = "got through"`, `ui.text(s.msg)`)})
		w := r.watch("plugin:" + id + "/ui")
		tree := w.until("the refusal", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error:") })
		if !hasText(tree, "needs") || hasText(tree, "got through") {
			t.Errorf("%s: %v", perm, texts(tree))
		}
	}
	// A permission granted for a method this version does not carry out
	// is not available.
	r.write("fs", map[string]string{ManifestName: manifest("fs", []string{"fs:project"}, ""),
		"main.js": block(`await mako.files.readFile({ path: "/etc/passwd" }); s.msg = "got through"`, `ui.text(s.msg)`)})
	r.state("fs", protocol.PluginWaiting)
	r.grant("fs")
	w := r.watch("plugin:fs/ui")
	w.until("not available", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "not available") })
	// The bridge itself is out of reach once the prelude ran.
	r.write("raw", map[string]string{ManifestName: manifest("raw", nil, ""),
		"main.js": block(`s.msg = typeof globalThis.__makoCall + " " + typeof setTimeout`, `ui.text(s.msg)`)})
	w = r.watch("plugin:raw/ui")
	w.until("no bridge", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "undefined undefined") })
}

func TestRunawayPluginsAreStopped(t *testing.T) {
	r := newRig(t, testLimits, nil)
	cases := map[string]struct{ load, render, want string }{
		"loop":         {`for (;;) {}`, `ui.text("x")`, "ran longer"},
		"render-loop":  {``, `(() => { for (;;) {} })()`, "ran longer"},
		"busy-host":    {`for (;;) { await mako.log.write({ message: "x" }) }`, `ui.text("x")`, "ran longer"},
		"memory":       {`const a = []; for (;;) a.push("x".repeat(1 << 20))`, `ui.text("x")`, "more memory"},
		"memory-catch": {`const a = []; for (;;) { try { a.push("x".repeat(1 << 20)) } catch (e) {} }`, `ui.text("x")`, ""},
		"recursion":    {`const f = (n) => f(n + 1) + 1; f(0)`, `ui.text("x")`, ""},
	}
	for id, c := range cases {
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": rawBlock(c.load, c.render)})
		r.state(id, protocol.PluginReady)
		w := r.watch("plugin:" + id + "/ui")
		u := w.until("it stopped", func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool { return u.GetState() == protocol.PluginFailed })
		_ = u
		info := r.state(id, protocol.PluginFailed)
		if !strings.Contains(info.GetError(), c.want) {
			t.Errorf("%s stopped with %q; want %q", id, info.GetError(), c.want)
		}
	}
	healthy(t, r)
}

func TestTreesOutOfBoundsAreRefused(t *testing.T) {
	r := newRig(t, testLimits, nil)
	cases := map[string]struct{ render, want string }{
		"huge":       {`ui.column(Array.from({ length: 5000 }, (_, i) => ui.text("n" + i)))`, "more than 2000 nodes"},
		"deep":       {`Array.from({ length: 60 }).reduce((n) => ui.column([n]), ui.text("x"))`, "deeper than"},
		"raw colour": {`({ kind: "text", text: "x", color: "#ff0000" })`, "unknown field"},
		"raw tone":   {`ui.text("x", { tone: "#ff0000" })`, "not a colour token"},
		"kind":       {`({ kind: "iframe", text: "https://evil.example" })`, "not a kind of node"},
		"spoof":      {`ui.text("Allow‮evil")`, "reordering"},
		"keys":       {`ui.column([ui.text("a", { key: "k" }), ui.text("b", { key: "k" })])`, "two children have the key"},
		"slash":      {`ui.column([ui.text("a", { key: "../x" })])`, "holds /"},
		"tap input":  {`ui.input("x", { onTap: ["go"] })`, "takes onChange"},
		"text kids":  {`({ kind: "text", text: "x", children: [{ kind: "text" }] })`, "holds no children"},
		"long text":  {`ui.text("x".repeat(100000))`, "over 8 KiB"},
	}
	for id, c := range cases {
		id = strings.ReplaceAll(id, " ", "-")
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": block(``, c.render)})
		w := r.watch("plugin:" + id + "/ui")
		w.until(c.want, func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
			return n == nil && strings.Contains(u.GetError(), c.want)
		})
	}
	healthy(t, r)
}

func TestPathTricksAreRefused(t *testing.T) {
	r := newRig(t, testLimits, nil)
	outside := filepath.Join(filepath.Dir(r.dir), "outside.js")
	if err := os.WriteFile(outside, []byte(`export const secret = "outside";`), 0o644); err != nil {
		t.Fatal(err)
	}
	good := block(`s.msg = "x"`, `ui.text(s.msg)`)
	for id, entry := range map[string]string{"dots": "../outside.js", "abs": "/etc/x.js", "unclean": "lib/../main.js", "backslash": `lib\\main.js`} {
		r.write(id, map[string]string{ManifestName: strings.Replace(manifest(id, nil, ""), `entry = "main.js"`, `entry = "`+entry+`"`, 1), "main.js": good})
		if info := r.state(id, protocol.PluginFailed); !strings.Contains(info.GetError(), "entry") {
			t.Errorf("%s: %v", id, info.GetError())
		}
	}
	// A folder whose manifest names another ID.
	r.write("named", map[string]string{ManifestName: manifest("other", nil, ""), "main.js": good})
	if info := r.state("named", protocol.PluginFailed); !strings.Contains(info.GetError(), "folder's name") {
		t.Errorf("named: %v", info.GetError())
	}
	// Symbolic links: a plugin's folder, or a file in it.
	target := filepath.Join(filepath.Dir(r.dir), "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ManifestName), []byte(manifest("linked", nil, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "main.js"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(r.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if info := r.state("linked", protocol.PluginFailed); !strings.Contains(info.GetError(), "symbolic link") {
		t.Errorf("linked: %v", info.GetError())
	}
	r.write("filelink", map[string]string{ManifestName: manifest("filelink", nil, "")})
	if err := os.Symlink(outside, filepath.Join(r.dir, "filelink", "main.js")); err != nil {
		t.Fatal(err)
	}
	if info := r.state("filelink", protocol.PluginFailed); !strings.Contains(info.GetError(), "symbolic link") {
		t.Errorf("filelink: %v", info.GetError())
	}
	// Imports reach the plugin's own modules alone; the runtime has no
	// files.
	for id, imp := range map[string]string{
		"importup":  `import { secret } from "../outside.js";`,
		"importabs": `import { secret } from "` + outside + `";`,
	} {
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": imp + "\n" + block(`s.msg = secret`, `ui.text(s.msg)`)})
		info := r.state(id, protocol.PluginFailed)
		if strings.Contains(info.GetError(), "outside") && !strings.Contains(info.GetError(), "outside.js") {
			t.Errorf("%s read the file outside: %v", id, info.GetError())
		}
	}
	r.write("stdfile", map[string]string{ManifestName: manifest("stdfile", nil, ""), "main.js": `import * as std from "qjs:std";
` + block(`s.msg = "read " + String(std.loadFile("`+outside+`"))`, `ui.text(s.msg)`)})
	info := r.info("stdfile", func(p *protocol.PluginInfo) bool {
		return p.GetState() == protocol.PluginFailed || p.GetState() == protocol.PluginReady
	})
	if info.GetState() == protocol.PluginReady {
		w := r.watch("plugin:stdfile/ui")
		tree := w.until("its read", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
			return hasText(n, "read") || hasText(n, "error")
		})
		if hasText(tree, "outside") {
			t.Fatalf("qjs:std read a file: %v", texts(tree))
		}
	}
	healthy(t, r)
}

func TestNamespacesAreThePluginsOwn(t *testing.T) {
	r := newRig(t, testLimits, nil)
	for id, name := range map[string]string{"sys": "mako.sidebar", "other": "plugin:victim/ui", "up": "../victim/ui"} {
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": `export default { activate(m) {
			m.blocks.register("` + name + `", { state: {}, render: (s, ui) => ui.text("mine") }) } }`})
		r.watch("plugin:" + id + "/ui")
		if info := r.state(id, protocol.PluginFailed); !strings.Contains(info.GetError(), "not declared") {
			t.Errorf("%s registered %s: %v", id, name, info.GetError())
		}
	}
	// A manifest cannot declare such a block either.
	for _, decl := range []string{"mako.sidebar", "../victim", "victim/ui", "UI"} {
		if _, err := ParseManifest([]byte(strings.Replace(manifest("x", nil, ""), `id = "ui"`, `id = "`+decl+`"`, 1))); err == nil {
			t.Errorf("a manifest declared block %q", decl)
		}
	}
	for _, name := range []string{"plugin:victim/ui", "plugin:../x/ui", "mako.sidebar", "plugin:sys/../victim"} {
		if err := r.m.Watch(t.Context(), name, func(*protocol.BlockUpdate) error { return nil }); err == nil {
			t.Errorf("watched %s", name)
		}
	}
}

func TestStorageQuota(t *testing.T) {
	lim := testLimits
	lim.Storage = 1000
	r := newRig(t, lim, nil)
	r.write("st", map[string]string{ManifestName: manifest("st", []string{"storage"}, ""), "main.js": block(`
		await mako.storage.setItem({ key: "a", value: "x".repeat(900) });
		s.msg = "first ";
		await mako.storage.setItem({ key: "b", value: "x".repeat(200) });
		s.msg += "second";`, `ui.text(s.msg)`)})
	r.state("st", protocol.PluginWaiting)
	r.grant("st")
	w := r.watch("plugin:st/ui")
	tree := w.until("the refusal", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error") })
	if !hasText(tree, "over its quota") || hasText(tree, "second") {
		t.Fatalf("%v", texts(tree))
	}
	// A key too long is refused before the quota is looked at.
	if _, err := newKVStore(filepath.Join(t.TempDir(), "s.json"), 1<<20).set(strings.Repeat("k", 300), "v"); err == nil {
		t.Fatal("a long key was taken")
	}
}

func TestFloodingIsRefused(t *testing.T) {
	lim := testLimits
	lim.Updates = 100 * 1e6 // 100ms
	r := newRig(t, lim, nil)
	r.write("flood", map[string]string{ManifestName: manifest("flood", nil, ""), "main.js": counter})
	w := r.watch("plugin:flood/ui")
	w.until("the count", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "count 0") })
	var refused, taken atomic.Int32
	var wg sync.WaitGroup
	for range 300 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.event("plugin:flood/ui", "/on", protocol.UiChange, "true"); err != nil {
				refused.Add(1)
			} else {
				taken.Add(1)
			}
		}()
	}
	wg.Wait()
	if refused.Load() == 0 {
		t.Fatalf("300 events at once were all taken")
	}
	w.mu.Lock()
	updates := w.updates
	w.mu.Unlock()
	if updates > int(taken.Load())+5 {
		t.Fatalf("%d updates for %d events taken", updates, taken.Load())
	}
	if _, err := r.event("plugin:flood/ui", "/on", protocol.UiChange, strings.Repeat("x", 10000)); err == nil {
		t.Fatal("an event's huge value was taken")
	}
	r.state("flood", protocol.PluginRunning)
	healthy(t, r)
}

func TestRepeatedFailuresStopThePlugin(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("bad", map[string]string{ManifestName: manifest("bad", nil, ""), "main.js": `export default { activate(m) {
		m.blocks.register("ui", { state: {}, render: (s, ui) => ui.column([ui.button("Go", { key: "go", onTap: ["go"] })]),
			on: { go() { throw new Error("nope") } } }) } }`})
	w := r.watch("plugin:bad/ui")
	w.until("the button", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "Go") })
	for range 3 {
		_, err := r.event("plugin:bad/ui", "/go", protocol.UiTap, "")
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("the failing handler answered %v", err)
		}
	}
	info := r.state("bad", protocol.PluginFailed)
	if !strings.Contains(info.GetError(), "3 times") {
		t.Fatalf("stopped with %q", info.GetError())
	}
	// Enabled again, it runs.
	if _, err := r.act(&protocol.PluginAction{PluginId: "bad", Op: protocol.PluginEnable}); err != nil {
		t.Fatal(err)
	}
	r.state("bad", protocol.PluginRunning)
}

// A folder whose name is no plugin ID is passed over, not read.
func TestFoldersNamedOtherwiseArePassedOver(t *testing.T) {
	r := newRig(t, testLimits, nil)
	for _, name := range []string{"Evil_Plugin", "-x", ".hidden", "a..b"} {
		r.write(name, map[string]string{ManifestName: manifest("x", nil, ""), "main.js": block(``, `ui.text("x")`)})
	}
	healthy(t, r)
	for _, p := range r.m.List() {
		if !strings.HasPrefix(p.GetId(), "well") {
			t.Errorf("%s was read", p.GetId())
		}
	}
}
