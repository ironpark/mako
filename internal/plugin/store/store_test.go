package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// A plugin installs, asks for what it needs, runs; its update asks for
// what it adds alone, and waits for it, the version before not running
// on in its place; an update signed by another key is refused.
func TestInstallAndUpdateAskForWhatIsAdded(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v1 := r.publish("hello-0.1.0.makoplugin", build(t, files("hello", "0.1.0", "storage"), a), "storage")
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("hello", a, v1)}})
	r.addRegistry("reg")

	found := r.s.Search(context.Background(), "hel", "")
	if len(found) != 1 || found[0].GetRegistry() != r.registryURL("reg") || found[0].GetFingerprint() != pack.Fingerprint(pack.PublicKey(a)) ||
		!slices.Equal(found[0].GetPermissions(), []string{"storage"}) || found[0].GetInstalled() != "" {
		t.Fatalf("search found %v", found)
	}
	done, err := r.install(protocol.InstallOp, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if done.GetVersion() != "0.1.0" || !slices.Equal(done.GetAdded(), []string{"storage"}) {
		t.Fatalf("installed %v", done)
	}
	info := r.state("hello", protocol.PluginWaiting)
	if !info.GetInstalled() || info.GetSource() != r.registryURL("reg") || info.GetKey() != pack.PublicKey(a) ||
		!slices.Equal(info.GetAsked(), []string{"storage"}) {
		t.Fatalf("listed as %v", info)
	}
	if err := r.act("hello", protocol.PluginGrant); err != nil {
		t.Fatal(err)
	}
	w := r.watch("plugin:hello/ui")
	w.until(t, "v0.1.0", func(u *protocol.BlockUpdate, n *protocol.UiNode) bool { return text(n) == "v0.1.0" })
	r.state("hello", protocol.PluginRunning)

	// 0.2.0 asks for one more.
	v2 := r.publish("hello-0.2.0.makoplugin", build(t, files("hello", "0.2.0", "storage", "sessions:read"), a), "storage", "sessions:read")
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("hello", a, v1, v2)}})
	r.s.Refresh(context.Background())
	r.info("hello", func(p *protocol.PluginInfo) bool { return p.GetUpdate() == "0.2.0" })
	done, err = r.install(protocol.UpdateOp, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(done.GetAdded(), []string{"sessions:read"}) {
		t.Fatalf("the update adds %v", done.GetAdded())
	}
	info = r.state("hello", protocol.PluginWaiting)
	if info.GetVersion() != "0.2.0" || !slices.Equal(info.GetAsked(), []string{"sessions:read"}) || info.GetStale() || info.GetUpdate() != "" {
		t.Fatalf("after the update: %v", info)
	}
	w.until(t, "that it waits", func(u *protocol.BlockUpdate, _ *protocol.UiNode) bool { return u.GetState() == protocol.PluginWaiting })
	if err := r.act("hello", protocol.PluginGrant); err != nil {
		t.Fatal(err)
	}
	w.until(t, "v0.2.0", func(u *protocol.BlockUpdate, n *protocol.UiNode) bool { return text(n) == "v0.2.0" })

	// 0.3.0 signed by another key is not an update, though the index
	// says that key is the publisher's now.
	b := newKey(t)
	v3 := r.publish("hello-0.3.0.makoplugin", build(t, files("hello", "0.3.0", "storage"), b), "storage")
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("hello", b, v3)}})
	r.s.Refresh(context.Background())
	if st := r.s.Status("hello", "0.2.0"); st.Update != "" {
		t.Fatalf("a version by another key is offered as an update: %v", st)
	}
	r.refused(protocol.UpdateOp, "hello", "", "an update must be signed by the same key")
	// Listed under the pinned key but signed by another, it is refused
	// for its signature.
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("hello", a, v3)}})
	r.s.Refresh(context.Background())
	r.refused(protocol.UpdateOp, "hello", "", "not by the publisher key")
	r.state("hello", protocol.PluginRunning)
}

// The same ID in two registries: install names the one, which the plugin
// follows; the other's version, by another key, is never taken.
func TestSameIDInTwoRegistries(t *testing.T) {
	r := newRig(t, "")
	a, b := newKey(t), newKey(t)
	va := r.publish("a.makoplugin", build(t, files("dup", "1.0.0"), a))
	vb := r.publish("b.makoplugin", build(t, files("dup", "2.0.0"), b))
	r.index("one", &registry.Index{Plugins: []*registry.Entry{entry("dup", a, va)}})
	r.index("two", &registry.Index{Plugins: []*registry.Entry{entry("dup", b, vb)}})
	r.addRegistry("one")
	r.addRegistry("two")
	r.refused(protocol.InstallOp, "dup", "", "listed by 2 registries")
	if _, err := r.install(protocol.InstallOp, "dup", "one"); err != nil {
		t.Fatal(err)
	}
	found := r.s.Search(context.Background(), "", "dup")
	if len(found) != 2 || found[0].GetOtherKey() || !found[1].GetOtherKey() || found[1].GetInstalledFrom() != r.registryURL("one") {
		t.Fatalf("listings %v", found)
	}
	if st := r.s.Status("dup", "1.0.0"); st.Update != "" {
		t.Fatalf("the other registry's version is offered: %v", st)
	}
	r.refused(protocol.UpdateOp, "dup", "two", "an update must be signed by the same key")
	r.refused(protocol.InstallOp, "dup", "two", "an update must be signed by the same key")
	// Removing it forgets the pin: the other publisher's is a first
	// install, which starts with nothing allowed.
	if err := r.s.Remove("dup"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.install(protocol.InstallOp, "dup", "two"); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.s.Installed("dup"); in.Key != pack.PublicKey(b) {
		t.Fatalf("pinned %v", in)
	}
}

// What a node holds of a package is the package the index lists, signed
// by the key it lists, of the ID it lists, or nothing.
func TestPackagesThatDoNotCheckAreRefused(t *testing.T) {
	r := newRig(t, "")
	a, b := newKey(t), newKey(t)
	good := build(t, files("pkg", "1.0.0"), a)
	v := r.publish("good.makoplugin", good)
	r.addRegistry("reg")
	setVersion := func(v *registry.Version) {
		r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("pkg", a, v)}})
		r.s.Refresh(context.Background())
	}

	// Each byte of the package changed on the server.
	for i := 0; i < len(good); i += len(good) / 7 {
		bad := []byte(string(good))
		bad[i] ^= 1
		r.serve("/pkg/good.makoplugin", bad)
		setVersion(v)
		r.refused(protocol.InstallOp, "pkg", "reg", "SHA-256 or size differs")
	}
	r.serve("/pkg/good.makoplugin", good)

	wrong := *v
	wrong.SHA256 = strings.Repeat("0", 64)
	setVersion(&wrong)
	r.refused(protocol.InstallOp, "pkg", "reg", "SHA-256 or size differs")

	setVersion(r.publish("other-key.makoplugin", build(t, files("pkg", "1.0.0"), b)))
	r.refused(protocol.InstallOp, "pkg", "reg", "not by the publisher key")

	setVersion(r.publish("other-id.makoplugin", build(t, files("another", "1.0.0"), a)))
	r.refused(protocol.InstallOp, "pkg", "reg", "lists a package of the plugin another as pkg")

	mislabeled := r.publish("other-version.makoplugin", build(t, files("pkg", "1.0.1"), a))
	mislabeled.Version = "1.0.0"
	setVersion(mislabeled)
	r.refused(protocol.InstallOp, "pkg", "reg", "lists a package of pkg 1.0.1 as 1.0.0")

	exec := files("pkg", "1.0.0")
	exec["plugin.toml"] = []byte(strings.Replace(string(exec["plugin.toml"]), "permissions = []", `permissions = ["exec"]`, 1))
	setVersion(r.publish("exec.makoplugin", signedRaw(t, a, "pkg", "1.0.0", exec)))
	r.refused(protocol.InstallOp, "pkg", "reg", "exec is not granted")

	slip := files("pkg", "1.0.0")
	slip["../../escape.js"] = []byte("x")
	setVersion(r.publish("slip.makoplugin", signedRaw(t, a, "pkg", "1.0.0", slip)))
	r.refused(protocol.InstallOp, "pkg", "reg", "plain relative path")
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.dir), "escape.js")); err == nil {
		t.Fatal("a path escaped the plugins' folder")
	}
	entries, _ := os.ReadDir(r.dir)
	if len(entries) != 0 {
		t.Fatalf("refused installs left %v", entries)
	}
	setVersion(v)
	if _, err := r.install(protocol.InstallOp, "pkg", "reg"); err != nil {
		t.Fatal(err)
	}
}

// A folder written by hand is never installed over, nor removed.
func TestDevFoldersAreLeftAlone(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("mine", a, r.publish("mine.makoplugin", build(t, files("mine", "9.0.0"), a)))}})
	r.addRegistry("reg")
	dev := filepath.Join(r.dir, "mine")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range files("mine", "0.0.1") {
		if err := os.WriteFile(filepath.Join(dev, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.state("mine", protocol.PluginReady)
	r.refused(protocol.InstallOp, "mine", "reg", "a folder of a plugin in development")
	if err := r.s.Remove("mine"); err == nil || !strings.Contains(err.Error(), "Delete the folder yourself") {
		t.Fatalf("removing a dev folder: %v", err)
	}
	if found := r.s.Search(context.Background(), "", "mine"); len(found) != 1 || found[0].GetInstalled() != "dev" {
		t.Fatalf("listed as %v", found)
	}
	if info := r.state("mine", protocol.PluginReady); info.GetInstalled() || info.GetVersion() != "0.0.1" {
		t.Fatalf("the dev folder is listed as %v", info)
	}
}

// Versions go forward only.
func TestNoDowngrade(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v2 := r.publish("2.makoplugin", build(t, files("p", "2.0.0"), a))
	v1 := r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a))
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v2)}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	r.refused(protocol.UpdateOp, "p", "", "is installed already")
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}})
	r.s.Refresh(context.Background())
	r.refused(protocol.UpdateOp, "p", "", "mako does not go back a version")
}

// signedRaw is a package of files, which packing would refuse, with a
// signature that holds for them.
func signedRaw(t *testing.T, priv ed25519.PrivateKey, id, version string, f map[string][]byte) []byte {
	t.Helper()
	digest := pack.Digest(f)
	sig, _ := json.Marshal(pack.Signature{Format: pack.SignatureFormat, ID: id, Version: version, Key: pack.PublicKey(priv), Contents: digest,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, pack.Statement(id, version, digest)))})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f[pack.SignatureName] = sig
	for name, data := range f {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
