package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// mako run says when the repository of its session has hooks the user
// does not trust, and the command that trusts them on the node it ran on.
func TestRepoHooksNotice(t *testing.T) {
	var asked []*protocol.RepoTrust
	answer := func(info *protocol.RepoTrustInfo, err error) repoTrustAsker {
		return func(_ context.Context, r *protocol.RepoTrust) (*protocol.RepoTrustInfo, error) {
			asked = append(asked, r)
			return info, err
		}
	}
	session := &protocol.SessionInfo{Repo: "/src/app", Project: "/src/app/sub"}
	untrusted := &protocol.RepoTrustInfo{Files: []*protocol.TrustedFile{{Root: "/src/app", Path: ".mako/hooks.toml"}}}

	got := repoHooksNotice(t.Context(), answer(untrusted, nil), "n1", session)
	if !strings.Contains(got, "do not run") || !strings.Contains(got, "mako -node n1 repo-trust allow /src/app/.mako/hooks.toml") {
		t.Fatalf("notice = %q", got)
	}
	if r := asked[len(asked)-1]; r.GetPath() != "/src/app/.mako/hooks.toml" || r.GetNodeId() != "n1" || r.GetOp() != protocol.RepoTrustShow {
		t.Fatalf("asked %+v", r)
	}

	trusted := &protocol.RepoTrustInfo{Files: []*protocol.TrustedFile{{Root: "/src/app", Path: ".mako/hooks.toml", Trusted: true}}}
	for name, ask := range map[string]repoTrustAsker{
		"trusted":  answer(trusted, nil),
		"no file":  answer(nil, errors.New("stat /src/app/.mako/hooks.toml: no such file or directory")),
		"no reply": answer(&protocol.RepoTrustInfo{}, nil),
	} {
		if got := repoHooksNotice(t.Context(), ask, "", session); got != "" {
			t.Errorf("%s: notice = %q", name, got)
		}
	}
	n := len(asked)
	if got := repoHooksNotice(t.Context(), answer(untrusted, nil), "", &protocol.SessionInfo{Project: "/tmp"}); got != "" || len(asked) != n {
		t.Fatalf("a session outside a repository: %q, asked %d", got, len(asked)-n)
	}
	if got := repoHooksNotice(t.Context(), answer(untrusted, nil), "", &protocol.SessionInfo{Agent: "shell", Repo: "/src/app", Project: "/src/app"}); got != "" || len(asked) != n {
		t.Fatalf("a shell's session, which runs no hooks: %q, asked %d", got, len(asked)-n)
	}
}
