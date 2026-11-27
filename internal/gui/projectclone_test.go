package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

func TestSetProjectDirsPersistsAndRendersInGeneralSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MAKO_HOME", home)

	cfg := config.Default()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	m.setProjectDirs([]string{"~/Projects", "/tmp/work"})

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ProjectDirs) != 2 || loaded.ProjectDirs[0] != "~/Projects" || loaded.ProjectDirs[1] != "/tmp/work" {
		t.Fatalf("persisted ProjectDirs = %v", loaded.ProjectDirs)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return generalSettings(m) }, ggui.Sz(1100, 900))
	defer p.Close()
	if _, ok := p.Find(T("Add project location")); !ok {
		t.Fatalf("missing Add project location input:\n%s", p.Semantics().String())
	}
}

func TestCloneProjectDialogClonesBareRepoIntoDefaultProjectDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("MAKO_HOME", t.TempDir())

	bare := filepath.Join(t.TempDir(), "demo-app.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "init")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "README.md")
	runGit(seed, "commit", "-m", "init")
	runGit(seed, "remote", "add", "origin", bare)
	runGit(seed, "push", "-u", "origin", "HEAD")

	storeDir := filepath.Join(t.TempDir(), "my-projects")
	cfg := config.Default()
	cfg.NodeID = "local-node"
	cfg.ProjectDirs = []string{storeDir}

	m := newModel(cfg, "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local-node", Name: "This Mac"}})
	done := make(chan struct{}, 1)
	m.post = func(fn func()) {
		fn()
		if !ggui.Peek(m.Proj.Clone.Cloning) {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}

	m.openCloneProject()
	if got := ggui.Peek(m.Proj.Clone.BaseDir); got != storeDir {
		t.Fatalf("openCloneProject BaseDir = %q, want %q", got, storeDir)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return cloneProjectDialog(m) }, ggui.Sz(800, 700))
	defer p.Close()
	if _, ok := p.Find(T("Git URL")); !ok {
		t.Fatalf("missing Git URL field in clone dialog:\n%s", p.Semantics().String())
	}

	m.Proj.Clone.URL.Set("file://" + bare)
	m.submitCloneProject()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for clone to finish")
	}

	if errMsg := ggui.Peek(m.Proj.Clone.Err); errMsg != "" {
		t.Fatalf("clone failed with error: %s", errMsg)
	}
	if ggui.Peek(m.Proj.Clone.Open) {
		t.Fatal("clone dialog should close after successful clone")
	}
	wantCloned := filepath.Join(storeDir, "demo-app")
	if _, err := os.Stat(filepath.Join(wantCloned, "README.md")); err != nil {
		t.Fatalf("cloned repo missing at %s: %v", wantCloned, err)
	}
	if len(ggui.Peek(m.Prefs).Projects) != 1 {
		t.Fatalf("expected 1 project added to sidebar prefs, got %+v", ggui.Peek(m.Prefs).Projects)
	}
}

func TestCloneProjectDialogHTTPSAuthAutoExpandAndClear(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MAKO_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))

	serveRoot := t.TempDir()
	bare := filepath.Join(serveRoot, "private-app.git")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "init")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "README.md")
	runGit(seed, "commit", "-m", "init")
	runGit(seed, "remote", "add", "origin", bare)
	runGit(seed, "push", "-u", "origin", "HEAD")
	runGit(bare, "update-server-info")

	const wantToken = "gui-clone-secret-77"
	fs := http.FileServer(http.Dir(serveRoot))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "x-access-token" || p != wantToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		fs.ServeHTTP(w, r)
	}))
	defer srv.Close()

	repoURL := srv.URL + "/private-app.git"
	t.Cleanup(func() { gitstat.ClearHTTPSCredential(repoURL) })

	storeDir := filepath.Join(t.TempDir(), "projects")
	cfg := config.Default()
	cfg.NodeID = "local-node"
	cfg.ProjectDirs = []string{storeDir}

	m := newModel(cfg, "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local-node", Name: "This Mac"}})
	done := make(chan struct{}, 2)
	m.post = func(fn func()) {
		fn()
		if !ggui.Peek(m.Proj.Clone.Cloning) {
			done <- struct{}{}
		}
	}

	m.openCloneProject()
	m.Proj.Clone.URL.Set(repoURL)
	m.Proj.Clone.SaveCreds.Set(false)

	p := ggui.ProbeBuilder(func() ggui.Widget { return cloneProjectDialog(m) }, ggui.Sz(800, 760))
	defer p.Close()
	if _, ok := p.Find(T("HTTPS authentication")); !ok {
		t.Fatalf("missing HTTPS authentication toggle for HTTP(S) URL:\n%s", p.Semantics().String())
	}

	// 1. Submitting without credentials fails and auto-expands HTTPS authentication.
	m.submitCloneProject()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for unauthenticated clone failure")
	}
	if !ggui.Peek(m.Proj.Clone.AuthOpen) {
		t.Fatalf("expected AuthOpen=true after HTTPS auth failure, err=%q", ggui.Peek(m.Proj.Clone.Err))
	}
	if !ggui.Peek(m.Proj.Clone.Open) {
		t.Fatal("dialog should stay open after auth failure")
	}

	// 2. Entering token succeeds, clears secrets, closes dialog, and keeps .git/config clean.
	m.Proj.Clone.Password.Set(wantToken)
	m.submitCloneProject()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for authenticated clone")
	}
	if errMsg := ggui.Peek(m.Proj.Clone.Err); errMsg != "" {
		t.Fatalf("authenticated clone failed: %s", errMsg)
	}
	if ggui.Peek(m.Proj.Clone.Open) {
		t.Fatal("clone dialog should close on success")
	}
	if got := ggui.Peek(m.Proj.Clone.Password); got != "" {
		t.Fatalf("password was not cleared after clone: %q", got)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(storeDir, "private-app", ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfgBytes), wantToken) {
		t.Fatalf(".git/config leaked token:\n%s", cfgBytes)
	}
}
