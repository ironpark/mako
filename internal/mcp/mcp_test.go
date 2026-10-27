package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
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

func TestResolveWaitsForContext(t *testing.T) {
	r := &Resolver{Extensions: t.TempDir(), Work: t.TempDir()}
	r.once.Do(func() { r.turn = make(chan struct{}, 1) })
	r.turn <- struct{}{} // another Resolve is running
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	got := r.Resolve(ctx, map[string]Server{"a": {Command: &Command{Path: "/bin/a"}}})
	if len(got) != 1 {
		t.Fatalf("Resolve = %+v", got)
	}
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
