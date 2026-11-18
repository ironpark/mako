package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// A folder written by hand where an installed plugin's was, its install
// still pinned, is a plugin in development: listed as one, neither
// updated over nor removed, and given none of what the plugin installed
// was allowed or kept.
func TestAFolderWrittenOverAnInstallIsADevFolder(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v1 := r.publish("1.makoplugin", build(t, files("tool", "1.0.0", "storage"), a))
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("tool", a, v1)}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "tool", ""); err != nil {
		t.Fatal(err)
	}
	r.state("tool", protocol.PluginWaiting)
	if err := r.act("tool", protocol.PluginGrant); err != nil {
		t.Fatal(err)
	}
	r.state("tool", protocol.PluginReady)
	kept := filepath.Join(r.data, "tool", "storage.json")
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte(`{"token":"the installed plugin's"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The user moves the folder away and starts a plugin of their own
	// of the ID.
	mine := files("tool", "0.1.0", "storage")
	mine["MY-WORK.txt"] = []byte("hours of work")
	r.writeFolder("tool", mine)
	info := r.info("tool", func(p *protocol.PluginInfo) bool { return p.GetVersion() == "0.1.0" && !p.GetStale() })
	if info.GetInstalled() || info.GetSource() != "" || info.GetState() != protocol.PluginWaiting || len(info.GetGranted()) != 0 {
		t.Fatalf("the hand-written folder is listed as %v", info)
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Fatalf("what the installed plugin kept is the hand-written one's: %v", err)
	}
	if found := r.s.Search(context.Background(), "", "tool"); len(found) != 1 || found[0].GetInstalled() != "dev" {
		t.Fatalf("listed as %v", found)
	}
	v2 := r.publish("2.makoplugin", build(t, files("tool", "2.0.0", "storage"), a))
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("tool", a, v1, v2)}})
	r.s.Refresh(context.Background())
	r.refused(protocol.UpdateOp, "tool", "", "a folder of a plugin in development")
	r.refused(protocol.InstallOp, "tool", "", "a folder of a plugin in development")
	if err := r.s.Remove("tool"); err == nil || !strings.Contains(err.Error(), "Delete the folder yourself") {
		t.Fatalf("removing the hand-written folder: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(r.dir, "tool", "MY-WORK.txt")); err != nil || string(b) != "hours of work" {
		t.Fatalf("the hand-written folder was changed: %q, %v", b, err)
	}
}

// A folder edited in place is no longer the one installed either, but a
// file of the system's beside the plugin's, starting with a dot, leaves
// it the one installed.
func TestAnInstalledFolderIsReadByItsContents(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a)))}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.dir, "p")
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("finder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r.s.devFolder("p") {
		t.Fatal("a dot file makes the install a dev folder")
	}
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte(`export default { activate(m) {} }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.s.devFolder("p") {
		t.Fatal("a folder edited in place is taken for the install")
	}
}

// crashRig is a node's store and plugins over folders a test lays out as
// a daemon stopped midway would have left them, with no manager reading
// the folder: what Reconcile makes of them alone is seen.
type crashRig struct {
	t         *testing.T
	dir, data string
	s         *Store
	m         *plugin.Manager
}

func newCrashRig(t *testing.T) *crashRig {
	base := t.TempDir()
	c := &crashRig{t: t, dir: filepath.Join(base, "plugins"), data: filepath.Join(base, "plugin-data")}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// start is the daemon starting over what was laid out.
func (c *crashRig) start() {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c.s = New(Config{DataDir: c.data, Loopback: true, Log: log})
	c.m = plugin.New(plugin.Config{Dir: c.dir, DataDir: c.data, Installs: c.s, Log: log})
	c.s.Attach(c.m)
	c.s.Reconcile()
}

// place writes a package's folder at name, as an install writes it.
func (c *crashRig) place(name string, data []byte) {
	c.t.Helper()
	pkg, err := pack.Open(data)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := pkg.WriteTo(filepath.Join(c.dir, name)); err != nil {
		c.t.Fatal(err)
	}
}

// pins writes installs.json.
func (c *crashRig) pins(in map[string]Install) {
	c.t.Helper()
	if err := writeJSON(filepath.Join(c.data, "installs.json"), in); err != nil {
		c.t.Fatal(err)
	}
}

// pin is the install a package makes.
func pin(t *testing.T, data []byte) *Install {
	pkg, err := pack.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return &Install{Registry: "https://example.com/r.json", Key: pkg.Key, Version: pkg.Manifest.Version, Contents: pkg.Digest}
}

func (c *crashRig) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(c.dir, name))
	return err == nil
}

// Whichever step of an install, an update or a removal a crash cuts
// short, the daemon starting again finds a pin naming the folder in
// place, or none for none, and nothing staged left over.
func TestAnInstallCutShortIsSettled(t *testing.T) {
	a := newKey(t)
	one, two := build(t, files("p", "1.0.0"), a), build(t, files("p", "2.0.0"), a)
	pinned := func(c *crashRig) string {
		in, ok := c.s.Installed("p")
		if !ok {
			return ""
		}
		if in.Pending != nil {
			return in.Version + " pending " + in.Pending.Version
		}
		return in.Version
	}
	withPending := func(prev, next *Install) map[string]Install {
		in := Install{Pending: next}
		if prev != nil {
			in = *prev
			in.Pending = next
		}
		return map[string]Install{"p": in}
	}
	cases := []struct {
		name   string
		layout func(c *crashRig)
		want   string // the version pinned, "" for none
	}{
		{"update staged, not swapped", func(c *crashRig) {
			c.place("p", one)
			c.place(".install-p-0123456789ab", two)
			c.pins(withPending(pin(t, one), pin(t, two)))
		}, "1.0.0"},
		{"update moved aside, not swapped", func(c *crashRig) {
			c.place(".p.old-0123456789ab", one)
			c.place(".install-p-0123456789ab", two)
			c.pins(withPending(pin(t, one), pin(t, two)))
		}, "1.0.0"},
		{"update swapped, not pinned", func(c *crashRig) {
			c.place("p", two)
			c.place(".p.old-0123456789ab", one)
			c.pins(withPending(pin(t, one), pin(t, two)))
		}, "2.0.0"},
		{"first install pinned pending, not swapped", func(c *crashRig) {
			c.place(".install-p-0123456789ab", one)
			c.pins(withPending(nil, pin(t, one)))
		}, ""},
		{"first install swapped, not pinned", func(c *crashRig) {
			c.place("p", one)
			c.pins(withPending(nil, pin(t, one)))
		}, "1.0.0"},
		{"removal moved aside, pin kept", func(c *crashRig) {
			c.place(".p.removed-0123456789ab", one)
			c.pins(map[string]Install{"p": *pin(t, one)})
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCrashRig(t)
			tc.layout(c)
			c.start()
			if got := pinned(c); got != tc.want {
				t.Fatalf("pinned %q; want %q", got, tc.want)
			}
			if tc.want != "" && c.s.devFolder("p") || tc.want == "" && c.exists("p") {
				t.Fatal("the folder in place is not the one pinned")
			}
			entries, _ := os.ReadDir(c.dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") {
					t.Fatalf("%s is left over", e.Name())
				}
			}
			if b, _ := os.ReadFile(filepath.Join(c.data, "installs.json")); strings.Contains(string(b), "pending") {
				t.Fatalf("installs.json still holds a pending version:\n%s", b)
			}
		})
	}
}

// A removed plugin's pin, its folder gone, and one whose folder was
// written over by hand, are forgotten as the daemon starts, and with
// them what the plugin was allowed and kept.
func TestAPinWithoutItsFolderIsForgotten(t *testing.T) {
	a := newKey(t)
	one := build(t, files("p", "1.0.0", "storage"), a)
	for _, hand := range []bool{false, true} {
		c := newCrashRig(t)
		c.pins(map[string]Install{"p": *pin(t, one)})
		if err := writeJSON(filepath.Join(c.data, "grants.json"), map[string]any{"p": map[string]any{"granted": []string{"storage"}}}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(c.data, "p", "storage.json"), map[string]string{"k": "v"}); err != nil {
			t.Fatal(err)
		}
		if hand {
			if err := os.MkdirAll(filepath.Join(c.dir, "p"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, b := range files("p", "0.0.1", "storage") {
				if err := os.WriteFile(filepath.Join(c.dir, "p", name), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		c.start()
		if _, ok := c.s.Installed("p"); ok {
			t.Fatalf("hand %v: the pin stands", hand)
		}
		if b, _ := os.ReadFile(filepath.Join(c.data, "grants.json")); strings.Contains(string(b), "storage") {
			t.Fatalf("hand %v: the grants stand: %s", hand, b)
		}
		if _, err := os.Stat(filepath.Join(c.data, "p")); !os.IsNotExist(err) {
			t.Fatalf("hand %v: what the plugin kept stands", hand)
		}
		if hand != c.exists("p") {
			t.Fatalf("hand %v: the folder is changed", hand)
		}
	}
}

// An update a crash cut short before its folder was swapped is tried
// again as an update, not refused as installed already.
func TestAnUpdateCutShortIsRetried(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v1 := r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a))
	two := build(t, files("p", "2.0.0"), a)
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	in, _ := r.s.Installed("p")
	in.Pending = pin(t, two)
	if err := r.s.putInstall("p", &in); err != nil {
		t.Fatal(err)
	}
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1, r.publish("2.makoplugin", two))}})
	r.s.Refresh(context.Background())
	if done, err := r.install(protocol.UpdateOp, "p", ""); err != nil || done.GetVersion() != "2.0.0" {
		t.Fatalf("the update tried again: %v, %v", done, err)
	}
	r.info("p", func(p *protocol.PluginInfo) bool { return p.GetVersion() == "2.0.0" && p.GetInstalled() })
}

// A node remembers whose key it installed an ID by: a later first
// install of the ID by another key says so, in the listing and in what
// the install answers, and goes ahead.
func TestAnotherPublisherOfAnIDIsNoted(t *testing.T) {
	r := newRig(t, "")
	a, b := newKey(t), newKey(t)
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, r.publish("a.makoplugin", build(t, files("p", "1.0.0"), a)))}})
	r.addRegistry("reg")
	done, err := r.install(protocol.InstallOp, "p", "")
	if err != nil || done.GetPreviousFingerprint() != "" {
		t.Fatalf("a first install: %v, %v", done, err)
	}
	if err := r.s.Remove("p"); err != nil {
		t.Fatal(err)
	}
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", b, r.publish("b.makoplugin", build(t, files("p", "1.0.0"), b)))}})
	r.s.Refresh(context.Background())
	fa := pack.Fingerprint(pack.PublicKey(a))
	if found := r.s.Search(context.Background(), "", "p"); len(found) != 1 || found[0].GetPreviousFingerprint() != fa {
		t.Fatalf("listed as %v", found)
	}
	done, err = r.install(protocol.InstallOp, "p", "")
	if err != nil || done.GetPreviousFingerprint() != fa {
		t.Fatalf("installed by another key: %v, %v", done, err)
	}
	if found := r.s.Search(context.Background(), "", "p"); found[0].GetPreviousFingerprint() != "" {
		t.Fatalf("installed, listed as %v", found)
	}
}

// What a node remembers of publishers is bounded: the ID installed
// longest ago goes first.
func TestKnownPublishersAreBounded(t *testing.T) {
	r := newRig(t, "")
	k := pack.PublicKey(newKey(t))
	for i := range maxKnown + 1 {
		r.s.rememberKey(strings.Repeat("a", 1+i/26)+string(rune('a'+i%26)), k)
	}
	if len(r.s.known) != maxKnown {
		t.Fatalf("%d remembered", len(r.s.known))
	}
	if _, ok := r.s.known["aa"]; ok {
		t.Fatal("the oldest is remembered")
	}
}
