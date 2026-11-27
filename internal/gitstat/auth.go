package gitstat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/safeexec"
	"github.com/zalando/go-keyring"
	"go.yaml.in/yaml/v3"
)

const (
	githubHTTPSOrigin = "https://github.com"
	githubHostName    = "github.com"
	defaultTokenUser  = "x-access-token"

	ghKeyringTimeout = 3 * time.Second
	ghKeyringHitTTL  = 5 * time.Minute
	ghKeyringMissTTL = 30 * time.Second
)

// HTTPSCredential holds HTTP(S) basic-auth or token credentials for a git
// remote origin. When Username is empty, "x-access-token" is used.
type HTTPSCredential struct {
	Username string
	Password string
}

// HTTPSAuthRequest is the JSON payload carried by protocol.GitOpAuth.
type HTTPSAuthRequest struct {
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
	Save     bool   `json:"save,omitempty"`
}

var (
	httpsCredsMu sync.RWMutex
	httpsCreds   = map[string]HTTPSCredential{}
)

// SetHTTPSCredential stores credentials in memory for the HTTP(S) origin of
// rawURL so subsequent git operations against that origin authenticate
// non-interactively for the lifetime of this process.
func SetHTTPSCredential(rawURL string, cred HTTPSCredential) error {
	origin := HTTPSOrigin(rawURL)
	if origin == "" {
		return errors.New("credentials require an HTTP or HTTPS repository URL")
	}
	user, pass, err := normalizeHTTPSCredential(cred.Username, cred.Password)
	if err != nil {
		return err
	}
	httpsCredsMu.Lock()
	httpsCreds[origin] = HTTPSCredential{Username: user, Password: pass}
	httpsCredsMu.Unlock()
	return nil
}

// GetHTTPSCredential returns the in-memory credentials for the HTTP(S) origin
// of rawURL, if any.
func GetHTTPSCredential(rawURL string) (HTTPSCredential, bool) {
	origin := HTTPSOrigin(rawURL)
	if origin == "" {
		return HTTPSCredential{}, false
	}
	httpsCredsMu.RLock()
	cred, ok := httpsCreds[origin]
	httpsCredsMu.RUnlock()
	return cred, ok
}

// ClearHTTPSCredential removes any in-memory credentials cached for the
// HTTP(S) origin of rawURL.
func ClearHTTPSCredential(rawURL string) {
	origin := HTTPSOrigin(rawURL)
	if origin == "" {
		return
	}
	httpsCredsMu.Lock()
	delete(httpsCreds, origin)
	httpsCredsMu.Unlock()
}

// ApproveHTTPSCredential caches credentials in memory and stores them in Git's
// configured credential helper via `git credential approve` without prompts.
func ApproveHTTPSCredential(ctx context.Context, rawURL, username, password string) error {
	scheme, host := parseHTTPOrigin(rawURL)
	if scheme == "" || host == "" {
		return errors.New("credentials require an HTTP or HTTPS repository URL")
	}
	user, pass, err := normalizeHTTPSCredential(username, password)
	if err != nil {
		return err
	}
	origin := scheme + "://" + host
	httpsCredsMu.Lock()
	httpsCreds[origin] = HTTPSCredential{Username: user, Password: pass}
	httpsCredsMu.Unlock()

	input := fmt.Sprintf("protocol=%s\nhost=%s\nusername=%s\npassword=%s\n\n", scheme, host, user, pass)
	cmd := exec.CommandContext(ctx, "git", "credential", "approve")
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	out, runErr := safeexec.CombinedOutput(cmd)
	if runErr != nil {
		return gitErr(runErr, string(out))
	}
	return nil
}

func normalizeHTTPSCredential(username, password string) (string, string, error) {
	if strings.ContainsAny(username, "\r\n\x00") || strings.ContainsAny(password, "\r\n\x00") {
		return "", "", errors.New("credentials must not contain newlines")
	}
	user := strings.TrimSpace(username)
	if password == "" {
		return "", "", errors.New("password or token is required")
	}
	if user == "" {
		user = defaultTokenUser
	}
	return user, password, nil
}

// IsHTTPSAuthError reports whether a git error message indicates a missing or
// rejected HTTP(S) credential.
func IsHTTPSAuthError(msg string) bool {
	s := strings.ToLower(strings.TrimSpace(msg))
	if s == "" {
		return false
	}
	for _, needle := range []string{
		"could not read username",
		"could not read password",
		"terminal prompts disabled",
		"authentication failed",
		"invalid username or password",
		"invalid username or token",
		"credentials rejected",
		"logon failed",
		"returned error: 401",
		"returned error: 403",
		"http 401",
		"http 403",
		"http basic: access denied",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// GitHub CLI / environment token fallback -------------------------------------

type cachedGHKeyring struct {
	mu    sync.Mutex
	user  string
	token string
	until time.Time
}

var (
	ghKeyringCache  cachedGHKeyring
	ghKeyringLookup = defaultGHKeyring
)

// ResetGitHubTokenCache clears the cached gh keyring token lookup.
func ResetGitHubTokenCache() {
	ghKeyringCache.mu.Lock()
	ghKeyringCache.user = ""
	ghKeyringCache.token = ""
	ghKeyringCache.until = time.Time{}
	ghKeyringCache.mu.Unlock()
}

// FindGitHubToken resolves a GitHub token from GH_TOKEN, GITHUB_TOKEN, gh's
// hosts.yml, or gh's system keyring entry. Returns "" when none is available.
func FindGitHubToken() string {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(env)); t != "" {
			return t
		}
	}
	user, tok := ghHosts()
	if tok != "" {
		return tok
	}
	return cachedGHKeyringToken(user)
}

func cachedGHKeyringToken(user string) string {
	ghKeyringCache.mu.Lock()
	if ghKeyringCache.user == user && time.Now().Before(ghKeyringCache.until) {
		tok := ghKeyringCache.token
		ghKeyringCache.mu.Unlock()
		return tok
	}
	ghKeyringCache.mu.Unlock()

	tok := ghKeyringLookup(user)
	ttl := ghKeyringMissTTL
	if tok != "" {
		ttl = ghKeyringHitTTL
	}
	ghKeyringCache.mu.Lock()
	ghKeyringCache.user = user
	ghKeyringCache.token = tok
	ghKeyringCache.until = time.Now().Add(ttl)
	ghKeyringCache.mu.Unlock()
	return tok
}

func ghConfigDir() string {
	if d := os.Getenv("GH_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gh")
	}
	if d := os.Getenv("AppData"); runtime.GOOS == "windows" && d != "" {
		return filepath.Join(d, "GitHub CLI")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gh")
}

type ghHostEntry struct {
	User  string `yaml:"user"`
	Token string `yaml:"oauth_token"`
	Users map[string]struct {
		Token string `yaml:"oauth_token"`
	} `yaml:"users"`
}

func ghHosts() (user, tok string) {
	dir := ghConfigDir()
	if dir == "" {
		return "", ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return "", ""
	}
	var hosts map[string]ghHostEntry
	if yaml.Unmarshal(b, &hosts) != nil {
		return "", ""
	}
	h := hosts[githubHostName]
	if h.Token != "" {
		return h.User, h.Token
	}
	return h.User, h.Users[h.User].Token
}

func defaultGHKeyring(user string) string {
	got := make(chan string, 1)
	go func() {
		users := []string{""}
		if user != "" {
			users = append(users, user)
		}
		for _, u := range users {
			if t, err := keyring.Get("gh:"+githubHostName, u); err == nil && t != "" {
				got <- t
				return
			}
		}
		got <- ""
	}()
	select {
	case t := <-got:
		return t
	case <-time.After(ghKeyringTimeout):
		return ""
	}
}

// Git env-based credential helper injection -----------------------------------

type gitConfigEntry struct {
	key   string
	value string
}

// appendHTTPSAuthEnv injects origin-scoped Git credential helpers via
// GIT_CONFIG_* environment variables so secrets never appear in command-line
// arguments or .git/config.
//
// Explicit or session-cached credentials override any existing helper for that
// origin (by emitting an empty credential.<origin>.helper first). Automatic
// GitHub token fallbacks are appended after user-configured helpers so the
// user's own credential helper always takes precedence.
func appendHTTPSAuthEnv(env []string, origins []string, explicit *HTTPSCredential) []string {
	if len(origins) == 0 {
		return env
	}
	seen := make(map[string]bool, len(origins))
	var entries []gitConfigEntry
	var extraEnv []string
	var idx int

	addHelper := func(origin, user, pass string, override bool) {
		if strings.ContainsAny(user, "\r\n\x00") || strings.ContainsAny(pass, "\r\n\x00") {
			return
		}
		if user == "" {
			user = defaultTokenUser
		}
		userVar := fmt.Sprintf("MAKO_GIT_USER_%d", idx)
		passVar := fmt.Sprintf("MAKO_GIT_PASS_%d", idx)
		idx++
		key := "credential." + origin + ".helper"
		if override {
			entries = append(entries, gitConfigEntry{key: key, value: ""})
		}
		helper := fmt.Sprintf(`!f() { test "$1" = get && printf 'username=%%s\npassword=%%s\n' "$%s" "$%s"; }; f`, userVar, passVar)
		entries = append(entries, gitConfigEntry{key: key, value: helper})
		extraEnv = append(extraEnv, userVar+"="+user, passVar+"="+pass)
	}

	for _, raw := range origins {
		origin := HTTPSOrigin(raw)
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true

		if explicit != nil && explicit.Password != "" {
			addHelper(origin, explicit.Username, explicit.Password, true)
			continue
		}
		if cached, ok := GetHTTPSCredential(origin); ok && cached.Password != "" {
			addHelper(origin, cached.Username, cached.Password, true)
			continue
		}
		if origin == githubHTTPSOrigin {
			if tok := FindGitHubToken(); tok != "" {
				addHelper(origin, defaultTokenUser, tok, false)
			}
		}
	}
	if len(entries) == 0 {
		return env
	}
	return appendGitConfigEnv(env, entries, extraEnv)
}

// appendGitConfigEnv appends GIT_CONFIG_KEY_N / GIT_CONFIG_VALUE_N entries to
// env while preserving any pre-existing GIT_CONFIG_COUNT entries.
func appendGitConfigEnv(env []string, entries []gitConfigEntry, extraEnv []string) []string {
	baseCount := 0
	out := make([]string, 0, len(env)+len(entries)*2+len(extraEnv)+1)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				baseCount = n
			}
			continue
		}
		out = append(out, kv)
	}
	for i, e := range entries {
		n := baseCount + i
		out = append(out,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, e.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, e.value),
		)
	}
	out = append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", baseCount+len(entries)))
	out = append(out, extraEnv...)
	return out
}

func isRemoteGitCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "fetch", "pull", "push", "ls-remote", "submodule":
		return true
	}
	return false
}

// repoHTTPSOrigins collects unique HTTP(S) origins from a repository's remotes
// and any HTTP(S) URLs passed directly as command arguments.
func repoHTTPSOrigins(root string, args []string) []string {
	var origins []string
	seen := map[string]bool{}
	add := func(u string) {
		if origin := HTTPSOrigin(u); origin != "" && !seen[origin] {
			seen[origin] = true
			origins = append(origins, origin)
		}
	}
	for _, r := range RemoteURLs(root) {
		add(r.URL)
	}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
			add(arg)
		}
	}
	return origins
}

// PrimaryHTTPSURL returns the repository's fetch URL if it uses HTTP(S), or
// the first HTTP(S) remote URL configured on root.
func PrimaryHTTPSURL(root string) string {
	if u := FetchURL(root); IsHTTPURL(u) {
		return u
	}
	for _, r := range RemoteURLs(root) {
		if IsHTTPURL(r.URL) {
			return r.URL
		}
	}
	return ""
}
