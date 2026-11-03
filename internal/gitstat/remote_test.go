package gitstat

import "testing"

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
