package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		"storage":          `await mako.storage.setItem({ key: "k", value: "v" })`,
		"sessions:read":    `await mako.sessions.listSessions()`,
		"net":              `await mako.http.fetch({ url: "https://api.github.com/" })`,
		"exec":             `await mako.exec.runProcess({ argv: ["sh"] })`,
		"secrets":          `await mako.secrets.getSecret({ name: "x" })`,
		"secrets-set":      `await mako.secrets.setSecret({ name: "x", value: "v" })`,
		"fs:project":       `await mako.files.listProjects({})`,
		"fs:project:write": `await mako.files.writeFile({ project: "p", root: "/", path: "x", text: "x" })`,
		"transcript":       `await mako.sessions.readTranscript({ sessionId: "s1" })`,
		"subscribe":        `await mako.sessions.subscribe({ events: ["sessionStarted"] })`,
		"sessions:write":   `await mako.messages.sendMessage({ sessionId: "s1", text: "hi" })`,
		"start":            `await mako.messages.startAgent({ agent: "claude", project: "p", root: "/", prompt: "x" })`,
		"layout:write":     `await mako.layout.patchLayout({ ops: "[]" })`,
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
	// A permission granted does not reach past what its method takes: a
	// file outside every project.
	r.write("fs", map[string]string{ManifestName: manifest("fs", []string{"fs:project"}, ""),
		"main.js": block(`await mako.files.readFile({ path: "/etc/passwd" }); s.msg = "got through"`, `ui.text(s.msg)`)})
	r.state("fs", protocol.PluginWaiting)
	r.grant("fs")
	w := r.watch("plugin:fs/ui")
	w.until("refused", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "not a path relative") })
	// The bridge itself is out of reach once the prelude ran.
	r.write("raw", map[string]string{ManifestName: manifest("raw", nil, ""),
		"main.js": block(`s.msg = typeof globalThis.__makoCall + " " + typeof setTimeout`, `ui.text(s.msg)`)})
	w = r.watch("plugin:raw/ui")
	w.until("no bridge", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "undefined undefined") })
}

func TestRunawayPluginsAreStopped(t *testing.T) {
	type runaway struct{ load, render, want string }
	run := func(r *rig, cases map[string]runaway) {
		for id, c := range cases {
			r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": rawBlock(c.load, c.render)})
			r.state(id, protocol.PluginReady)
			r.watch("plugin:" + id + "/ui")
			r.fails(id, c.want)
		}
	}
	// Going over the time a call may take.
	r := newRig(t, runawayLimits, nil)
	run(r, map[string]runaway{
		"loop":        {`for (;;) {}`, `ui.text("x")`, "ran longer"},
		"render-loop": {``, `(() => { for (;;) {} })()`, "ran longer"},
		"busy-host":   {`for (;;) { await mako.log.write({ message: "x" }) }`, `ui.text("x")`, "ran longer"},
	})
	healthy(t, r)
	// Going over the memory or the stack, with time enough that a slow
	// machine cannot stop them for their time first.
	r = newRig(t, testLimits, nil)
	run(r, map[string]runaway{
		"memory":    {`const a = []; for (;;) a.push("x".repeat(1 << 20))`, `ui.text("x")`, "more memory"},
		"recursion": {`const f = (n) => f(n + 1) + 1; f(0)`, `ui.text("x")`, "nested too deep"},
	})
	healthy(t, r)
	// A bomb that catches its own out-of-memory errors stays within the
	// heap, and loops until its time is up.
	r = newRig(t, runawayLimits, nil)
	run(r, map[string]runaway{
		"memory-catch": {`const a = []; for (;;) { try { a.push("x".repeat(1 << 20)) } catch (e) {} }`, `ui.text("x")`, "ran longer"},
	})
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
		"chart nan":  {`ui.chart("line", [{ values: [1, "x"] }])`, "finite"},
		"chart kind": {`ui.chart("pie", [{ values: [1] }])`, "not one of"},
		"chart big":  {`ui.chart("line", [{ values: Array.from({ length: 300 }, (_, i) => i) }])`, "1 to 200 values"},
		"labels":     {`ui.text("x", { labels: ["a"] })`, "only a chart"},
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
		r.fails(id, "entry")
	}
	// A folder whose manifest names another ID.
	r.write("named", map[string]string{ManifestName: manifest("other", nil, ""), "main.js": good})
	r.fails("named", "folder's name")
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
	r.fails("linked", "symbolic link")
	r.write("filelink", map[string]string{ManifestName: manifest("filelink", nil, "")})
	if err := os.Symlink(outside, filepath.Join(r.dir, "filelink", "main.js")); err != nil {
		t.Fatal(err)
	}
	r.fails("filelink", "symbolic link")
	// Imports reach the plugin's own modules alone; the runtime has no
	// files.
	for id, imp := range map[string]string{
		"importup":  `import { secret } from "../outside.js";`,
		"importabs": `import { secret } from "` + outside + `";`,
	} {
		r.write(id, map[string]string{ManifestName: manifest(id, nil, ""), "main.js": imp + "\n" + block(`s.msg = secret`, `ui.text(s.msg)`)})
		// The import of a module the plugin does not have fails its check.
		info := r.fails(id, "outside.js")
		if strings.Contains(info.GetError(), "\"outside\"") {
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
		r.fails(id, "not declared")
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
	// The events' clock stops, so that however slowly the machine takes
	// them, they come within the second the limit counts.
	p, _ := r.m.plugin("flood")
	p.evRate.mu.Lock()
	at := time.Now()
	p.evRate.now = func() time.Time { return at }
	p.evRate.mu.Unlock()
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
	if taken.Load() != int32(testLimits.withDefaults().Events) {
		t.Fatalf("%d of 300 events in a second were taken; want %d", taken.Load(), testLimits.withDefaults().Events)
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
	r.fails("bad", "3 times")
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
