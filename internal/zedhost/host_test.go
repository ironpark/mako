package zedhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/zed"
)

func TestAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		manifest string
		wasm     []byte
		want     string
		ok       bool
	}{
		{"0.7.0", nil, "0.7.0", true},
		{"0.1.0", nil, "0.1.0", false},
		{"0.9.0", nil, "0.9.0", false},
		{"", append([]byte("junk"), append(apiSection, 0, 0, 0, 6, 0, 1)...), "0.6.1", true},
		{"", nil, "", false},
	} {
		v, err := parseAPIVersion(tc.manifest, tc.wasm)
		if (err == nil) != tc.ok || tc.want != "" && v.String() != tc.want {
			t.Errorf("parseAPIVersion(%q) = %v, %v; want %s, ok %v", tc.manifest, v, err, tc.want, tc.ok)
		}
	}
}

func TestPath(t *testing.T) {
	e := &Extension{work: "/w/ext"}
	for p, ok := range map[string]bool{
		"bin/server":       true,
		".":                true,
		"/w/ext/bin":       true,
		"../other":         false,
		"/etc/passwd":      false,
		"a/../../../x":     false,
		"node_modules/a/b": true,
	} {
		if _, err := e.path(p); (err == nil) != ok {
			t.Errorf("path(%q): err %v, want ok %v", p, err, ok)
		}
	}
}

// TestContextServer runs the extensions installed under
// $MAKO_ZED_EXTENSIONS that have context servers, as Zed's store hands
// them out, asking each for its configuration and its command.
func TestContextServer(t *testing.T) {
	dir := os.Getenv("MAKO_ZED_EXTENSIONS")
	if dir == "" {
		t.Skip("MAKO_ZED_EXTENSIONS names no directory of extensions")
	}
	exts, err := (&zed.Store{Dir: dir}).Installed()
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{WorkDir: t.TempDir(), Settings: func(category, key string) (string, error) {
		if category == "context_servers" {
			return `{"settings":{"github_personal_access_token":"x","brave_api_key":"x","database_url":"postgres://localhost/x"}}`, nil
		}
		return "", nil
	}}
	ran := 0
	for _, ext := range exts {
		if len(ext.ContextServers) == 0 {
			continue
		}
		ran++
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		start := time.Now()
		e, err := h.Load(ctx, ext)
		if err != nil {
			cancel()
			t.Errorf("%s: %v", ext.ID, err)
			continue
		}
		t.Logf("%s: loaded in %v", ext.ID, time.Since(start))
		for _, id := range ext.ContextServerIDs() {
			cfg, err := e.ContextServerConfiguration(ctx, id)
			if err != nil {
				t.Errorf("%s: configuration: %v", id, err)
			} else if cfg != nil {
				t.Logf("%s: default settings %s; instructions %.80q", id, strings.TrimSpace(cfg.DefaultSettings), cfg.InstallationInstructions)
			}
			cmd, err := e.ContextServerCommand(ctx, id)
			if err != nil {
				t.Errorf("%s: command: %v", id, err)
				continue
			}
			t.Logf("%s: %s %q env %q", id, cmd.Command, cmd.Args, cmd.Env)
		}
		e.Close()
		cancel()
	}
	if ran == 0 {
		t.Skip("no extension with a context server")
	}
}

// Compiled code comes back only exactly as it was written: one cut short,
// or written over in the middle, as by another process compiling the same
// extension, is not handed to wasmtime, which would run it and crash.
func TestCompiledCodeComesBackWholeOrNotAtAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.cwasm")
	code := bytes.Repeat([]byte("machine code "), 1000)
	if err := writeCompiled(path, code); err != nil {
		t.Fatal(err)
	}
	if got, ok := readCompiled(path); !ok || !bytes.Equal(got, code) {
		t.Fatal("what was written does not come back")
	}
	data, _ := os.ReadFile(path)
	for name, bad := range map[string][]byte{
		"cut short": data[:len(data)/2],
		"zeroed":    append(append(append([]byte(nil), data[:100]...), make([]byte, 100)...), data[200:]...),
		"empty":     nil,
	} {
		os.WriteFile(path, bad, 0o644)
		if _, ok := readCompiled(path); ok {
			t.Errorf("%s: read back", name)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".compiling-*")); len(matches) != 0 {
		t.Errorf("left behind %v", matches)
	}
}
