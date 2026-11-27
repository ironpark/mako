package agent

import "testing"

func TestIsLoopbackURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"http://127.0.0.1:9999/mcp", true},
		{"https://127.0.0.1:8443/mcp", true},
		{"http://127.0.1.1:8080/mcp", true},
		{"http://localhost:8080/mcp", true},
		{"http://LOCALHOST:8080/mcp", true},
		{"http://mcp.localhost:8080/mcp", true},
		{"http://localhost./mcp", true},
		{"http://[::1]:8080/mcp", true},
		{"ws://127.0.0.1:12345", true},
		{"wss://localhost:12345/ws", true},
		{"unix:///tmp/codex.sock", false},
		{"https://mcp.example.com/mcp", false},
		{"http://10.0.0.1:8080/mcp", false},
		{"http://127.0.0.1.example.com/mcp", false},
		{"file:///tmp/x", false},
		{"", false},
		{"://bad", false},
	}
	for _, tc := range tests {
		if got := IsLoopbackURL(tc.raw); got != tc.want {
			t.Errorf("IsLoopbackURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestHasLoopbackURL(t *testing.T) {
	if HasLoopbackURL(nil) {
		t.Fatal("nil servers should not report loopback")
	}
	if HasLoopbackURL([]Server{{ID: "stdio", Command: "/bin/mcp"}, {ID: "remote", URL: "https://mcp.example.com"}}) {
		t.Fatal("stdio and remote servers should not report loopback")
	}
	if !HasLoopbackURL([]Server{{ID: "mako", URL: "http://127.0.0.1:12345/mcp"}}) {
		t.Fatal("loopback HTTP server should report true")
	}
}

func TestMergeLocalProxyBypass(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{
			name:   "empty",
			values: nil,
			want:   "127.0.0.1,localhost,::1",
		},
		{
			name:   "existing corp host",
			values: []string{"internal.corp"},
			want:   "internal.corp,127.0.0.1,localhost,::1",
		},
		{
			name:   "merges upper and lower preserving order without duplicates",
			values: []string{"a.com,shared.corp", "shared.corp,b.com"},
			want:   "a.com,shared.corp,b.com,127.0.0.1,localhost,::1",
		},
		{
			name:   "already has all loopback entries",
			values: []string{"127.0.0.1,localhost,::1"},
			want:   "127.0.0.1,localhost,::1",
		},
		{
			name:   "trims whitespace and skips empty segments",
			values: []string{" internal.corp , , 127.0.0.1 , ", " localhost , INTERNAL.CORP "},
			want:   "internal.corp,127.0.0.1,localhost,::1",
		},
		{
			name:   "appends only missing loopback hosts",
			values: []string{"127.0.0.1,localhost"},
			want:   "127.0.0.1,localhost,::1",
		},
		{
			name:   "case-insensitive dedup of localhost",
			values: []string{"LOCALHOST,127.0.0.1"},
			want:   "LOCALHOST,127.0.0.1,::1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeLocalProxyBypass(tc.values...); got != tc.want {
				t.Fatalf("MergeLocalProxyBypass(%q) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

func TestEnsureLocalProxyBypassWith(t *testing.T) {
	mapLookup := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	t.Run("sets both NO_PROXY and no_proxy when empty", func(t *testing.T) {
		var l Launch
		EnsureLocalProxyBypassWith(&l, mapLookup(nil))
		want := "127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("preserves ambient NO_PROXY and writes merged list to both", func(t *testing.T) {
		var l Launch
		EnsureLocalProxyBypassWith(&l, mapLookup(map[string]string{"NO_PROXY": "internal.corp"}))
		want := "internal.corp,127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("preserves ambient lowercase no_proxy when NO_PROXY is absent", func(t *testing.T) {
		var l Launch
		EnsureLocalProxyBypassWith(&l, mapLookup(map[string]string{"no_proxy": "foo.bar"}))
		want := "foo.bar,127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("merges both ambient NO_PROXY and no_proxy when they differ", func(t *testing.T) {
		var l Launch
		EnsureLocalProxyBypassWith(&l, mapLookup(map[string]string{
			"NO_PROXY": "127.0.0.1,localhost,::1",
			"no_proxy": "only.corp",
		}))
		want := "127.0.0.1,localhost,::1,only.corp"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("explicit launch override wins over ambient", func(t *testing.T) {
		l := Launch{Env: map[string]string{"NO_PROXY": "custom.corp"}}
		EnsureLocalProxyBypassWith(&l, mapLookup(map[string]string{
			"NO_PROXY": "ambient.upper",
			"no_proxy": "ambient.lower",
		}))
		want := "custom.corp,127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("explicit empty launch override clears ambient entries while still adding loopback", func(t *testing.T) {
		l := Launch{Env: map[string]string{"NO_PROXY": ""}}
		EnsureLocalProxyBypassWith(&l, mapLookup(map[string]string{
			"NO_PROXY": "ambient.upper",
			"no_proxy": "ambient.lower",
		}))
		want := "127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("merges both explicit launch overrides without duplicating", func(t *testing.T) {
		l := Launch{Env: map[string]string{
			"NO_PROXY": "a.com,127.0.0.1",
			"no_proxy": "b.com,a.com",
		}}
		EnsureLocalProxyBypassWith(&l, mapLookup(nil))
		want := "a.com,127.0.0.1,b.com,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("idempotent across repeated calls", func(t *testing.T) {
		var l Launch
		lookup := mapLookup(map[string]string{"no_proxy": "corp.local"})
		EnsureLocalProxyBypassWith(&l, lookup)
		EnsureLocalProxyBypassWith(&l, lookup)
		want := "corp.local,127.0.0.1,localhost,::1"
		if l.Env["NO_PROXY"] != want || l.Env["no_proxy"] != want {
			t.Fatalf("env = %v, want %q in both", l.Env, want)
		}
	})

	t.Run("nil launch is a no-op", func(t *testing.T) {
		EnsureLocalProxyBypassWith(nil, mapLookup(nil))
	})
}
