package main

import (
	"runtime"
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
	// A path with a space pastes as one word.
	if runtime.GOOS != "windows" {
		got = skippedHooksNotice("", &protocol.Worktree{SkippedHooks: "/my repo/.mako/worktree.toml"})
		if !strings.Contains(got, "trust it with: mako repo-trust allow '/my repo/.mako/worktree.toml'") {
			t.Fatalf("notice = %q", got)
		}
	}
}
