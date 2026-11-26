package gitstat

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsGitURLAndParseGitURL(t *testing.T) {
	valid := []struct {
		raw        string
		wantName   string
		wantOwner  string
		wantRemote string
	}{
		{"https://github.com/ironpark/mako.git", "mako", "github.com/ironpark", "github.com/ironpark/mako"},
		{"https://github.com/ironpark/mako", "mako", "github.com/ironpark", "github.com/ironpark/mako"},
		{"git@github.com:ironpark/mako.git", "mako", "github.com/ironpark", "github.com/ironpark/mako"},
		{"ssh://git@gitlab.com/group/sub/project.git", "project", "gitlab.com/sub", "gitlab.com/group/sub/project"},
		{"file:///tmp/demo-repo.git", "demo-repo", "", ""},
	}
	for _, tc := range valid {
		if !IsGitURL(tc.raw) {
			t.Errorf("IsGitURL(%q) = false, want true", tc.raw)
		}
		name, owner, remote, err := ParseGitURL(tc.raw)
		if err != nil {
			t.Errorf("ParseGitURL(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if name != tc.wantName || owner != tc.wantOwner || remote != tc.wantRemote {
			t.Errorf("ParseGitURL(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.raw, name, owner, remote, tc.wantName, tc.wantOwner, tc.wantRemote)
		}
	}

	invalid := []string{
		"",
		"/Users/ironpark/Projects/mako",
		"./mako",
		"~/Projects/mako",
		"--upload-pack=evil",
		"ext::sh -c evil",
		"https://github.com/ironpark/mako with spaces",
	}
	for _, raw := range invalid {
		if IsGitURL(raw) {
			t.Errorf("IsGitURL(%q) = true, want false", raw)
		}
		if _, _, _, err := ParseGitURL(raw); err == nil {
			t.Errorf("ParseGitURL(%q) expected error", raw)
		}
	}
}

func TestCloneLocalBareRepoAndReuse(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := filepath.Join(t.TempDir(), "sample.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	// Push one initial commit so the bare repo is non-empty.
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
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "README.md")
	runGit(seed, "commit", "-m", "init")
	runGit(seed, "remote", "add", "origin", bare)
	runGit(seed, "push", "-u", "origin", "HEAD")

	dest := filepath.Join(t.TempDir(), "projects", "sample")
	url := "file://" + bare
	res, err := CloneWithInfo(t.Context(), url, dest)
	if err != nil {
		t.Fatalf("CloneWithInfo: %v", err)
	}
	if res.AlreadyExisted {
		t.Fatalf("expected fresh clone, got AlreadyExisted=true")
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Fatalf("cloned file missing: %v", err)
	}

	// Second clone to same destination with same URL reuses existing repo cleanly.
	res2, err := CloneWithInfo(t.Context(), url, dest)
	if err != nil {
		t.Fatalf("second CloneWithInfo: %v", err)
	}
	if !res2.AlreadyExisted || res2.Path != res.Path {
		t.Fatalf("expected AlreadyExisted reuse at %s, got %+v", res.Path, res2)
	}

	// Non-empty non-matching directory must fail cleanly.
	other := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CloneWithInfo(t.Context(), url, other); err == nil {
		t.Fatal("expected error cloning into non-empty non-matching directory")
	}
}
