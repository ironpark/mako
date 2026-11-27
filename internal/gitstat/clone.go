package gitstat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ironpark/mako/internal/safeexec"
)

var scpLikePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[^\s]+$`)

// IsGitURL reports whether raw looks like a remote git repository URL
// (https://, http://, ssh://, git://, file://, or SCP-style user@host:path).
// Unsafe forms such as leading "-" options or "ext::" remote helpers return false.
func IsGitURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "ext::") {
		return false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(lower, scheme) {
			return len(s) > len(scheme)
		}
	}
	return scpLikePattern.MatchString(s)
}

// ParseGitURL validates a git URL and extracts its default repository folder
// name, owner ("host/owner"), and normalized remote ("host/owner/repo").
func ParseGitURL(raw string) (repoName, owner, remote string, err error) {
	s := strings.TrimSpace(raw)
	if !IsGitURL(s) {
		return "", "", "", fmt.Errorf("%q is not a valid git URL", s)
	}
	lower := strings.ToLower(s)
	var rawPath string
	if strings.HasPrefix(lower, "file://") {
		rawPath = strings.TrimPrefix(s, s[:len("file://")])
	} else {
		_, repoPath := RepoPath(s)
		rawPath = repoPath
		owner = urlOwner(s)
		remote = urlRemote(s)
	}
	trimmed := strings.TrimSuffix(strings.TrimRight(rawPath, "/\\"), ".git")
	if idx := strings.LastIndexAny(trimmed, "/\\"); idx >= 0 {
		repoName = trimmed[idx+1:]
	} else {
		repoName = trimmed
	}
	repoName = strings.TrimSpace(repoName)
	if repoName == "" || repoName == "." || repoName == ".." || strings.ContainsAny(repoName, `/\`) {
		return "", "", "", fmt.Errorf("cannot determine repository folder name from %q", s)
	}
	return repoName, owner, remote, nil
}

// CloneResult describes the outcome of CloneWithInfo.
type CloneResult struct {
	Path           string
	Owner          string
	Remote         string
	AlreadyExisted bool
}

// CloneOptions configures optional behavior for CloneWithOptions.
type CloneOptions struct {
	// Credential overrides HTTPS authentication for the clone without writing
	// secrets into the URL or .git/config.
	Credential *HTTPSCredential
	// SaveCredential persists Credential into Git's credential store after a
	// successful clone (via `git credential approve`).
	SaveCredential bool
}

// Clone clones rawURL into destDir, returning the canonical repository path.
// If destDir already exists and is a git checkout of the same remote, Clone
// reuses it rather than failing.
func Clone(ctx context.Context, rawURL, destDir string) (string, error) {
	res, err := CloneWithInfo(ctx, rawURL, destDir)
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// CloneWithInfo clones rawURL into destDir and returns canonical path and
// remote metadata.
func CloneWithInfo(ctx context.Context, rawURL, destDir string) (CloneResult, error) {
	return CloneWithOptions(ctx, rawURL, destDir, CloneOptions{})
}

// CloneWithOptions clones rawURL into destDir using optional HTTPS
// credentials and returns canonical path and remote metadata.
func CloneWithOptions(ctx context.Context, rawURL, destDir string, opts CloneOptions) (CloneResult, error) {
	rawURL = strings.TrimSpace(rawURL)
	_, parsedOwner, parsedRemote, err := ParseGitURL(rawURL)
	if err != nil {
		return CloneResult{}, err
	}
	destDir = filepath.Clean(strings.TrimSpace(destDir))
	if destDir == "" || !filepath.IsAbs(destDir) {
		return CloneResult{}, errors.New("destination path must be absolute")
	}

	if fi, err := os.Stat(destDir); err == nil {
		if !fi.IsDir() {
			return CloneResult{}, fmt.Errorf("destination %s already exists and is not a directory", destDir)
		}
		entries, readErr := os.ReadDir(destDir)
		if readErr != nil {
			return CloneResult{}, readErr
		}
		if len(entries) > 0 {
			// Reuse if it's already a clone of the same repository.
			_, repo, owner, remote := Head(destDir)
			sameRemote := parsedRemote != "" && remote != "" && strings.EqualFold(parsedRemote, remote)
			sameURL := repo != "" && strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(FetchURL(destDir)), ".git"), strings.TrimSuffix(rawURL, ".git"))
			if sameRemote || sameURL {
				realPath := destDir
				if r, err := filepath.EvalSymlinks(destDir); err == nil {
					realPath = r
				}
				return CloneResult{
					Path:           filepath.Clean(realPath),
					Owner:          firstNonEmpty(owner, parsedOwner),
					Remote:         firstNonEmpty(remote, parsedRemote),
					AlreadyExisted: true,
				}, nil
			}
			return CloneResult{}, fmt.Errorf("destination %s already exists and is not empty", destDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return CloneResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return CloneResult{}, err
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--", rawURL, destDir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_EDITOR=true")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	if origin := HTTPSOrigin(rawURL); origin != "" {
		cmd.Env = appendHTTPSAuthEnv(cmd.Env, []string{origin}, opts.Credential)
	}
	out, runErr := safeexec.CombinedOutput(cmd)
	if runErr != nil {
		return CloneResult{}, gitErr(runErr, string(out))
	}

	if opts.Credential != nil && opts.Credential.Password != "" && IsHTTPURL(rawURL) {
		if opts.SaveCredential {
			_ = ApproveHTTPSCredential(ctx, rawURL, opts.Credential.Username, opts.Credential.Password)
		} else {
			_ = SetHTTPSCredential(rawURL, *opts.Credential)
		}
	}

	realPath := destDir
	if r, err := filepath.EvalSymlinks(destDir); err == nil {
		realPath = r
	}
	_, _, owner, remote := Head(realPath)
	return CloneResult{
		Path:   filepath.Clean(realPath),
		Owner:  firstNonEmpty(owner, parsedOwner),
		Remote: firstNonEmpty(remote, parsedRemote),
	}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
