package forge

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A GitHub repository is found as gh finds it: the remote set-default
// chose, else upstream before origin, else one pushed to.
func TestGitHubOf(t *testing.T) {
	git := func(root string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	repo := func(t *testing.T) string {
		root := t.TempDir()
		git(root, "init", "-q")
		return root
	}
	root := repo(t)
	git(root, "remote", "add", "origin", "git@github.com:me/fork.git")
	git(root, "remote", "add", "upstream", "https://github.com/them/app.git")
	if g := gitHubOf(root); g == nil || g.full() != "them/app" {
		t.Errorf("upstream first: %v", g)
	}
	git(root, "config", "remote.origin.gh-resolved", "base")
	if g := gitHubOf(root); g == nil || g.full() != "me/fork" {
		t.Errorf("set-default: %v", g)
	}

	root = repo(t)
	git(root, "remote", "add", "origin", "https://git.example.com/me/app.git")
	git(root, "remote", "set-url", "--add", "--push", "origin", "git@github.com:me/app.git")
	if g := gitHubOf(root); g == nil || g.full() != "me/app" {
		t.Errorf("pushed to: %v", g)
	}
	if remote, u := remoteOf(root, "me/app"); remote != "" || u != "git@github.com:me/app.git" {
		t.Errorf("remoteOf = %q %q", remote, u)
	}
	if g := gitHubOf(repo(t)); g != nil {
		t.Errorf("no remote: %v", g)
	}
}

// A job's log is told to its steps by the time each began, and only a
// failed one's is written.
func TestWriteFailedSteps(t *testing.T) {
	at := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	j := job{Name: "build", Steps: []step{
		{Name: "Set up", Conclusion: "success", StartedAt: at("2026-01-01T00:00:00Z")},
		{Name: "Test", Conclusion: "failure", StartedAt: at("2026-01-01T00:00:05Z")},
		{Name: "Post", Conclusion: "success", StartedAt: at("2026-01-01T00:00:09Z")},
	}}
	log := "2026-01-01T00:00:01.5Z setting up\n" +
		"2026-01-01T00:00:05.1Z go test\n" +
		"  a line without a stamp\n" +
		"2026-01-01T00:00:08.9Z FAIL\n" +
		"2026-01-01T00:00:09.2Z cleaning up\n"
	var b strings.Builder
	writeFailedSteps(&b, j, log)
	want := "build\tTest\t2026-01-01T00:00:05.1Z go test\n" +
		"build\tTest\t  a line without a stamp\n" +
		"build\tTest\t2026-01-01T00:00:08.9Z FAIL\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}

	b.Reset()
	writeFailedSteps(&b, job{Name: "x"}, "one\ntwo")
	if b.String() != "x\t\tone\nx\t\ttwo\n" {
		t.Errorf("whole: %q", b.String())
	}
}

func TestPullRef(t *testing.T) {
	if n, ok := pullRef("refs/pull/12/head"); !ok || n != 12 {
		t.Errorf("%d %v", n, ok)
	}
	for _, r := range []string{"refs/heads/main", "refs/pull/x/head", "refs/pull/3/merge", ""} {
		if _, ok := pullRef(r); ok {
			t.Errorf("%q read as a pull request", r)
		}
	}
}
