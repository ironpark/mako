package pack

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifest = `id = "hello"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "ui"
title = "Hello"
`

func testFiles() map[string][]byte {
	return map[string][]byte{
		"plugin.toml": []byte(manifest),
		"main.js":     []byte(`export default { activate(m) {} }`),
		"lib/util.js": []byte(`export const x = 1;`),
		"README.md":   []byte("# hello\n"),
	}
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestPackRoundTrips(t *testing.T) {
	priv := newKey(t)
	data, pkg, err := Build(testFiles(), priv)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.ID != "hello" || pkg.Key != PublicKey(priv) || !maps.EqualFunc(pkg.Files, testFiles(), bytes.Equal) {
		t.Fatalf("opened as %+v", pkg)
	}
	again, _, err := Build(testFiles(), priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("the same files packed to other bytes")
	}
	dir := filepath.Join(t.TempDir(), "hello")
	if err := pkg.WriteTo(dir); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFolder(dir)
	if err == nil {
		t.Fatalf("a folder holding the signature's file was packed: %v", got)
	}
	if err := os.Remove(filepath.Join(dir, SignatureName)); err != nil {
		t.Fatal(err)
	}
	got, err = ReadFolder(dir)
	if err != nil || !maps.EqualFunc(got, testFiles(), bytes.Equal) {
		t.Fatalf("written and read back as %v, %v", got, err)
	}
}

// The digest is defined byte for byte, so another program can make it.
func TestDigestIsAsDocumented(t *testing.T) {
	files := map[string][]byte{"b": []byte("22"), "a": []byte("1"), SignatureName: []byte("ignored")}
	want := "mako-plugin-contents/1\na\n1\n1b\n2\n22"
	if got := Digest(files); got != Digest(map[string][]byte{"a": []byte("1"), "b": []byte("22")}) {
		t.Fatal("the signature's file counted in the digest")
	}
	sum := sha256Hex([]byte(want))
	if Digest(files) != "sha256:"+sum {
		t.Fatalf("digest %s; want sha256:%s", Digest(files), sum)
	}
	if s := string(Statement("x", "1.0.0", "sha256:ab")); s != "mako-plugin-signature/1\nid x\nversion 1.0.0\ncontents sha256:ab\n" {
		t.Fatalf("statement %q", s)
	}
}

// No byte of a package changed makes it open with other contents: it
// does not open, or opens with the very files signed.
func TestNoByteChangesTheContentsUnseen(t *testing.T) {
	data, _, err := Build(testFiles(), newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	for i := range data {
		bad := bytes.Clone(data)
		bad[i] ^= 0x41
		pkg, err := Open(bad)
		if err != nil {
			continue
		}
		opened++
		if !maps.EqualFunc(pkg.Files, testFiles(), bytes.Equal) {
			t.Fatalf("byte %d changed opened with other contents", i)
		}
	}
	t.Logf("%d of %d changed bytes still opened, with the same contents (dates, attributes)", opened, len(data))
}

// rawZip makes an archive as an attacker would, without packing's checks.
type entry struct {
	name   string
	data   []byte
	mode   os.FileMode
	method uint16
}

func rawZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: e.method}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signed is files as entries, with a signature by priv that holds for
// them, so that only what is under test is wrong.
func signed(t *testing.T, priv ed25519.PrivateKey, id, version string, files map[string][]byte, extra ...entry) []byte {
	t.Helper()
	digest := Digest(files)
	sig, _ := json.Marshal(Signature{Format: SignatureFormat, ID: id, Version: version, Key: PublicKey(priv), Contents: digest,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, Statement(id, version, digest)))})
	var entries []entry
	for name, b := range files {
		entries = append(entries, entry{name: name, data: b})
	}
	entries = append(entries, extra...)
	entries = append(entries, entry{name: SignatureName, data: sig})
	return rawZip(t, entries...)
}

func TestHostilePackagesAreRefused(t *testing.T) {
	priv := newKey(t)
	files := testFiles()
	with := func(name string, data []byte) map[string][]byte {
		f := maps.Clone(files)
		f[name] = data
		return f
	}
	big := bytes.Repeat([]byte("a"), MaxFileBytes+1)
	many := maps.Clone(files)
	for i := range MaxFiles {
		many["f"+strings.Repeat("x", i%7)+"/"+string(rune('a'+i%26))+string(rune('a'+i/26))] = []byte("x")
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"zip slip", signed(t, priv, "hello", "0.1.0", with("../evil.js", []byte("x"))), "plain relative path"},
		{"absolute", signed(t, priv, "hello", "0.1.0", with("/etc/evil.js", []byte("x"))), "plain relative path"},
		{"backslash", signed(t, priv, "hello", "0.1.0", with(`lib\..\..\evil.js`, []byte("x"))), "plain relative path"},
		{"drive", signed(t, priv, "hello", "0.1.0", with("C:/evil.js", []byte("x"))), "plain relative path"},
		{"dot file", signed(t, priv, "hello", "0.1.0", with(".hidden/x.js", []byte("x"))), "plain relative path"},
		{"deep", signed(t, priv, "hello", "0.1.0", with("a/b/c/d/e/f/g/h/i.js", []byte("x"))), "deeper"},
		{"symlink", signed(t, priv, "hello", "0.1.0", files, entry{name: "link.js", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777}), "not a regular file"},
		{"folder", signed(t, priv, "hello", "0.1.0", files, entry{name: "lib2/", mode: os.ModeDir | 0o755}), "plain relative path"},
		{"twice", signed(t, priv, "hello", "0.1.0", files, entry{name: "main.js", data: []byte("evil()")}), "twice"},
		{"twice by case", signed(t, priv, "hello", "0.1.0", files, entry{name: "MAIN.js", data: []byte("evil()")}), "twice"},
		{"huge file", signed(t, priv, "hello", "0.1.0", with("big.js", big)), "over 2 MiB"},
		{"too many", signed(t, priv, "hello", "0.1.0", many), "more than 256"},
		{"unsigned", rawZip(t, entry{name: "plugin.toml", data: files["plugin.toml"]}, entry{name: "main.js", data: files["main.js"]}), "not signed"},
		{"not a zip", []byte("PK\x03\x04 not really"), "not a zip"},
		{"exec", signed(t, priv, "hello", "0.1.0", with("plugin.toml", []byte(strings.Replace(manifest, `["storage"]`, `["exec"]`, 1)))), "exec is not granted"},
		{"other id", signed(t, priv, "other", "0.1.0", files), "signature is for other"},
		{"no manifest", signed(t, priv, "hello", "0.1.0", map[string][]byte{"main.js": files["main.js"]}), "no plugin.toml"},
		{"no entry", signed(t, priv, "hello", "0.1.0", map[string][]byte{"plugin.toml": files["plugin.toml"]}), "entry main.js is not in"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Open(c.data)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("opened with %v; want an error saying %q", err, c.want)
			}
		})
	}
}

// A file that inflates past its bound is refused once read, whatever its
// header says, and one whose header lies about its size is refused.
func TestZipBombsAreRefused(t *testing.T) {
	priv := newKey(t)
	bomb := bytes.Repeat([]byte{0}, MaxFileBytes+4096)
	files := testFiles()
	data := signed(t, priv, "hello", "0.1.0", files, entry{name: "bomb.js", data: bomb, method: zip.Deflate})
	if len(data) > 64<<10 {
		t.Fatalf("the bomb is %d bytes; it should compress", len(data))
	}
	if _, err := Open(data); err == nil || !strings.Contains(err.Error(), "over 2 MiB") {
		t.Fatalf("a bomb opened: %v", err)
	}
	// The central directory says the bomb is small.
	lied := setUncompressedSize(t, data, "bomb.js", 10)
	if _, err := Open(lied); err == nil {
		t.Fatalf("a bomb with a lying header opened: %v", err)
	}
	small := signed(t, priv, "hello", "0.1.0", files, entry{name: "x.js", data: bytes.Repeat([]byte{1}, 1000), method: zip.Deflate})
	if _, err := Open(setUncompressedSize(t, small, "x.js", 10)); err == nil {
		t.Fatal("a file longer than its header says opened")
	}
	// Many files each in bounds are bounded in all.
	var entries []entry
	for i := range 5 {
		entries = append(entries, entry{name: "z" + string(rune('a'+i)) + ".js", data: bytes.Repeat([]byte{0}, MaxFileBytes), method: zip.Deflate})
	}
	if _, err := Open(signed(t, priv, "hello", "0.1.0", files, entries...)); err == nil || !strings.Contains(err.Error(), "in all") {
		t.Fatalf("files over the bound in all opened: %v", err)
	}
	if _, err := Open(make([]byte, MaxPackageBytes+1)); err == nil || !strings.Contains(err.Error(), "over 8 MiB") {
		t.Fatalf("an oversized package opened: %v", err)
	}
}

// setUncompressedSize rewrites the size the central directory gives a
// file.
func setUncompressedSize(t *testing.T, data []byte, name string, size uint32) []byte {
	t.Helper()
	out := bytes.Clone(data)
	sig := []byte{0x50, 0x4b, 0x01, 0x02}
	for i := 0; ; {
		j := bytes.Index(out[i:], sig)
		if j < 0 {
			t.Fatalf("no central record of %s", name)
		}
		at := i + j
		nameLen := int(binary.LittleEndian.Uint16(out[at+28:]))
		if string(out[at+46:at+46+nameLen]) == name {
			binary.LittleEndian.PutUint32(out[at+24:], size)
			return out
		}
		i = at + 4
	}
}

func TestSignaturesHoldOnlyForTheirKey(t *testing.T) {
	a, b := newKey(t), newKey(t)
	data, _, err := Build(testFiles(), a)
	if err != nil {
		t.Fatal(err)
	}
	other, pkg, err := Resign(data, b)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Key != PublicKey(b) || bytes.Equal(other, data) {
		t.Fatal("resigned with the same key")
	}
	// A signature copied from another package does not hold.
	files := testFiles()
	files["main.js"] = []byte("evil()")
	var good Signature
	pa, _ := Open(data)
	_ = json.Unmarshal(pa.sig, &good)
	sig, _ := json.Marshal(good)
	var entries []entry
	for n, d := range files {
		entries = append(entries, entry{name: n, data: d})
	}
	entries = append(entries, entry{name: SignatureName, data: sig})
	if _, err := Open(rawZip(t, entries...)); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("contents changed under a signature opened: %v", err)
	}
	// A signature's file with fields of its own is refused.
	good.Format = SignatureFormat
	extra := strings.Replace(string(sig), `"format"`, `"trust":"me","format"`, 1)
	entries[len(entries)-1].data = []byte(extra)
	if _, err := Open(rawZip(t, entries...)); err == nil || !strings.Contains(err.Error(), "does not read") {
		t.Fatalf("a signature with unknown fields opened: %v", err)
	}
}

func TestKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "publisher.key")
	pub, err := Keygen(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key is %v", fi.Mode())
	}
	if _, err := Keygen(path); err == nil {
		t.Fatal("a key was written over")
	}
	priv, err := LoadKey(path)
	if err != nil || PublicKey(priv) != pub {
		t.Fatalf("loaded %v, %v", err, PublicKey(priv))
	}
	if fp := Fingerprint(pub); len(fp) != 39 || strings.Count(fp, "-") != 7 {
		t.Fatalf("fingerprint %q", fp)
	}
	for _, bad := range []string{"", "ed25519:", "rsa:AAAA", "ed25519:" + strings.Repeat("A", 44)} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("%q read as a key", bad)
		}
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
