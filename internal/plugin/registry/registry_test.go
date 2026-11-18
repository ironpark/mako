package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin/pack"
)

func key(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pack.EncodeKey(pub)
}

// alias writes a key with its last character's unused low bit flipped:
// the same key's bytes, in a spelling mako does not write.
func alias(key string) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return key[:len(key)-1] + string(alpha[strings.IndexByte(alpha, key[len(key)-1])^0x1])
}

func goodIndex(t *testing.T) *Index {
	return &Index{Format: Format, Name: "test", Plugins: []*Entry{{
		ID: "hello", Publisher: "someone", Key: key(t), Name: "Hello", Description: "says hello",
		Versions: []*Version{
			{Version: "0.1.0", API: 1, URL: "https://example.com/hello-0.1.0.makoplugin", SHA256: strings.Repeat("a", 64), Size: 100, Permissions: []string{}},
			{Version: "0.2.0", API: 1, URL: "https://example.com/hello-0.2.0.makoplugin", SHA256: strings.Repeat("b", 64), Size: 100, Permissions: []string{"storage"}},
		},
	}}, Blocklist: []*Block{{ID: "evil", Reason: "steals"}}}
}

func TestIndexRoundTrips(t *testing.T) {
	idx := goodIndex(t)
	b, err := idx.Marshal(false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b, false)
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Find("hello").Newest(1); v.Version != "0.2.0" {
		t.Fatalf("newest %v", v)
	}
	if got.Find("hello").Versions[0].Version != "0.2.0" {
		t.Fatal("versions are not written newest first")
	}
	if got.Find("hello").Newest(2) != nil {
		t.Fatal("a version for another api")
	}
}

func TestIndexesThatDoNotCheckAreRefused(t *testing.T) {
	mutate := func(fn func(*Index)) []byte {
		idx := goodIndex(t)
		fn(idx)
		b, _ := json.Marshal(idx)
		return b
	}
	huge := append([]byte(`{"format":"mako-registry/1","name":"`), bytes.Repeat([]byte("a"), MaxIndexBytes)...)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"enormous", huge, "over 4 MiB"},
		{"malformed", []byte(`{"format": "mako-registry/1", "plugins": [`), "does not read"},
		{"unknown field", []byte(`{"format": "mako-registry/1", "verified": true}`), "does not read"},
		{"newer", []byte(`{"format": "mako-registry/2", "plugins": [], "signed_by": "x"}`), "for a newer mako"},
		{"no format", []byte(`{"plugins": []}`), "this mako reads"},
		{"trailing", append(mutate(func(*Index) {}), []byte(` {}`)...), "more after its end"},
		{"twice", mutate(func(i *Index) { i.Plugins = append(i.Plugins, i.Plugins[0]) }), "twice"},
		{"bad id", mutate(func(i *Index) { i.Plugins[0].ID = "../evil" }), "not a plugin's ID"},
		{"mako id", mutate(func(i *Index) { i.Plugins[0].ID = "mako-core" }), "not a plugin's ID"},
		{"bad key", mutate(func(i *Index) { i.Plugins[0].Key = "rsa:abc" }), "publisher key"},
		{"key spelt otherwise", mutate(func(i *Index) { i.Plugins[0].Key = alias(i.Plugins[0].Key) }), "another spelling"},
		{"blocked key spelt otherwise", mutate(func(i *Index) { i.Blocklist = []*Block{{Key: alias(key(t)), Reason: "x"}} }), "another spelling"},
		{"bad sha", mutate(func(i *Index) { i.Plugins[0].Versions[0].SHA256 = "ABC" }), "64 lowercase hex"},
		{"http", mutate(func(i *Index) { i.Plugins[0].Versions[0].URL = "http://example.com/x" }), "not an https URL"},
		{"file", mutate(func(i *Index) { i.Plugins[0].Versions[0].URL = "file:///etc/passwd" }), "not an https URL"},
		{"loopback", mutate(func(i *Index) { i.Plugins[0].Versions[0].URL = "https://127.0.0.1/x" }), "IP address"},
		{"port", mutate(func(i *Index) { i.Plugins[0].Versions[0].URL = "https://example.com:8443/x" }), "port"},
		{"exec", mutate(func(i *Index) { i.Plugins[0].Versions[0].Permissions = []string{"exec"} }), "exec is not granted"},
		{"huge size", mutate(func(i *Index) { i.Plugins[0].Versions[0].Size = 1 << 40 }), "is not of a package"},
		{"no versions", mutate(func(i *Index) { i.Plugins[0].Versions = nil }), "1 to 64 versions"},
		{"spoofing", mutate(func(i *Index) { i.Plugins[0].Name = "Hello‮" }), "reordering"},
		{"blank block", mutate(func(i *Index) { i.Blocklist = []*Block{{Reason: "x"}} }), "names an ID or a key"},
		{"no reason", mutate(func(i *Index) { i.Blocklist = []*Block{{ID: "x"}} }), "says why"},
		{"null", []byte(`{"format": "mako-registry/1", "plugins": [null]}`), "null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.data, false); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("read with %v; want an error saying %q", err, c.want)
			}
		})
	}
}

func TestRegistryURLs(t *testing.T) {
	good := map[string]string{
		"https://github.com/someone/plugins":              "https://raw.githubusercontent.com/someone/plugins/HEAD/mako-registry.json",
		"https://GitHub.com/someone/plugins.git/":         "https://raw.githubusercontent.com/someone/plugins/HEAD/mako-registry.json",
		"https://gitlab.com/group/sub/plugins":            "https://gitlab.com/group/sub/plugins/-/raw/HEAD/mako-registry.json",
		"https://example.org/raw/main/mako-registry.json": "https://example.org/raw/main/mako-registry.json",
	}
	for in, want := range good {
		if _, got, err := Normalize(in, false); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"/home/me/registry":                          "not an https URL",
		"./registry":                                 "not an https URL",
		"file:///home/me/mako-registry.json":         "not an https URL",
		"ssh://git@github.com/someone/plugins":       "not an https URL",
		"git://github.com/someone/plugins":           "not an https URL",
		"git@github.com:someone/plugins.git":         "not an https URL",
		"http://github.com/someone/plugins":          "not an https URL",
		"javascript:alert(1)":                        "not an https URL",
		"https://user:pw@github.com/someone/p":       "user or password",
		"https://10.0.0.1/mako-registry.json":        "IP address",
		"https://localhost/mako-registry.json":       "host on the internet",
		"https://example.com:444/mako-registry.json": "port",
		"https://github.com/someone":                 "not a GitHub or GitLab repository",
		"https://example.com/mako-registry.json?x=1": "no query",
		"http://127.0.0.1:9/mako-registry.json":      "not an https URL",
	}
	for in, want := range bad {
		if _, _, err := Normalize(in, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want an error saying %q", in, err, want)
		}
	}
	// Loopback, for tests alone, lets http on loopback through, and only that.
	if _, _, err := Normalize("http://127.0.0.1:9/r/mako-registry.json", true); err != nil {
		t.Errorf("loopback in a test: %v", err)
	}
	if _, _, err := Normalize("http://example.com/r/mako-registry.json", true); err == nil {
		t.Error("http off loopback in a test")
	}
}

// The fetcher bounds what it reads and where it goes.
func TestFetcherBounds(t *testing.T) {
	slow := make(chan struct{})
	defer close(slow)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fine")) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bytes.Repeat([]byte("x"), 2000)) })
	mux.HandleFunc("/big-chunked", func(w http.ResponseWriter, _ *http.Request) {
		for range 20 {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 200))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-slow:
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/to-http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/x", http.StatusFound)
	})
	mux.HandleFunc("/to-ip", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.1.2.3/x", http.StatusFound)
	})
	mux.HandleFunc("/to-file", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := NewFetcher(true)
	get := func(path string, max int64, within time.Duration) ([]byte, error) {
		return f.Get(context.Background(), srv.URL+path, max, within)
	}
	if b, err := get("/ok", 100, time.Second); err != nil || string(b) != "fine" {
		t.Fatalf("ok: %q, %v", b, err)
	}
	if b, err := get("/hop", 100, time.Second); err != nil || string(b) != "fine" {
		t.Fatalf("a redirect within bounds: %q, %v", b, err)
	}
	for path, want := range map[string]string{
		"/big":         "over the 0 KiB",
		"/big-chunked": "more than the 0 KiB",
		"/missing":     "404",
		"/to-http":     "not an https URL",
		"/to-ip":       "IP address",
		"/to-file":     "not an https URL",
		"/loop":        "redirects",
	} {
		if _, err := get(path, 1000, 5*time.Second); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want an error saying %q", path, err, want)
		}
	}
	if _, err := get("/slow", 100, 200*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Errorf("slow: %v", err)
	}
	// Without the test's loopback, loopback is neither a URL nor an
	// address the fetcher reaches.
	if _, err := NewFetcher(false).Get(context.Background(), srv.URL+"/ok", 100, time.Second); err == nil {
		t.Error("loopback reached outside a test")
	}
}
