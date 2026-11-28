package gitstat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
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

// Even when the checked-out branch itself tracks a PR ref, a tag, the local
// repo (remote = .), or a deleted/gone upstream, Status and Diff succeed.
func TestCheckedOutBranchWithNonBranchUpstreams(t *testing.T) {
	cases := []struct {
		name  string
		setup func(run func(...string) string)
	}{
		{
			name: "pr_ref",
			setup: func(run func(...string) string) {
				run("config", "branch.main.remote", "origin")
				run("config", "branch.main.merge", "refs/pull/7/head")
			},
		},
		{
			name: "tag_ref",
			setup: func(run func(...string) string) {
				run("config", "branch.main.remote", "origin")
				run("config", "branch.main.merge", "refs/tags/v1.0")
			},
		},
		{
			name: "local_dot_remote",
			setup: func(run func(...string) string) {
				run("config", "branch.main.remote", ".")
				run("config", "branch.main.merge", "refs/tags/v1.0")
			},
		},
		{
			name: "deleted_gone_upstream",
			setup: func(run func(...string) string) {
				run("config", "branch.main.remote", "origin")
				run("config", "branch.main.merge", "refs/pull/99/head")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, run := cliRepo(t)
			write(t, root, "a.txt", "before\n")
			run("add", "a.txt")
			run("commit", "-q", "-m", "init")
			run("remote", "add", "origin", "https://github.com/octocat/Hello-World.git")
			tc.setup(run)
			write(t, root, "a.txt", "after\n")

			st, err := Status(t.Context(), root)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if st.GetNotRepo() || st.GetBranch() != "main" || len(st.GetFiles()) != 1 {
				t.Fatalf("Status = %+v", st)
			}
			d, err := Diff(st.GetRoot(), "a.txt", "")
			if err != nil || len(d.GetLines()) == 0 {
				t.Fatalf("Diff = %+v, %v", d, err)
			}
		})
	}
}

// Diff requires the repository root (DetectDotGit: false) and rejects
// subdirectories with git.ErrRepositoryNotExists.
func TestDiffRejectsSubdirectoryRoot(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "sub/file.txt", "old\n")
	run("add", "sub/file.txt")
	run("commit", "-q", "-m", "init")
	write(t, root, "sub/file.txt", "new\n")

	sub := filepath.Join(root, "sub")
	if _, err := Diff(sub, "file.txt", ""); !errors.Is(err, git.ErrRepositoryNotExists) {
		t.Fatalf("Diff(sub) err = %v, want git.ErrRepositoryNotExists", err)
	}
	if _, err := Diff("", "sub/file.txt", ""); !errors.Is(err, git.ErrRepositoryNotExists) {
		t.Fatalf("Diff(empty) err = %v, want git.ErrRepositoryNotExists", err)
	}
	d, err := Diff(root, "sub/file.txt", "")
	if err != nil || len(d.GetLines()) == 0 {
		t.Fatalf("Diff(root) = %+v, %v", d, err)
	}
}

// Status accepts relative paths at the repo root and from inside subdirectories,
// always reporting an absolute repository root.
func TestStatusAcceptsRelativePaths(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "sub/a.txt", "one\n")
	run("add", "sub/a.txt")
	run("commit", "-q", "-m", "init")
	write(t, root, "sub/a.txt", "two\n")

	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	checkRoot := func(t *testing.T, dir string) {
		t.Helper()
		st, err := Status(t.Context(), dir)
		if err != nil {
			t.Fatalf("Status(%q): %v", dir, err)
		}
		if st.GetNotRepo() || len(st.GetFiles()) != 1 {
			t.Fatalf("Status(%q) = %+v", dir, st)
		}
		if !filepath.IsAbs(st.GetRoot()) {
			t.Fatalf("Status(%q).Root = %q, want absolute path", dir, st.GetRoot())
		}
		gotRoot, err := filepath.EvalSymlinks(st.GetRoot())
		if err != nil {
			t.Fatal(err)
		}
		if gotRoot != wantRoot {
			t.Fatalf("Status(%q).Root = %q (resolved %q), want %q", dir, st.GetRoot(), gotRoot, wantRoot)
		}
	}

	t.Chdir(root)
	checkRoot(t, ".")
	checkRoot(t, "sub")

	t.Chdir(filepath.Join(root, "sub"))
	checkRoot(t, ".")
}

// A linked worktree whose commondir points to a missing path returns
// git.ErrRepositoryIncomplete, matching go-git's PlainOpenWithOptions.
func TestBrokenWorktreeCommonDirReturnsIncomplete(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "side", linked)

	gitDir := GitDir(linked)
	if gitDir == "" {
		t.Fatal("GitDir(linked) was empty")
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../missing-common-dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Status(t.Context(), linked); !errors.Is(err, git.ErrRepositoryIncomplete) {
		t.Fatalf("Status(broken linked) err = %v, want git.ErrRepositoryIncomplete", err)
	}
	if _, err := Diff(linked, "a.txt", ""); !errors.Is(err, git.ErrRepositoryIncomplete) {
		t.Fatalf("Diff(broken linked) err = %v, want git.ErrRepositoryIncomplete", err)
	}
}

// A .git file with extra whitespace after "gitdir:" is still resolved.
func TestFindGitDirTrimsSpacesAfterGitdirPrefix(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "-b", "spaced", linked)

	gitDir := GitDir(linked)
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir:   "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Branch(linked); got != "spaced" {
		t.Fatalf("Branch(linked) = %q, want spaced", got)
	}
	write(t, linked, "a.txt", "b\n")
	st, err := Status(t.Context(), linked)
	if err != nil || st.GetNotRepo() || len(st.GetFiles()) != 1 {
		t.Fatalf("Status(linked) = %+v, %v", st, err)
	}
}

// Valid git configs that go-git rejects (such as branch.*.rebase = merges or
// negative remote.*.fetch refspecs like ^refs/heads/ignore) are tolerated
// while preserving valid fetch refspecs, URLs, and raw config reads.
func TestParseConfigToleratesRebaseMergesAndNegativeFetchRefspecs(t *testing.T) {
	root, run := cliRepo(t)
	write(t, root, "a.txt", "a\n")
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	run("remote", "add", "origin", "https://github.com/octocat/Hello-World.git")
	run("config", "--add", "remote.origin.fetch", "^refs/heads/ignore")
	run("config", "branch.main.rebase", "merges")
	run("config", "branch.other.rebase", "preserve")
	run("config", "branch.short.rebase", "m")
	run("config", "branch.bool.rebase", "yes")
	run("config", "branch.num.rebase", "1")
	write(t, root, "a.txt", "b\n")

	if u := FetchURL(root); u != "https://github.com/octocat/Hello-World.git" {
		t.Fatalf("FetchURL = %q", u)
	}
	if v := Config(root, "branch", "main", "rebase"); v != "merges" {
		t.Fatalf("Config(branch.main.rebase) = %q, want merges", v)
	}
	st, err := Status(t.Context(), root)
	if err != nil || st.GetNotRepo() || len(st.GetFiles()) != 1 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	d, err := Diff(st.GetRoot(), "a.txt", "")
	if err != nil || len(d.GetLines()) == 0 {
		t.Fatalf("Diff = %+v, %v", d, err)
	}

	// Verify parseConfigBytes retains valid fetch refspecs alongside negative ones,
	// and also handles a remote with only an unparseable fetch refspec.
	raw := []byte(`[remote "origin"]
	url = https://github.com/octocat/Hello-World.git
	fetch = +refs/heads/*:refs/remotes/origin/*
	fetch = ^refs/heads/ignore
[remote "negonly"]
	url = https://github.com/octocat/NegOnly.git
	fetch = ^refs/heads/wip
[branch "main"]
	remote = origin
	merge = refs/heads/main
	rebase = merges
`)
	cfg := parseConfigBytes(raw)
	if cfg == nil {
		t.Fatal("parseConfigBytes returned nil")
	}
	origin := cfg.Remotes["origin"]
	if origin == nil || len(origin.URLs) != 1 || origin.URLs[0] != "https://github.com/octocat/Hello-World.git" {
		t.Fatalf("origin remote = %+v", origin)
	}
	if len(origin.Fetch) != 1 || origin.Fetch[0].String() != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("origin.Fetch = %v, want single valid refspec", origin.Fetch)
	}
	negOnly := cfg.Remotes["negonly"]
	if negOnly == nil || len(negOnly.URLs) != 1 || len(negOnly.Fetch) != 0 {
		t.Fatalf("negonly remote = %+v", negOnly)
	}
	if cfg.Raw.Section("branch").Subsection("main").Option("rebase") != "merges" {
		t.Fatal("cfg.Raw did not preserve original rebase value")
	}

	// Verify tolerantStorage memoizes Config() so .git/config is parsed once per openRepo.
	r, err := openRepo(root, false)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	c1, err := r.Config()
	if err != nil {
		t.Fatalf("first Config: %v", err)
	}
	c2, err := r.Config()
	if err != nil {
		t.Fatalf("second Config: %v", err)
	}
	if c1 != c2 {
		t.Fatal("tolerantStorage.Config() did not return memoized config")
	}
}
