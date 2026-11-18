package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// rig is a node's plugins and store over a test's registries, served on
// loopback by one server: an index at /<name>/mako-registry.json and
// packages at /pkg/<file>, both of which a test changes as it goes.
type rig struct {
	t     *testing.T
	srv   *httptest.Server
	dir   string
	data  string
	m     *plugin.Manager
	s     *Store
	logs  *logBuffer
	mu    sync.Mutex
	files map[string][]byte // path → body
	hooks map[string]http.HandlerFunc
}

type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newRig(t *testing.T, defaultRegistry string) *rig {
	t.Helper()
	r := &rig{t: t, files: map[string][]byte{}, hooks: map[string]http.HandlerFunc{}, logs: &logBuffer{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		hook := r.hooks[req.URL.Path]
		body, ok := r.files[req.URL.Path]
		r.mu.Unlock()
		if hook != nil {
			hook(w, req)
			return
		}
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.srv.Close)
	base := t.TempDir()
	r.dir, r.data = filepath.Join(base, "plugins"), filepath.Join(base, "plugin-data")
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.s = New(Config{DataDir: r.data, Default: defaultRegistry, Loopback: true, Log: log})
	r.m = plugin.New(plugin.Config{
		Dir: r.dir, DataDir: r.data, Poll: 30 * time.Millisecond, Log: log, Installs: r.s,
		Limits: plugin.Limits{CPU: 10 * time.Second, Wall: 20 * time.Second, Memory: 16 << 20, Updates: 20 * time.Millisecond},
	})
	r.s.Attach(r.m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("log:\n%s", r.logs.String())
		}
	})
	return r
}

// url is the server's URL of a path.
func (r *rig) url(path string) string { return r.srv.URL + path }

// registryURL is the URL of the registry of the name.
func (r *rig) registryURL(name string) string { return r.url("/" + name + "/" + registry.IndexName) }

func (r *rig) serve(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = body
}

func (r *rig) hook(path string, h http.HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks[path] = h
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// files are a plugin's files: one block, ui, that draws its version.
func files(id, version string, perms ...string) map[string][]byte {
	q := make([]string, len(perms))
	for i, p := range perms {
		q[i] = `"` + p + `"`
	}
	return map[string][]byte{
		"plugin.toml": []byte(`id = "` + id + `"
version = "` + version + `"
publisher = "test"
api = 1
entry = "main.js"
permissions = [` + strings.Join(q, ", ") + `]

[[contributes.block]]
id = "ui"
title = "Test"
`),
		"main.js": []byte(`export default { activate(m) { m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("v` + version + `") }) } }`),
	}
}

// build packs files signed by priv.
func build(t *testing.T, f map[string][]byte, priv ed25519.PrivateKey) []byte {
	t.Helper()
	data, _, err := pack.Build(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// publish serves a package and returns the index's version for it: its
// manifest's, or 1.0.0 for a package that does not open.
func (r *rig) publish(name string, data []byte, perms ...string) *registry.Version {
	r.t.Helper()
	pkg, err := pack.Open(data)
	version := "1.0.0"
	if err == nil {
		version = pkg.Manifest.Version
	}
	path := "/pkg/" + name
	r.serve(path, data)
	sum := sha256.Sum256(data)
	if perms == nil {
		perms = []string{}
	}
	return &registry.Version{Version: version, API: 1, URL: r.url(path), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Permissions: perms}
}

// index serves a registry's index.
func (r *rig) index(name string, idx *registry.Index) {
	r.t.Helper()
	idx.Format = registry.Format
	if idx.Name == "" {
		idx.Name = name
	}
	b, err := idx.Marshal(true)
	if err != nil {
		r.t.Fatal(err)
	}
	r.serve("/"+name+"/"+registry.IndexName, b)
}

func entry(id string, priv ed25519.PrivateKey, versions ...*registry.Version) *registry.Entry {
	return &registry.Entry{ID: id, Publisher: "test", Key: pack.PublicKey(priv), Versions: versions}
}

func (r *rig) addRegistry(name string) {
	r.t.Helper()
	if err := r.s.AddRegistry(context.Background(), r.registryURL(name)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) install(op, id, reg string) (*protocol.PluginInstalled, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if reg != "" {
		reg = r.registryURL(reg)
	}
	return r.s.Install(ctx, op, id, reg)
}

// refused runs an install that must fail saying want, and leaves the
// plugin as it was.
func (r *rig) refused(op, id, reg, want string) {
	r.t.Helper()
	before := r.snapshot(id)
	_, err := r.install(op, id, reg)
	if err == nil || !strings.Contains(err.Error(), want) {
		r.t.Fatalf("%s %s from %s: %v; want an error saying %q", op, id, reg, err, want)
	}
	if after := r.snapshot(id); after != before {
		r.t.Fatalf("a refused %s changed the plugin's folder or install:\n%s\nto\n%s", op, before, after)
	}
}

// snapshot is a plugin's folder and install as they stand.
func (r *rig) snapshot(id string) string {
	var b strings.Builder
	_ = filepath.WalkDir(filepath.Join(r.dir, id), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			b.WriteString(p + "=" + string(data) + "\n")
		}
		return nil
	})
	in, ok := r.s.Installed(id)
	if ok {
		b.WriteString("install " + in.Registry + " " + in.Key + " " + in.Version)
	}
	return b.String()
}

// info waits for the plugin to stand as cond says.
func (r *rig) info(id string, cond func(*protocol.PluginInfo) bool) *protocol.PluginInfo {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *protocol.PluginInfo
	for time.Now().Before(deadline) {
		for _, p := range r.m.List() {
			if p.GetId() == id {
				last = p
				if cond(p) {
					return p
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("plugin %s never stood as wanted; last %v", id, last)
	return nil
}

func (r *rig) state(id, state string) *protocol.PluginInfo {
	r.t.Helper()
	return r.info(id, func(p *protocol.PluginInfo) bool { return p.GetState() == state })
}

func (r *rig) act(id, op string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := r.m.Act(ctx, &protocol.PluginAction{PluginId: id, Op: op})
	return err
}

// watcher follows a block as a front-end would.
type watcher struct {
	mu      sync.Mutex
	last    *protocol.BlockUpdate
	tree    *protocol.UiNode
	changed chan struct{}
}

func (r *rig) watch(block string) *watcher {
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	w := &watcher{changed: make(chan struct{}, 1)}
	go func() {
		_ = r.m.Watch(ctx, block, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			w.last = u
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				w.tree, _ = protocol.ApplyUi(w.tree, u.GetOps())
			}
			w.mu.Unlock()
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	return w
}

func (w *watcher) until(t *testing.T, what string, cond func(*protocol.BlockUpdate, *protocol.UiNode) bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		w.mu.Lock()
		last, tree := w.last, w.tree
		w.mu.Unlock()
		if last != nil && cond(last, tree) {
			return
		}
		select {
		case <-w.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("the block never showed %s; last %v", what, last)
		}
	}
}

func text(n *protocol.UiNode) string {
	if n == nil {
		return ""
	}
	var b bytes.Buffer
	b.WriteString(n.GetText())
	for _, c := range n.GetChildren() {
		b.WriteString(text(c))
	}
	return b.String()
}

// get fetches a URL of the server, for the fetcher's own tests.
func get(t *testing.T, f *registry.Fetcher, url string, max int64, within time.Duration) ([]byte, error) {
	t.Helper()
	return f.Get(context.Background(), url, max, within)
}
