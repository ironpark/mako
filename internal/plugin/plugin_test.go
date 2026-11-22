package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// counter is a plugin of a block that counts taps, and a command that
// adds ten, keeping the count in its store.
const counter = `
let mako;
export default {
  activate(m) {
    mako = m;
    mako.blocks.register("ui", {
      state: { n: 0, label: "" },
      async load(s) {
        const kept = await mako.storage.getItem({ key: "n" });
        if (kept.found) s.n = Number(kept.value);
        const { setting } = await mako.settings.getSetting({ key: "label" });
        s.label = setting.text;
      },
      render: (s, ui) => ui.column([
        ui.text(s.label + " count " + s.n, { key: "count" }),
        ui.button("Add", { key: "add", onTap: ["add", 1] }),
        ui.toggle(s.on, "On", { key: "on", onChange: ["set"] }),
      ]),
      on: {
        async add(s, by) { s.n += by; await mako.storage.setItem({ key: "n", value: String(s.n) }); },
        set(s, on) { s.on = on; },
      },
    });
    mako.commands.register("go", () => { mako.blocks.state("ui").n += 10; });
  },
};`

func TestPluginRunsAfterItsPermissionsAreAllowed(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("counter", map[string]string{
		ManifestName: manifest("counter", []string{"storage"}, `
[contributes.settings]
label = { type = "string", label = "Label", default = "the" }
secret = { type = "secret", label = "Secret" }
`),
		"main.js": counter,
	})
	info := r.state("counter", protocol.PluginWaiting)
	if strings.Join(info.GetAsked(), ",") != "storage" {
		t.Fatalf("asked %v; want storage", info.GetAsked())
	}
	w := r.watch("plugin:counter/ui")
	w.until("that it waits", func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetState() == protocol.PluginWaiting && u.GetPlugin() == "counter" && u.GetTitle() == "Test"
	})
	r.grant("counter")
	tree := w.until("the count", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "the count 0") })
	add := find(tree, "Add")
	if add == nil || !add.GetTap() || add.GetId() != "/add" {
		t.Fatalf("the button is %v", add)
	}
	if _, err := r.event("plugin:counter/ui", "/add", protocol.UiTap, ""); err != nil {
		t.Fatal(err)
	}
	w.until("the count after a tap", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "the count 1") })
	if _, err := r.event("plugin:counter/ui", "/on", protocol.UiChange, "true"); err != nil {
		t.Fatal(err)
	}
	w.until("the toggle on", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return len(n.GetChildren()) == 3 && n.GetChildren()[2].GetChecked()
	})
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginRun, Command: "go"}); err != nil {
		t.Fatal(err)
	}
	w.until("the count after the command", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "the count 11") })
	// A setting set reloads the block with it; a secret is not shown.
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginSet, Key: "label", Value: "my"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "counter", Op: protocol.PluginSet, Key: "secret", Value: "hunter2"}); err != nil {
		t.Fatal(err)
	}
	w.until("the label", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "my count") })
	for _, s := range r.state("counter", protocol.PluginRunning).GetSettings() {
		if s.GetKey() == "secret" && (s.GetValue() != "" || !s.GetSet()) {
			t.Fatalf("the secret is listed as %v", s)
		}
	}
	// A node that sends nothing for an event, and one not in the tree,
	// are refused.
	if _, err := r.event("plugin:counter/ui", "/count", protocol.UiTap, ""); err == nil {
		t.Fatal("a tap on text that sends nothing was taken")
	}
	if _, err := r.event("plugin:counter/ui", "/nothing", protocol.UiTap, ""); err == nil {
		t.Fatal("a tap on no node was taken")
	}
}

func TestIdlePluginSleepsAndKeepsItsState(t *testing.T) {
	lim := testLimits
	lim.Idle = 200 * time.Millisecond
	r := newRig(t, lim, nil)
	r.write("counter", map[string]string{ManifestName: manifest("counter", []string{"storage"}, `
[contributes.settings]
label = { type = "string", label = "Label" }
`), "main.js": counter})
	r.state("counter", protocol.PluginWaiting)
	r.grant("counter")
	w := r.watch("plugin:counter/ui")
	w.until("the count", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "count 0") })
	if _, err := r.event("plugin:counter/ui", "/on", protocol.UiChange, "true"); err != nil {
		t.Fatal(err)
	}
	w.until("the toggle on", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return n.GetChildren()[2].GetChecked() })
	w.cancel()
	r.state("counter", protocol.PluginReady)
	// Shown again, it starts again with the state it had.
	w = r.watch("plugin:counter/ui")
	r.state("counter", protocol.PluginRunning)
	w.until("the toggle still on", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return len(n.GetChildren()) == 3 && n.GetChildren()[2].GetChecked()
	})
}

func TestMorePermissionsAskAgain(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("p", map[string]string{ManifestName: manifest("p", nil, ""), "main.js": `export default { activate(m) {
		m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("v1") }) } }`})
	r.state("p", protocol.PluginReady)
	r.write("p", map[string]string{ManifestName: manifest("p", []string{"storage", "net:api.example.com"}, "")})
	info := r.state("p", protocol.PluginWaiting)
	if strings.Join(info.GetAsked(), ",") != "storage,net:api.example.com" {
		t.Fatalf("asked %v", info.GetAsked())
	}
	r.grant("p")
	r.state("p", protocol.PluginReady)
	// Refused, it stays refused; asking for more, it asks again for them.
	r.write("p", map[string]string{ManifestName: manifest("p", []string{"storage", "net:api.example.com", "sessions:read"}, "")})
	info = r.state("p", protocol.PluginWaiting)
	if strings.Join(info.GetAsked(), ",") != "sessions:read" {
		t.Fatalf("asked %v; want only what is new", info.GetAsked())
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "p", Op: protocol.PluginDeny}); err != nil {
		t.Fatal(err)
	}
	r.state("p", protocol.PluginDenied)
	if _, err := os.Stat(filepath.Join(r.data, "grants.json")); err != nil {
		t.Fatalf("the grants are not kept: %v", err)
	}
}

func TestHotReloadKeepsTheLastGoodVersion(t *testing.T) {
	r := newRig(t, testLimits, nil)
	v := func(text string) string {
		return `export default { activate(m) { m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("` + text + `") }) } }`
	}
	r.write("p", map[string]string{ManifestName: manifest("p", nil, ""), "main.js": v("one")})
	w := r.watch("plugin:p/ui")
	w.until("one", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "one") })
	r.write("p", map[string]string{"main.js": v("two")})
	w.until("two", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool { return hasText(n, "two") && !u.GetStale() })
	// A version that does not parse leaves two drawn, and says why.
	r.write("p", map[string]string{"main.js": "export default { activate(m) { m.blocks.register(\"ui\", {"})
	w.until("the error", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetStale() && strings.Contains(u.GetError(), "SyntaxError") && hasText(n, "two")
	})
	if r.state("p", protocol.PluginRunning).GetStale() != true {
		t.Fatal("the listing does not say the version is stale")
	}
	// A version whose activate throws is refused the same way.
	r.write("p", map[string]string{"main.js": `export default { activate(m) { throw new Error("broken") } }`})
	w.until("the activate error", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetStale() && strings.Contains(u.GetError(), "broken") && hasText(n, "two")
	})
	r.write("p", map[string]string{"main.js": v("three")})
	w.until("three", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return hasText(n, "three") && !u.GetStale() && u.GetError() == ""
	})
	// A bad manifest likewise.
	r.write("p", map[string]string{ManifestName: manifest("p", []string{"exec"}, "")})
	w.until("the manifest's error", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetStale() && strings.Contains(u.GetError(), "exec") && hasText(n, "three")
	})
}

func TestRemovedPluginLeaves(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("p", map[string]string{ManifestName: manifest("p", nil, ""), "main.js": `export default { activate(m) {
		m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("x") }) } }`})
	r.state("p", protocol.PluginReady)
	if _, ok := r.m.BlockProps("plugin:p/ui"); !ok {
		t.Fatal("the block is not in the catalog")
	}
	if err := os.RemoveAll(filepath.Join(r.dir, "p")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(r.m.List()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the plugin stayed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := r.m.BlockProps("plugin:p/ui"); ok {
		t.Fatal("the block stayed in the catalog")
	}
}

func TestOpenURLOnlyForTheUser(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("p", map[string]string{ManifestName: manifest("p", nil, ""), "main.js": `let mako; export default { activate(m) { mako = m;
		m.blocks.register("ui", { state: { err: "" },
			async load(s) { try { await mako.commands.runCommand({ id: "mako.openUrl", arg: "https://example.com/x" }) } catch (e) { s.err = e.message } },
			render: (s, ui) => ui.column([ui.text("err " + s.err), ui.button("Open", { key: "open", onTap: ["open"] })]),
			on: { open: () => mako.commands.runCommand({ id: "mako.openUrl", arg: "https://example.com/run/1" }) } }) } }`})
	w := r.watch("plugin:p/ui")
	w.until("the refusal", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "only in answer") })
	done, err := r.event("plugin:p/ui", "/open", protocol.UiTap, "")
	if err != nil {
		t.Fatal(err)
	}
	if done.GetOpenUrl() != "https://example.com/run/1" {
		t.Fatalf("open %q", done.GetOpenUrl())
	}
}

// A version asking for more while its block shows and the last version
// runs does not start, nor leave the last running in its place: it asks,
// for what is new alone, and runs once allowed.
func TestMorePermissionsAskAgainWhileShown(t *testing.T) {
	r := newRig(t, testLimits, nil)
	v := func(text string) string {
		return `export default { activate(m) { m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("` + text + `") }) } }`
	}
	r.write("p", map[string]string{ManifestName: manifest("p", []string{"storage"}, ""), "main.js": v("one")})
	r.state("p", protocol.PluginWaiting)
	r.grant("p")
	w := r.watch("plugin:p/ui")
	w.until("one", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "one") })
	r.state("p", protocol.PluginRunning)
	r.write("p", map[string]string{ManifestName: manifest("p", []string{"storage", "sessions:read"}, ""), "main.js": v("two")})
	info := r.state("p", protocol.PluginWaiting)
	if strings.Join(info.GetAsked(), ",") != "sessions:read" || info.GetStale() {
		t.Fatalf("after asking for more: %v", info)
	}
	w.until("that it waits", func(_ *protocol.UiNode, u *protocol.BlockUpdate) bool { return u.GetState() == protocol.PluginWaiting })
	r.grant("p")
	w.until("two", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "two") })
}

// A chart is built as the vocabulary's other nodes are, and drawn.
func TestAPluginDrawsAChart(t *testing.T) {
	r := newRig(t, testLimits, nil)
	r.write("charts", map[string]string{ManifestName: manifest("charts", nil, ""),
		"main.js": block(``, `ui.column([ui.chart("bar", [{ name: "passed", tone: "success", values: [3, 5] }], { key: "c", labels: ["mon", 2], caption: "Runs" })])`)})
	tree := r.watch("plugin:charts/ui").until("the chart", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return n != nil })
	c := tree.GetChildren()[0]
	if c.GetKind() != "chart" || c.GetChart() != "bar" || c.GetText() != "Runs" || c.GetId() != "/c" || strings.Join(c.GetLabels(), ",") != "mon,2" ||
		c.GetSeries()[0].GetName() != "passed" || len(c.GetSeries()[0].GetValues()) != 2 {
		t.Fatalf("the chart is %v", c)
	}
}

func TestPreludeRichUIv2AndI18n(t *testing.T) {
	r := newRig(t, testLimits, nil)
	src := `
export default {
  activate(mako) {
    mako.i18n.register({
      en: {
        title: "Vault ({count})",
        nav: { home: "Home", notes: "Notes" },
        onlyEn: "English fallback",
      },
      ko: {
        title: "보관함 ({count})",
        nav: { home: "홈" },
      },
      "ko-KR": {
        "nav.notes": "노트",
      },
    });
    // Second register merges cleanly without clobbering existing keys.
    mako.i18n.register({
      ko: { extra: "추가 {n}" },
    });
    mako.blocks.register("ui", {
      state: { selected: "a", link: "" },
      render(s, ui, ctx) {
        const checks = [
          ctx.locale,
          ctx.t("title", { count: 3 }),
          mako.i18n.t("title", { count: 0 }, "ko-KR"),
          mako.i18n.t("nav.home", null, "ko-KR"),
          mako.i18n.t("nav.notes", null, "ko_KR"),
          mako.i18n.t("onlyEn", null, "ko-KR"),
          mako.i18n.t("extra", { n: 5 }, "ko"),
          mako.i18n.t("missing.key", { x: 1 }, "fr"),
        ].join("|");
        return ui.split(
          ui.column([
            ui.breadcrumb([ctx.t("nav.home"), ctx.t("nav.notes")], { key: "bc", onChange: ["nav"] }),
            ui.tree([
              ui.treeItem("Docs", [
                ui.treeItem("Spec", { key: "spec", icon: "file-text", value: "2KB", onTap: ["pick", "spec"] }),
              ], { key: "docs", icon: "folder", checked: true, onChange: ["exp"] }),
            ], { key: "tr" }),
            ui.collapsible("More", [
              ui.wrap([ui.chip("a"), ui.chip("b")], { gap: 4 }),
            ], { key: "col", checked: true, icon: "tag" }),
          ], { key: "left" }),
          ui.column([
            ui.text(checks, { key: "i18n" }),
            ui.grid(2, [
              ui.card([ui.button("", { key: "add", icon: "plus", variant: "ghost", size: "small", onTap: ["add"] })], { key: "c1", variant: "outline" }),
              ui.chart("donut", [{ values: [3, 7] }], { key: "ch", labels: ["A", "B"], caption: "Mix" }),
            ], { key: "g", gap: 8 }),
            ui.markdown("Open [[Spec]] " + s.link, { key: "md", onLink: ["follow"] }),
            ui.graph(
              [{ id: "a", label: "Alpha", tone: "accent" }, { id: "b", label: "Beta", weight: 2 }],
              [{ from: "a", to: "b", label: "links", dashed: true }],
              { key: "gr", value: s.selected, maxHeight: 300, caption: "Map", onChange: ["focus"] },
            ),
          ], { key: "right" }),
          { fill: true, direction: "horizontal", ratio: 0.3 },
        );
      },
      on: {
        follow(s, target) { s.link = target; },
        focus(s, nodeId) { s.selected = nodeId; },
      },
    });
  },
};`
	r.write("richv2", map[string]string{ManifestName: manifest("richv2", nil, ""), "main.js": src})
	w := r.watch("plugin:richv2/ui")
	wantI18n := "en|Vault (3)|보관함 (0)|홈|노트|English fallback|추가 5|missing.key"
	tree := w.until("rich ui v2", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, wantI18n) })
	if tree.GetKind() != "split" || !tree.GetFill() || tree.GetRatio() != 0.3 {
		t.Fatalf("root split = %+v", tree)
	}
	if _, err := r.event("plugin:richv2/ui", "/right/md", protocol.UiChange, "Spec"); err != nil {
		t.Fatal(err)
	}
	w.until("markdown onLink handled", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "Open [[Spec]] Spec") })
	if _, err := r.event("plugin:richv2/ui", "/right/gr", protocol.UiChange, "b"); err != nil {
		t.Fatal(err)
	}
	w.until("graph onChange handled", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		for _, c := range n.GetChildren() {
			if c.GetId() == "/right" {
				for _, gc := range c.GetChildren() {
					if gc.GetId() == "/right/gr" {
						return gc.GetValue() == "b"
					}
				}
			}
		}
		return false
	})
}
