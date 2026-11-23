package zedhost

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// Compiled code is kept where the guest, which can write its work
// directory, cannot plant any.
func TestCompiledCodeIsOutOfTheGuestsReach(t *testing.T) {
	h := &Host{WorkDir: t.TempDir()}
	dir, err := h.compiledDir("ext")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("%s not made: %v", dir, err)
	}
	if rel, err := filepath.Rel(h.WorkDir, dir); err == nil && filepath.IsLocal(rel) {
		t.Fatalf("%s is inside the work directories %s", dir, h.WorkDir)
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

func TestExtensionSymlinkEscapeBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test requires unix permissions/symlinks")
	}
	base := t.TempDir()
	work := filepath.Join(base, "work")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(work, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secretPkg := filepath.Join(outside, "package.json")
	if err := os.WriteFile(secretPkg, []byte(`{"version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(outside, "tool")
	if err := os.WriteFile(targetFile, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "node_modules", "evilpkg")); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pwned"))
	}))
	defer srv.Close()

	e := &Extension{
		work:       work,
		httpClient: newSSRFClient(5*time.Second, strings.TrimPrefix(srv.URL, "http://")),
	}
	res, err := e.makeFileExecutable([]Val{String("escape/tool")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("makeFileExecutable through symlink unexpectedly succeeded: %+v, %v", res, err)
	}
	fi, err := os.Stat(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 != 0 {
		t.Fatal("symlink escape modified permissions outside work dir")
	}

	res, err = e.npmInstalledVersion([]Val{String("evilpkg")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("npmInstalledVersion through symlink unexpectedly succeeded: %+v, %v", res, err)
	}

	res, err = e.downloadFile([]Val{String(srv.URL + "/x"), String("escape/out"), String("uncompressed")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("downloadFile through symlink unexpectedly succeeded: %+v, %v", res, err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "out")); !os.IsNotExist(statErr) {
		t.Fatalf("downloadFile escaped through symlink: %v", statErr)
	}
}

func TestSSRFBlockedAndFetchTruncation(t *testing.T) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret-metadata"))
	}))
	defer forbidden.Close()

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("ok"))
		case "/redir":
			http.Redirect(w, r, forbidden.URL+"/meta", http.StatusFound)
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("a", 128)))
		}
	}))
	defer allowed.Close()

	// Allow ONLY `allowed` server; `forbidden` (loopback) and metadata IP must be blocked!
	e := &Extension{
		work:       t.TempDir(),
		httpClient: newSSRFClient(5*time.Second, strings.TrimPrefix(allowed.URL, "http://")),
	}
	reqVal := func(u string) Val {
		return Record(
			Field{"method", String("GET")},
			Field{"url", String(u)},
			Field{"headers", List()},
			Field{"body", None()},
			Field{"redirect-policy", Enum("no-follow")},
		)
	}
	reqFollow := func(u string) Val {
		return Record(
			Field{"method", String("GET")},
			Field{"url", String(u)},
			Field{"headers", List()},
			Field{"body", None()},
			Field{"redirect-policy", Enum("follow-all")},
		)
	}

	// Allowed server succeeds.
	res, err := e.fetch([]Val{reqVal(allowed.URL + "/ok")})
	if err != nil || len(res) != 1 || !res[0].Ok {
		t.Fatalf("allowed fetch failed: %+v, %v", res, err)
	}
	// Direct loopback to un-allowlisted server is blocked.
	res, err = e.fetch([]Val{reqVal(forbidden.URL + "/meta")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("forbidden loopback fetch unexpectedly succeeded: %+v, %v", res, err)
	}
	// 302 redirect from allowlisted server to un-allowlisted loopback is blocked.
	res, err = e.fetch([]Val{reqFollow(allowed.URL + "/redir")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("SSRF 302 redirect unexpectedly succeeded: %+v, %v", res, err)
	}
	// Non-http(s) scheme is rejected.
	res, err = e.fetch([]Val{reqVal("file:///etc/passwd")})
	if err != nil || len(res) != 1 || res[0].Ok {
		t.Fatalf("file:// fetch unexpectedly succeeded: %+v, %v", res, err)
	}
	// Default sharedAPIClient blocks 127.0.0.1 and 169.254.169.254.
	eDefault := &Extension{work: t.TempDir()}
	for _, u := range []string{forbidden.URL + "/meta", "http://[::1]:1/x", "http://169.254.169.254/latest/meta-data/"} {
		res, err = eDefault.fetch([]Val{reqVal(u)})
		if err != nil || len(res) != 1 || res[0].Ok {
			t.Fatalf("default SSRF client did not block %s: %+v, %v", u, res, err)
		}
	}
	// Truncation beyond limit returns explicit error instead of silent truncation.
	res, err = e.fetchLimit([]Val{reqVal(allowed.URL + "/big")}, 64)
	if err != nil || len(res) != 1 || res[0].Ok || !strings.Contains(res[0].Payload.Str, "exceeds") {
		t.Fatalf("expected explicit truncation error, got %+v, %v", res, err)
	}
}

func TestNpmInstallAndLatestVersionPassDoubleDash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake npm requires unix")
	}
	binDir := t.TempDir()
	work := t.TempDir()
	argsLog := filepath.Join(binDir, "args.txt")
	fakeNpm := filepath.Join(binDir, "npm")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsLog + "\"\nprintf '\"1.2.3\"'\n"
	if err := os.WriteFile(fakeNpm, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := &Extension{work: work}
	res, err := e.npmInstall([]Val{String("-malicious-flag"), String("1.0.0")})
	if err != nil || len(res) != 1 || !res[0].Ok {
		t.Fatalf("npmInstall: %+v, %v", res, err)
	}
	gotInstall, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotInstall), "\n--\n-malicious-flag@1.0.0\n") {
		t.Fatalf("npmInstall missing '--' before spec:\n%s", string(gotInstall))
	}

	res, err = e.npmLatestVersion([]Val{String("-malicious-pkg")})
	if err != nil || len(res) != 1 || !res[0].Ok || res[0].Payload.Str != "1.2.3" {
		t.Fatalf("npmLatestVersion: %+v, %v", res, err)
	}
	gotView, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotView), "view\n--json\n--\n-malicious-pkg\nversion\n") {
		t.Fatalf("npmLatestVersion missing '--' before pkg:\n%s", string(gotView))
	}
}
