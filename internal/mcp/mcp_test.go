package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/zed"
)

func TestResolveConfigured(t *testing.T) {
	off := false
	r := &Resolver{Extensions: t.TempDir(), Work: t.TempDir()}
	got := r.Resolve(t.Context(), map[string]Server{
		"b":   {Command: &Command{Path: "/bin/b", Args: []string{"x"}}},
		"a":   {Command: &Command{Path: "/bin/a"}},
		"off": {Enabled: &off, Command: &Command{Path: "/bin/off"}},
		"ext": {}, // no extension has it
	})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || got[1].Args[0] != "x" {
		t.Fatalf("Resolve = %+v", got)
	}
}

func TestSettingsFor(t *testing.T) {
	s, _ := settingsFor("context_servers", Server{Settings: json.RawMessage(`{"k":1}`)})
	if s != `{"settings":{"k":1}}` {
		t.Errorf("settings %s", s)
	}
	if s, _ := settingsFor("context_servers", Server{}); s != `{"settings":null}` {
		t.Errorf("settings %s", s)
	}
	if s, _ := settingsFor("lsp", Server{}); s != "" {
		t.Errorf("lsp settings %q", s)
	}
}

// TestResolveExtensions resolves the servers of the extensions under
// $MAKO_ZED_EXTENSIONS, twice, the second from the cache.
func TestResolveExtensions(t *testing.T) {
	dir := os.Getenv("MAKO_ZED_EXTENSIONS")
	if dir == "" {
		t.Skip("MAKO_ZED_EXTENSIONS names no directory of extensions")
	}
	r := &Resolver{Extensions: dir, Work: t.TempDir()}
	servers := map[string]Server{
		"mcp-server-brave-search": {Settings: json.RawMessage(`{"brave_api_key":"k"}`)},
		"postgres-context-server": {Settings: json.RawMessage(`{"database_url":"postgres://x"}`)},
	}
	first := r.Resolve(t.Context(), servers)
	if len(first) == 0 {
		t.Fatal("nothing resolved")
	}
	for _, l := range first {
		t.Logf("%s: %s %q %v", l.ID, l.Command, l.Args, l.Env)
	}
	second := r.Resolve(t.Context(), servers)
	if len(second) != len(first) {
		t.Fatalf("second Resolve gave %d, first %d", len(second), len(first))
	}
}

func writeFakeExtension(t *testing.T, extsDir, extID, serverID string) {
	t.Helper()
	dir := filepath.Join(extsDir, extID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id = \"" + extID + "\"\nname = \"" + extID + "\"\nversion = \"1.0.0\"\n\n[context_servers." + serverID + "]\n"
	if err := os.WriteFile(filepath.Join(dir, "extension.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWaitsForContext(t *testing.T) {
	extsDir := t.TempDir()
	writeFakeExtension(t, extsDir, "ext-a", "srv-a")
	r := &Resolver{Extensions: extsDir, Work: t.TempDir()}

	// Hold server "srv-a"'s turn lock to simulate an in-flight resolution.
	turnA := r.serverTurn("srv-a")
	turnA <- struct{}{}
	defer func() { <-turnA }()

	// Static server "b" resolves immediately without blocking on "srv-a" when not requested.
	ctxStatic, cancelStatic := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelStatic()
	gotStatic := r.ResolveOnly(ctxStatic, map[string]Server{"b": {Command: &Command{Path: "/bin/b"}}}, []string{"b"})
	if len(gotStatic) != 1 || gotStatic[0].ID != "b" {
		t.Fatalf("static ResolveOnly while srv-a busy = %+v", gotStatic)
	}

	// Uncached "srv-a" waits on its turn and gives up when ctx expires.
	ctxA, cancelA := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelA()
	got := r.Resolve(ctxA, map[string]Server{
		"srv-a": {},
		"b":     {Command: &Command{Path: "/bin/b"}},
	})
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("Resolve with busy srv-a = %+v, want only b", got)
	}
}

func TestResolveDeterministicCanceledCtx(t *testing.T) {
	extsDir := t.TempDir()
	writeFakeExtension(t, extsDir, "ext-a", "srv-a")
	writeFakeExtension(t, extsDir, "ext-b", "srv-b")
	workDir := t.TempDir()
	r := &Resolver{Extensions: extsDir, Work: workDir}

	// Pre-populate cache for srv-b only (relative command passes runnable()).
	exts, err := r.findExts()
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.cache = map[string]cached{
		"srv-b": {
			key:    cacheKey(Server{}, exts["srv-b"]),
			launch: Launch{ID: "srv-b", Command: "cached-b", Args: []string{"--ok"}, Env: map[string]string{"K": "V"}},
			at:     time.Now(),
		},
	}
	r.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for i := range 50 {
		got := r.Resolve(ctx, map[string]Server{"srv-a": {}, "srv-b": {}})
		if len(got) != 1 || got[0].ID != "srv-b" || got[0].Command != "cached-b" {
			t.Fatalf("iter %d: got %+v, want only cached srv-b", i, got)
		}
	}
	// Ensure uncached ext-a was never attempted against WorkDir.
	if entries, err := os.ReadDir(workDir); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected workDir activity on canceled ctx: %v, %v", entries, err)
	}
}

func (r *Resolver) findExts() (map[string]zed.Extension, error) {
	extA, err := Find(r.Extensions, "srv-a")
	if err != nil {
		return nil, err
	}
	extB, err := Find(r.Extensions, "srv-b")
	if err != nil {
		return nil, err
	}
	return map[string]zed.Extension{"srv-a": extA, "srv-b": extB}, nil
}

func TestResolveClonesArgsAndEnv(t *testing.T) {
	extsDir := t.TempDir()
	writeFakeExtension(t, extsDir, "ext-c", "srv-c")
	r := &Resolver{Extensions: extsDir, Work: t.TempDir()}

	origArgs := []string{"--flag", "v1"}
	origEnv := map[string]string{"TOKEN": "orig"}
	servers := map[string]Server{
		"static": {Command: &Command{Path: "/bin/static", Args: origArgs, Env: origEnv}},
	}
	got := r.Resolve(t.Context(), servers)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	got[0].Args[0] = "mutated"
	got[0].Env["TOKEN"] = "hacked"
	if !slices.Equal(origArgs, []string{"--flag", "v1"}) || origEnv["TOKEN"] != "orig" {
		t.Fatalf("caller Command mutated: args=%v env=%v", origArgs, origEnv)
	}

	// Verify cached extension entries are also cloned on every Resolve.
	extC, err := Find(extsDir, "srv-c")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.cache = map[string]cached{
		"srv-c": {
			key:    cacheKey(Server{}, extC),
			launch: Launch{ID: "srv-c", Command: "cached-c", Args: []string{"--port", "1"}, Env: map[string]string{"A": "1"}},
			at:     time.Now(),
		},
	}
	r.mu.Unlock()

	got1 := r.Resolve(t.Context(), map[string]Server{"srv-c": {}})
	if len(got1) != 1 {
		t.Fatalf("got1 %+v", got1)
	}
	got1[0].Args[1] = "999"
	got1[0].Env["A"] = "2"

	got2 := r.Resolve(t.Context(), map[string]Server{"srv-c": {}})
	if len(got2) != 1 || !slices.Equal(got2[0].Args, []string{"--port", "1"}) || got2[0].Env["A"] != "1" {
		t.Fatalf("cache entry mutated across Resolve calls: %+v", got2)
	}
}

func TestResolvePerServerConcurrency(t *testing.T) {
	extsDir := t.TempDir()
	writeFakeExtension(t, extsDir, "ext-a", "srv-a")
	writeFakeExtension(t, extsDir, "ext-b", "srv-b")
	r := &Resolver{Extensions: extsDir, Work: t.TempDir()}

	exts, err := r.findExts()
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.cache = map[string]cached{
		"srv-b": {
			key:    cacheKey(Server{}, exts["srv-b"]),
			launch: Launch{ID: "srv-b", Command: "cached-b", Args: []string{"1"}, Env: map[string]string{"K": "V"}},
			at:     time.Now(),
		},
	}
	r.mu.Unlock()

	// Hold srv-a's turn while running concurrent ResolveOnly calls for srv-b and static servers.
	turnA := r.serverTurn("srv-a")
	turnA <- struct{}{}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := r.ResolveOnly(t.Context(), map[string]Server{
				"srv-b":  {},
				"static": {Command: &Command{Path: "/bin/s", Args: []string{"x"}, Env: map[string]string{"E": "1"}}},
			}, []string{"srv-b", "static"})
			if len(got) != 2 {
				t.Errorf("concurrent ResolveOnly = %+v", got)
				return
			}
			got[0].Args[0] = "mut"
			got[0].Env["K"] = "mut"
			got[1].Args[0] = "mut"
			got[1].Env["E"] = "mut"
		}()
	}
	wg.Wait()
	<-turnA
}

func TestParseSettings(t *testing.T) {
	for in, want := range map[string]string{
		"":         "",
		`{"a": 1}`: `{"a":1}`,
		"{\n  /// key\n  \"k\": \"v//x\", // note\n}": `{"k":"v//x"}`,
		"{ /* c */ \"l\": [1, 2,], }":                 `{"l":[1,2]}`,
		"{\"a\": 1, // last\n}":                       `{"a":1}`,
	} {
		got, err := ParseSettings(in)
		if err != nil || string(got) != want {
			t.Errorf("ParseSettings(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	if _, err := ParseSettings("{oops"); err == nil {
		t.Error("ParseSettings took {oops")
	}
}

func TestResolveOnly(t *testing.T) {
	off := false
	r := &Resolver{Extensions: t.TempDir(), Work: t.TempDir()}
	servers := map[string]Server{
		"a":   {Command: &Command{Path: "/bin/a"}},
		"b":   {Command: &Command{Path: "/bin/b"}},
		"off": {Enabled: &off, Command: &Command{Path: "/bin/off"}},
	}
	got := r.ResolveOnly(t.Context(), servers, []string{"b", "off"})
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "off" {
		t.Fatalf("ResolveOnly = %+v", got)
	}
	if got := r.ResolveOnly(t.Context(), servers, []string{}); len(got) != 0 {
		t.Fatalf("ResolveOnly(none) = %+v", got)
	}
}
