package sshremote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestParseSSHConfigAndInclude(t *testing.T) {
	dir := t.TempDir()
	incDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(incDir, 0o700); err != nil {
		t.Fatal(err)
	}
	incBody := `
Host gpu-box
  HostName 10.20.30.40
  User ubuntu
  Port 2222
  IdentityFile ~/.ssh/gpu_ed25519
  ProxyJump bastion
`
	if err := os.WriteFile(filepath.Join(incDir, "work.conf"), []byte(incBody), 0o600); err != nil {
		t.Fatal(err)
	}
	mainBody := `
Include config.d/*.conf

Host *
  ServerAliveInterval 60

Host *.internal !bad
  User default

Host dev-mac
  HostName 192.168.1.50
  User lee
`
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte(mainBody), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := DiscoverConfigHostsFrom(cfgPath)
	if len(hosts) != 2 {
		t.Fatalf("got %d hosts (%+v), want 2", len(hosts), hosts)
	}
	if hosts[0].Alias != "gpu-box" || hosts[0].HostName != "10.20.30.40" || hosts[0].User != "ubuntu" || hosts[0].Port != 2222 || hosts[0].ProxyJump != "bastion" {
		t.Fatalf("hosts[0] = %+v", hosts[0])
	}
	if hosts[0].Summary() != "ubuntu@10.20.30.40:2222" {
		t.Fatalf("Summary = %q", hosts[0].Summary())
	}
	if hosts[1].Alias != "dev-mac" || hosts[1].User != "lee" || hosts[1].HostName != "192.168.1.50" {
		t.Fatalf("hosts[1] = %+v", hosts[1])
	}
}

func TestBaseArgsAndShellQuote(t *testing.T) {
	h := config.SSHHost{
		ID:           "h1",
		Name:         "GPU",
		Host:         "10.0.0.5",
		User:         "dev",
		Port:         2200,
		IdentityFile: "/tmp/id_ed25519",
		ProxyJump:    "jump.example.com",
		ExtraArgs:    []string{"-o", "ForwardAgent=yes"},
	}
	args, err := BaseArgs(h, Options{ControlPath: "/tmp/c.sock"})
	if err != nil {
		t.Fatalf("BaseArgs: %v", err)
	}
	if !slices.Contains(args, "-p") || !slices.Contains(args, "2200") {
		t.Fatalf("missing port flag in %v", args)
	}
	if !slices.Contains(args, "-J") || !slices.Contains(args, "jump.example.com") {
		t.Fatalf("missing ProxyJump in %v", args)
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "dev@10.0.0.5" {
		t.Fatalf("unexpected target tail: %v", args[len(args)-2:])
	}

	// ShellQuote must neutralize single quotes and metacharacters.
	if got := ShellQuote("a'b $(rm -rf /)"); got != `'a'\''b $(rm -rf /)'` {
		t.Fatalf("ShellQuote = %q", got)
	}
	if got := ShellQuote(""); got != "''" {
		t.Fatalf("ShellQuote empty = %q", got)
	}

	// Reject option injection.
	if _, err := BaseArgs(config.SSHHost{Host: "-oProxyCommand=id"}, Options{}); err == nil {
		t.Fatal("expected error for host starting with '-'")
	}
}

func TestParseProbeOutput(t *testing.T) {
	raw := `
OS=Linux
ARCH=x86_64
HOSTNAME=gpu-01
HOME=/home/dev
SHELL=/bin/zsh
MAKOD=/home/dev/.local/bin/makod
AGENTS=claude,codex,agy
`
	res := ParseProbeOutput(raw)
	if res.OS != "linux" || res.Arch != "amd64" || res.Hostname != "gpu-01" || res.Home != "/home/dev" || res.Shell != "/bin/zsh" || res.MakodPath != "/home/dev/.local/bin/makod" {
		t.Fatalf("unexpected probe result: %+v", res)
	}
	if !slices.Equal(res.Agents, []string{"claude", "codex", "agy"}) {
		t.Fatalf("agents = %v", res.Agents)
	}
	infos := AgentInfos(res.Agents)
	if len(infos) != 4 || infos[3].GetName() != "shell" {
		t.Fatalf("AgentInfos = %+v", infos)
	}
}

func TestParseGitStatusAndUnifiedDiff(t *testing.T) {
	rawStatus := `ROOT=/repo
===STATUS===
# branch.oid 123456
# branch.head main
# branch.upstream origin/main
# branch.ab +2 -1
1 .M N... 100644 100644 100644 aaa bbb src/app.go
? newfile.txt
2 R. N... 100644 100644 100644 aaa bbb R100 dst.go	src.go
===NUMSTAT===
10	3	src/app.go
-	-	img.png
===BRANCHES===
main
feat/ssh
===REMOTES===
origin/HEAD
origin/main
===STASH===
stash@{0}	1700000000	WIP on main
`
	ch := ParseGitStatusOutput(rawStatus)
	if ch.GetRoot() != "/repo" || ch.GetBranch() != "main" || ch.GetUpstream() != "origin/main" || ch.GetAhead() != 2 || ch.GetBehind() != 1 {
		t.Fatalf("unexpected header: %+v", ch)
	}
	if len(ch.GetFiles()) != 3 {
		t.Fatalf("files len = %d, want 3", len(ch.GetFiles()))
	}
	if len(ch.GetBranches()) != 2 || len(ch.GetRemoteBranches()) != 1 || len(ch.GetStashes()) != 1 {
		t.Fatalf("branches=%v remotes=%v stashes=%v", ch.GetBranches(), ch.GetRemoteBranches(), ch.GetStashes())
	}

	rawDiff := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 keep
-old
+new
 tail
`
	d := ParseUnifiedDiff("a.txt", rawDiff)
	if d.GetBinary() || len(d.GetLines()) != 5 {
		t.Fatalf("diff lines = %d, want 5", len(d.GetLines()))
	}
	if d.Lines[0].Op != "@" || d.Lines[1].Op != " " || d.Lines[2].Op != "-" || d.Lines[2].Old != 2 || d.Lines[3].Op != "+" || d.Lines[3].New != 2 {
		t.Fatalf("unexpected diff lines: %+v", d.Lines)
	}
}

// TestDirectOpsViaFakeSSH uses a tiny fake `ssh` script that executes the
// remote command locally via `/bin/sh -c`, exercising the real Probe, ListDir,
// ReadFile, WriteFile, FileOp, StageFile, Search, and Git* code paths end to end.
func TestDirectOpsViaFakeSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake ssh test")
	}
	fakeBin := filepath.Join(t.TempDir(), "fake-ssh")
	// Fake ssh skips all flags until "--", skips the target argument, and runs
	// the remote command string through /bin/sh -c.
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
  if [ "$1" = "--" ]; then
    shift
    shift # target
    break
  fi
  shift
done
exec /bin/sh -c "$*"
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldBin := SSHBinary
	SSHBinary = fakeBin
	t.Cleanup(func() { SSHBinary = oldBin })

	ctx := context.Background()
	host := config.SSHHost{ID: "test1", Name: "TestHost", Host: "fake.local"}

	// 1. Probe
	probe, err := Probe(ctx, host, "")
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if probe.OS == "" || probe.Home == "" {
		t.Fatalf("incomplete probe: %+v", probe)
	}

	// 2. File ops: WriteFile, ReadFile, ListDir, FileOp, StageFile
	work := t.TempDir()
	filePath := filepath.Join(work, "hello.txt")
	if _, err := WriteFile(ctx, host, "", probe.Home, &protocol.WriteFile{
		Path: filePath,
		Data: []byte("hello remote ssh\nline 2\n"),
	}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	rf, err := ReadFile(ctx, host, "", probe.Home, &protocol.ReadFile{Path: filePath})
	if err != nil || string(rf.GetData()) != "hello remote ssh\nline 2\n" {
		t.Fatalf("ReadFile = (%q, %v)", string(rf.GetData()), err)
	}
	dir, err := ListDir(ctx, host, "", probe.Home, work)
	if err != nil || len(dir.GetEntries()) != 1 || dir.GetEntries()[0].GetName() != "hello.txt" {
		t.Fatalf("ListDir = (%+v, %v)", dir, err)
	}
	if _, err := FileOp(ctx, host, "", probe.Home, &protocol.FileOp{
		Op:   protocol.FileMkdir,
		Path: filepath.Join(work, "sub"),
	}); err != nil {
		t.Fatalf("FileOp mkdir: %v", err)
	}
	if _, err := FileOp(ctx, host, "", probe.Home, &protocol.FileOp{
		Op:   protocol.FileRename,
		Path: filePath,
		To:   filepath.Join(work, "sub", "renamed.txt"),
	}); err != nil {
		t.Fatalf("FileOp rename: %v", err)
	}

	// 3. Search
	sr, err := Search(ctx, host, "", probe.Home, &protocol.Search{Root: work, Files: true})
	if err != nil || !slices.Contains(sr.GetFiles(), "sub/renamed.txt") {
		t.Fatalf("Search files = (%+v, %v)", sr, err)
	}
	sr, err = Search(ctx, host, "", probe.Home, &protocol.Search{Root: work, Query: "remote ssh"})
	if err != nil || len(sr.GetMatches()) != 1 || !strings.Contains(sr.GetMatches()[0].GetText(), "remote ssh") {
		t.Fatalf("Search query = (%+v, %v)", sr, err)
	}

	// 4. Git ops on a temp git repo
	if _, err := exec.LookPath("git"); err == nil {
		repo := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			c := exec.Command("git", append([]string{"-C", repo}, args...)...)
			c.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
				"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			)
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run("add", "a.txt")
		run("commit", "-m", "initial commit")
		run("remote", "add", "origin", "https://github.com/ironpark/mako.git")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\nchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		head := GitHead(ctx, host, "", probe.Home, repo)
		if head.Branch != "main" || head.Remote != "github.com/ironpark/mako" || head.Owner != "github.com/ironpark" {
			t.Fatalf("GitHead = %+v", head)
		}
		st, err := GitStatus(ctx, host, "", probe.Home, &protocol.GitStatus{Path: repo})
		if err != nil || st.GetNotRepo() || len(st.GetFiles()) != 1 || st.GetFiles()[0].GetPath() != "a.txt" {
			t.Fatalf("GitStatus = (%+v, %v)", st, err)
		}
		diff, err := GitDiff(ctx, host, "", probe.Home, &protocol.GitDiff{Root: repo, Path: "a.txt"})
		if err != nil || len(diff.GetLines()) == 0 {
			t.Fatalf("GitDiff = (%+v, %v)", diff, err)
		}
		hist, err := GitLog(ctx, host, "", probe.Home, &protocol.GitLog{Root: repo, Max: 10})
		if err != nil || len(hist.GetCommits()) != 1 || hist.GetCommits()[0].GetSubject() != "initial commit" {
			t.Fatalf("GitLog = (%+v, %v)", hist, err)
		}
		ci, err := GitShow(ctx, host, "", probe.Home, &protocol.GitShow{Root: repo, Hash: hist.GetCommits()[0].GetHash()})
		if err != nil || ci.GetCommit().GetSubject() != "initial commit" || len(ci.GetFiles()) != 1 {
			t.Fatalf("GitShow = (%+v, %v)", ci, err)
		}
	}
}

func TestFindConfigHost(t *testing.T) {
	hosts := []ConfigHost{{Alias: "GPU-Box"}, {Alias: "gpu-box"}, {Alias: "dev-mac"}}
	for _, tc := range []struct{ name, want string }{
		{"gpu-box", "gpu-box"},   // an exact match wins over an earlier case-folded one
		{"GPU-BOX", "GPU-Box"},   // case-folded, like OpenSSH
		{" dev-mac ", "dev-mac"}, // trimmed
		{"other", ""},
		{"", ""},
	} {
		got, ok := FindConfigHost(hosts, tc.name)
		if ok != (tc.want != "") || got.Alias != tc.want {
			t.Errorf("FindConfigHost(%q) = (%q, %v), want %q", tc.name, got.Alias, ok, tc.want)
		}
	}
}

// A control path with a space or a % reaches ssh whole: ssh -G, which
// only parses its options, reports the path unchanged.
func TestBaseArgsControlPathWithSpace(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh client")
	}
	ctrl := "/Users/me/Library/Application Support/mako/run/ssh-c-5%d.sock"
	args, err := BaseArgs(config.SSHHost{Host: "example.com"}, Options{ControlPath: ctrl})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ssh, append([]string{"-G", "-F", "/dev/null"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh -G %v: %v\n%s", args, err, out)
	}
	if !slices.Contains(strings.Split(string(out), "\n"), "controlpath "+ctrl) {
		t.Fatalf("ssh -G does not report controlpath %q:\n%s", ctrl, out)
	}
}

func TestSanitizeWorktreeSlugAndLeadingDashGuards(t *testing.T) {
	if got := sanitizeWorktreeSlug(`feat/$(id);rm -rf /`); strings.ContainsAny(got, `$();" `) {
		t.Fatalf("unsafe worktree slug: %q", got)
	}
	if got := sanitizeWorktreeSlug("../evil/branch"); strings.Contains(got, "..") || strings.HasPrefix(got, "-") {
		t.Fatalf("traversal or dash slug: %q", got)
	}
	if got := sanitizeWorktreeSlug("///"); got != "worktree" {
		t.Fatalf("empty slug fallback = %q, want worktree", got)
	}
	if err := rejectLeadingDash("branch", "--upload-pack=evil"); err == nil {
		t.Fatal("expected leading-dash rejection")
	}
	if err := rejectLeadingDash("branch", "feat/ok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitRunCommandCheckoutIgnoresOtherWorktrees(t *testing.T) {
	cmd, err := gitRunCommand("/srv/repo", &protocol.GitRun{
		Op:     protocol.GitOpCheckout,
		Branch: "feat/wt",
	})
	if err != nil {
		t.Fatalf("gitRunCommand checkout: %v", err)
	}
	want := ShellJoin([]string{"git", "-C", "/srv/repo", "checkout", "--ignore-other-worktrees", "feat/wt", "--"})
	if !strings.Contains(cmd, want) {
		t.Fatalf("checkout cmd = %q; want substring %q", cmd, want)
	}
	if _, err := gitRunCommand("/srv/repo", &protocol.GitRun{
		Op:     protocol.GitOpCheckout,
		Branch: "--orphan",
	}); err == nil {
		t.Fatal("expected leading-dash branch to be rejected")
	}
}
