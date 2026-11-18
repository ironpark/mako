package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// alias writes a key with its last character's unused low bit flipped:
// the same key's bytes, in a spelling mako does not write.
func alias(key string) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return key[:len(key)-1] + string(alpha[strings.IndexByte(alpha, key[len(key)-1])^0x1])
}

// rekey writes a package's signature file again naming key, leaving the
// signature, which covers the ID, version and contents alone.
func rekey(t *testing.T, data []byte, key string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == pack.SignatureName {
			var sig pack.Signature
			if err := json.Unmarshal(b, &sig); err != nil {
				t.Fatal(err)
			}
			sig.Key = key
			b, _ = json.Marshal(sig)
		}
		h := &zip.FileHeader{Name: f.Name, Method: zip.Store}
		h.SetMode(0o644)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A publisher a registry blocks by key does not get past the block by
// writing the key otherwise, in its index and its package's signature:
// both are refused as they are read.
func TestAKeySpeltOtherwiseIsStillBlocked(t *testing.T) {
	r := newRig(t, "")
	evil := newKey(t)
	canon := pack.PublicKey(evil)
	alt := alias(canon)
	r.index("guard", &registry.Index{Blocklist: []*registry.Block{{Key: canon, Reason: "malware publisher"}}})
	r.addRegistry("guard")

	v := r.publish("s.makoplugin", rekey(t, build(t, files("stealer", "1.0.0"), evil), alt))
	e := entry("stealer", evil, v)
	e.Key = alt
	r.serveRaw("evil", &registry.Index{Plugins: []*registry.Entry{e}})
	r.addRegistry("evil")
	if regs := r.s.Registries(); !strings.Contains(regs[1].GetError(), "another spelling") {
		t.Fatalf("an index naming a key otherwise read: %v", regs[1])
	}
	r.refused(protocol.InstallOp, "stealer", "evil", "could not be read")

	// Listed with the key as mako writes it, the package naming it
	// otherwise does not open; and the key is blocked as written.
	e.Key = canon
	r.index("evil", &registry.Index{Plugins: []*registry.Entry{e}})
	r.s.Refresh(context.Background())
	r.refused(protocol.InstallOp, "stealer", "evil", "blocked by the registry")
}

// A pin kept by a mako that took other spellings is still compared as
// the key it names: a block by the key reaches the plugin, and an update
// signed by it is taken.
func TestAPinSpeltOtherwiseIsTheKey(t *testing.T) {
	r := newRig(t, "")
	a := newKey(t)
	v1 := r.publish("1.makoplugin", build(t, files("p", "1.0.0"), a))
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}})
	r.addRegistry("reg")
	if _, err := r.install(protocol.InstallOp, "p", ""); err != nil {
		t.Fatal(err)
	}
	// installs.json as an older mako could have kept it.
	b, err := os.ReadFile(r.s.path("installs.json"))
	if err != nil {
		t.Fatal(err)
	}
	canon := pack.PublicKey(a)
	if err := os.WriteFile(r.s.path("installs.json"), []byte(strings.ReplaceAll(string(b), canon, alias(canon))), 0o600); err != nil {
		t.Fatal(err)
	}
	r.index("reg", &registry.Index{Plugins: []*registry.Entry{entry("p", a, v1)}, Blocklist: []*registry.Block{{Key: canon, Reason: "key stolen"}}})
	r.s.Refresh(context.Background())
	again := New(Config{DataDir: r.data, Loopback: true})
	if st := again.Status("p", "1.0.0"); !st.Installed || !strings.Contains(st.Blocked, "key stolen") {
		t.Fatalf("a pin spelt otherwise escapes a block of its key: %+v", st)
	}
}
