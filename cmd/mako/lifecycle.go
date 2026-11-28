package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ironpark/mako/internal/lifecycle"
	"github.com/ironpark/mako/internal/protocol"
)

// repoHooksNotice says, for a session in a repository whose
// .mako/hooks.toml the user does not trust as it reads, that its hooks do
// not run, and the command that trusts it on the session's node; "" for
// any other, or when the node cannot say.
func repoHooksNotice(ctx context.Context, ask repoTrustAsker, nodeID string, info *protocol.SessionInfo) string {
	if info.GetRepo() == "" {
		return ""
	}
	file := lifecycle.ProjectFileIn(info.GetRepo())
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	shown, err := ask(ctx, &protocol.RepoTrust{NodeId: nodeID, Path: file})
	if err != nil || len(shown.GetFiles()) != 1 || shown.GetFiles()[0].GetTrusted() {
		return ""
	}
	f := protocol.Printable(file)
	node := ""
	if nodeID != "" {
		node = "-node " + nodeID + " "
	}
	return fmt.Sprintf("[mako] the hooks of %s do not run: the file is not trusted as it reads\r\n"+
		"[mako] review it and trust it with: mako %srepo-trust allow %s\r\n", f, node, f)
}
