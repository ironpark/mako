package gitstat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestHTTPSHostAndOrigin(t *testing.T) {
	cases := []struct {
		raw        string
		wantHost   string
		wantOrigin string
	}{
		{"https://github.com/ironpark/mako.git", "github.com", "https://github.com"},
		{"HTTPS://GitHub.COM:443/ironpark/mako.git", "github.com", "https://github.com"},
		{"https://git.example.com:3000/team/app.git", "git.example.com:3000", "https://git.example.com:3000"},
		{"https://user:secret@git.example.com:8443/team/app.git", "git.example.com:8443", "https://git.example.com:8443"},
		{"http://127.0.0.1:8080/team/app.git", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://127.0.0.1:80/team/app.git", "127.0.0.1", "http://127.0.0.1"},
		{"https://[::1]:3000/team/app.git", "[::1]:3000", "https://[::1]:3000"},
		{"https://[::1]/team/app.git", "[::1]", "https://[::1]"},
		{"git@github.com:ironpark/mako.git", "", ""},
		{"ssh://git@git.example.com:2222/team/app.git", "", ""},
		{"file:///tmp/repo.git", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := HTTPSHost(tc.raw); got != tc.wantHost {
			t.Errorf("HTTPSHost(%q) = %q, want %q", tc.raw, got, tc.wantHost)
		}
		if got := HTTPSOrigin(tc.raw); got != tc.wantOrigin {
			t.Errorf("HTTPSOrigin(%q) = %q, want %q", tc.raw, got, tc.wantOrigin)
		}
		if got := IsHTTPURL(tc.raw); got != (tc.wantOrigin != "") {
			t.Errorf("IsHTTPURL(%q) = %v, want %v", tc.raw, got, tc.wantOrigin != "")
		}
	}
}

func TestIsHTTPSAuthError(t *testing.T) {
	yes := []string{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"fatal: Authentication failed for 'https://git.example.com:3000/team/app.git/'",
		"error: The requested URL returned error: 401",
		"fatal: unable to access 'https://git.example.com/a/b.git/': The requested URL returned error: 403",
		"remote: HTTP Basic: Access denied. Invalid username or token.",
	}
	for _, msg := range yes {
		if !IsHTTPSAuthError(msg) {
			t.Errorf("IsHTTPSAuthError(%q) = false, want true", msg)
		}
	}
	no := []string{
		"",
		"error: failed to push some refs (non-fast-forward)",
		"CONFLICT (content): Merge conflict in README.md",
		"fatal: not a git repository",
	}
	for _, msg := range no {
		if IsHTTPSAuthError(msg) {
			t.Errorf("IsHTTPSAuthError(%q) = true, want false", msg)
		}
	}
}

func TestFindGitHubTokenFromEnvAndHosts(t *testing.T) {
	origKeyring := ghKeyringLookup
	ghKeyringLookup = func(string) string { return "" }
	t.Cleanup(func() {
		ghKeyringLookup = origKeyring
		ResetGitHubTokenCache()
	})
	ResetGitHubTokenCache()

	cfgDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", cfgDir)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	if got := FindGitHubToken(); got != "" {
		t.Fatalf("empty config FindGitHubToken = %q, want empty", got)
	}

	hostsYAML := "github.com:\n  user: alice\n  users:\n    alice:\n      oauth_token: gho_from_hosts\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "hosts.yml"), []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindGitHubToken(); got != "gho_from_hosts" {
		t.Fatalf("hosts.yml FindGitHubToken = %q, want gho_from_hosts", got)
	}

	t.Setenv("GITHUB_TOKEN", "ghp_from_github_token")
	if got := FindGitHubToken(); got != "ghp_from_github_token" {
		t.Fatalf("GITHUB_TOKEN FindGitHubToken = %q, want ghp_from_github_token", got)
	}

	t.Setenv("GH_TOKEN", "ghp_from_gh_token")
	if got := FindGitHubToken(); got != "ghp_from_gh_token" {
		t.Fatalf("GH_TOKEN FindGitHubToken = %q, want ghp_from_gh_token", got)
	}
}

func TestAppendHTTPSAuthEnvCredentialFill(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	origKeyring := ghKeyringLookup
	ghKeyringLookup = func(string) string { return "" }
	t.Cleanup(func() {
		ghKeyringLookup = origKeyring
		ResetGitHubTokenCache()
	})
	ResetGitHubTokenCache()

	// Use an isolated HOME and empty global config so host credential helpers
	// never interfere with this test.
	home := t.TempDir()
	baseEnv := append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)

	// 1. Explicit credential with shell metacharacters, preserving existing GIT_CONFIG_COUNT.
	secret := `p@$$w'o"rd%s\123`
	envWithExisting := append(append([]string{}, baseEnv...),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=Preserved User",
	)
	env := appendHTTPSAuthEnv(envWithExisting, []string{"https://git.example.com:3000/team/app.git"}, &HTTPSCredential{
		Username: "alice",
		Password: secret,
	})

	cmd := exec.Command("git", "credential", "fill")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("protocol=https\nhost=git.example.com:3000\n\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git credential fill: %v: %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "username=alice\n") || !strings.Contains(got, "password="+secret+"\n") {
		t.Fatalf("unexpected credential fill output:\n%s", got)
	}

	// Verify existing GIT_CONFIG_* entry was preserved.
	cfgCmd := exec.Command("git", "config", "--get", "user.name")
	cfgCmd.Env = env
	cfgOut, err := cfgCmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(cfgOut)) != "Preserved User" {
		t.Fatalf("preserved user.name = %q (err=%v), want Preserved User", string(cfgOut), err)
	}

	// Verify credentials are strictly scoped to the target origin.
	otherCmd := exec.Command("git", "credential", "fill")
	otherCmd.Env = env
	otherCmd.Stdin = strings.NewReader("protocol=https\nhost=other.example.com\n\n")
	otherOut, _ := otherCmd.CombinedOutput()
	if strings.Contains(string(otherOut), secret) {
		t.Fatalf("credential leaked to unrelated host:\n%s", otherOut)
	}

	// 2. Automatic GitHub token fallback lets user's configured helper win first,
	//    and kicks in when no helper supplies credentials.
	t.Setenv("GH_TOKEN", "ghp_fallback_token")
	ghEnv := appendHTTPSAuthEnv(baseEnv, []string{"https://github.com/ironpark/mako.git"}, nil)

	ghCmd := exec.Command("git", "credential", "fill")
	ghCmd.Env = ghEnv
	ghCmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	ghOut, err := ghCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("github fallback fill: %v: %s", err, ghOut)
	}
	if !strings.Contains(string(ghOut), "username=x-access-token\n") || !strings.Contains(string(ghOut), "password=ghp_fallback_token\n") {
		t.Fatalf("unexpected github fallback output:\n%s", ghOut)
	}

	// Now configure a user helper in global gitconfig and verify it takes precedence over fallback.
	if err := os.WriteFile(filepath.Join(home, "gitconfig"), []byte(
		"[credential \"https://github.com\"]\n\thelper = \"!f() { test \\\"$1\\\" = get && printf 'username=custom\\\\npassword=user_helper_tok\\\\n'; }; f\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	ghCmd2 := exec.Command("git", "credential", "fill")
	ghCmd2.Env = ghEnv
	ghCmd2.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	ghOut2, err := ghCmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("github helper precedence fill: %v: %s", err, ghOut2)
	}
	if !strings.Contains(string(ghOut2), "username=custom\n") || !strings.Contains(string(ghOut2), "password=user_helper_tok\n") {
		t.Fatalf("expected user helper to win over fallback, got:\n%s", ghOut2)
	}
}

func TestCloneAndFetchWithHTTPBasicAuth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Isolate git global config so local credential helpers don't interfere.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))

	// Create a bare repo prepared for dumb HTTP transport.
	serveRoot := t.TempDir()
	bare := filepath.Join(serveRoot, "demo.git")
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
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello https\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "README.md")
	runGit(seed, "commit", "-m", "init")
	runGit(seed, "remote", "add", "origin", bare)
	runGit(seed, "push", "-u", "origin", "HEAD")
	runGit(bare, "update-server-info")

	const wantToken = "tok-super-secret-42"
	fs := http.FileServer(http.Dir(serveRoot))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != defaultTokenUser || p != wantToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		fs.ServeHTTP(w, r)
	}))
	defer srv.Close()

	repoURL := srv.URL + "/demo.git"
	t.Cleanup(func() { ClearHTTPSCredential(repoURL) })

	// 1. Clone without credentials must fail with an HTTPS auth error.
	destFail := filepath.Join(t.TempDir(), "fail-clone")
	if _, err := CloneWithOptions(t.Context(), repoURL, destFail, CloneOptions{}); err == nil {
		t.Fatal("expected unauthenticated HTTP clone to fail")
	} else if !IsHTTPSAuthError(err.Error()) {
		t.Fatalf("expected IsHTTPSAuthError=true, got: %v", err)
	}

	// 2. Clone with explicit token succeeds and never writes secret into .git/config.
	destOK := filepath.Join(t.TempDir(), "ok-clone")
	res, err := CloneWithOptions(t.Context(), repoURL, destOK, CloneOptions{
		Credential:     &HTTPSCredential{Password: wantToken},
		SaveCredential: false,
	})
	if err != nil {
		t.Fatalf("authenticated CloneWithOptions failed: %v", err)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(res.Path, ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfgBytes), wantToken) {
		t.Fatalf(".git/config must not contain plaintext credentials:\n%s", cfgBytes)
	}

	// 3. Subsequent git fetch reuses the in-memory cached credential seamlessly.
	if _, err := Run(t.Context(), &protocol.GitRun{Root: res.Path, Op: protocol.GitOpFetch}); err != nil {
		t.Fatalf("cached HTTPS fetch failed: %v", err)
	}

	// 4. Clearing the cached credential causes fetch to fail with auth error again.
	ClearHTTPSCredential(repoURL)
	if _, err := Run(t.Context(), &protocol.GitRun{Root: res.Path, Op: protocol.GitOpFetch}); err == nil {
		t.Fatal("expected fetch after ClearHTTPSCredential to fail")
	} else if !IsHTTPSAuthError(err.Error()) {
		t.Fatalf("expected auth error after clearing cache, got: %v", err)
	}
}
