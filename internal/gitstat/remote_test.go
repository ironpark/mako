package gitstat

import (
	"os"
	"path/filepath"
	"testing"
)

// A branch gh pr checkout set to pull a fork's pull request, from
// refs/pull/n/head, leaves the remotes read, and its upstream too.
func TestConfigReadsAPullRequestsUpstream(t *testing.T) {
	root, run := cliRepo(t)
	run("remote", "add", "origin", "https://github.com/octocat/Hello-World.git")
	run("config", "branch.fork.remote", "origin")
	run("config", "branch.fork.merge", "refs/pull/7/head")
	if u := FetchURL(root); u != "https://github.com/octocat/Hello-World.git" {
		t.Errorf("FetchURL = %q", u)
	}
	if urls := RemoteURLs(root); len(urls) != 1 || urls[0].Remote != "origin" {
		t.Errorf("RemoteURLs = %v", urls)
	}
	if m := Config(root, "branch", "fork", "merge"); m != "refs/pull/7/head" {
		t.Errorf("merge = %q", m)
	}
	if m := Config(root, "branch", "none", "merge"); m != "" {
		t.Errorf("an unset key = %q", m)
	}
}

// A branch whose upstream is not a branch, a pull request's or one set by
// its bare name, leaves the repository's status and diffs read, in its
// main work tree and in a linked one, where go-git refuses the config.
func TestStatusReadsARepositoryWithAPullRequestsUpstream(t *testing.T) {
	root, run := cliRepo(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "a")
	run("remote", "add", "origin", "https://github.com/octocat/Hello-World.git")
	run("config", "branch.fork.remote", "origin")
	run("config", "branch.fork.merge", "refs/pull/7/head")
	run("config", "branch.bare.merge", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "side", linked)
	for _, dir := range []string{root, linked} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Status(t.Context(), dir)
		if err != nil {
			t.Fatalf("Status of %s: %v", dir, err)
		}
		if st.GetNotRepo() || len(st.GetFiles()) != 1 {
			t.Fatalf("Status of %s: %v", dir, st)
		}
		d, err := Diff(st.GetRoot(), "a.txt", "")
		if err != nil || len(d.GetLines()) == 0 {
			t.Fatalf("Diff in %s: %v, %v", dir, d, err)
		}
	}
}

func TestConfigRemote(t *testing.T) {
	owner, remote := ConfigRemote([]byte("[remote \"origin\"]\n\turl = git@github.com:IronPark/Mako.git\n"))
	if owner != "github.com/IronPark" || remote != "github.com/ironpark/mako" {
		t.Fatalf("ConfigRemote = (%q, %q), want (github.com/IronPark, github.com/ironpark/mako)", owner, remote)
	}
	if o, r := ConfigRemote(nil); o != "" || r != "" {
		t.Fatalf("ConfigRemote(nil) = (%q, %q)", o, r)
	}
}
