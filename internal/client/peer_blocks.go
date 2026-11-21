package client

import (
	"context"

	"github.com/ironpark/mako/internal/protocol"
)

// The blocks of another node, which lets this one's daemon in: the daemon
// routes the requests by node_id (revision 30), as a phone's does to show
// the blocks of the computer it shows.

// WatchBlockOn follows the block of node, as WatchBlock follows one of
// the daemon's own.
func (c *Client) WatchBlockOn(ctx context.Context, node, block string, fn func(*protocol.BlockUpdate)) error {
	return c.WatchBlockOnScoped(ctx, node, block, "", fn)
}

// WatchBlockOnScoped follows the block of node in the given canonical scope.
func (c *Client) WatchBlockOnScoped(ctx context.Context, node, block, scope string, fn func(*protocol.BlockUpdate)) error {
	return follow(ctx, c, protocol.TypeWatchBlock, &protocol.WatchBlock{Block: block, NodeId: node, Scope: scope}, fn)
}

// AgentBlocksOn lists the blocks node's agents show.
func (c *Client) AgentBlocksOn(ctx context.Context, node string) (*protocol.AgentBlocks, error) {
	return request[*protocol.AgentBlocks](ctx, c, protocol.TypeListAgentBlocks, &protocol.ListAgentBlocks{NodeId: node})
}

// Panels lists the blocks the layout documents of the node place, and
// its agents' blocks, by name, title and kind ("" for the daemon's own):
// what a node let in lists a computer's panels by before opening one
// (revision 35).
func (c *Client) Panels(ctx context.Context, node string) (*protocol.Panels, error) {
	return request[*protocol.Panels](ctx, c, protocol.TypeListPanels, &protocol.ListPanels{NodeId: node})
}
