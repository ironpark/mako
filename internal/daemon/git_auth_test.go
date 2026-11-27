package daemon

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

func TestDaemonGitAuthLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	d := &Daemon{}

	// 1. Explicit URL mode (used before remote-node HTTPS clones).
	url := "https://git.example.com:3000/team/app.git"
	t.Cleanup(func() { gitstat.ClearHTTPSCredential(url) })

	payload, err := json.Marshal(gitstat.HTTPSAuthRequest{
		URL:      url,
		Username: "bob",
		Password: "tok-explicit",
		Save:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.gitRunLocal(t.Context(), &protocol.GitRun{
		Op:      protocol.GitOpAuth,
		Message: string(payload),
	})
	if err != nil {
		t.Fatalf("gitRunLocal(GitOpAuth explicit URL): %v", err)
	}
	if res.GetOutput() != "" {
		t.Fatalf("GitOpAuth output must be empty, got %q", res.GetOutput())
	}
	cred, ok := gitstat.GetHTTPSCredential(url)
	if !ok || cred.Username != "bob" || cred.Password != "tok-explicit" {
		t.Fatalf("cached credential = (%+v, %v), want bob/tok-explicit", cred, ok)
	}

	// 2. Repo-root mode (used by mako-git without URL).
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://code.example.org:8443/org/repo.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	t.Cleanup(func() { gitstat.ClearHTTPSCredential("https://code.example.org:8443") })

	payload2, err := json.Marshal(gitstat.HTTPSAuthRequest{
		Password: "tok-repo-only",
		Save:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.gitRunLocal(t.Context(), &protocol.GitRun{
		Root:    root,
		Op:      protocol.GitOpAuth,
		Message: string(payload2),
	}); err != nil {
		t.Fatalf("gitRunLocal(GitOpAuth repo root): %v", err)
	}
	cred2, ok := gitstat.GetHTTPSCredential("https://code.example.org:8443/org/repo.git")
	if !ok || cred2.Username != "x-access-token" || cred2.Password != "tok-repo-only" {
		t.Fatalf("repo cached credential = (%+v, %v), want x-access-token/tok-repo-only", cred2, ok)
	}

	// 3. SSH-only repo must be rejected cleanly.
	sshRoot := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:org/repo.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", sshRoot}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if _, err := d.gitRunLocal(t.Context(), &protocol.GitRun{
		Root:    sshRoot,
		Op:      protocol.GitOpAuth,
		Message: string(payload2),
	}); err == nil {
		t.Fatal("expected error for SSH-only repo")
	}
}
