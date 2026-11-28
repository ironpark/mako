package main

import (
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// mako run says when its worktree was made without the repository's
// setup_cmd, and the command that trusts it, on the node it ran on.
func TestSkippedHooksNotice(t *testing.T) {
	if got := skippedHooksNotice("", &protocol.Worktree{Path: "/w"}); got != "" {
		t.Fatalf("notice without a skip: %q", got)
	}
	got := skippedHooksNotice("n1", &protocol.Worktree{SkippedHooks: "/repo/.mako/worktree.toml"})
	if !strings.Contains(got, "did not run") || !strings.Contains(got, "mako -node n1 repo-trust allow /repo/.mako/worktree.toml") {
		t.Fatalf("notice = %q", got)
	}
}
