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
	return follow(ctx, c, protocol.TypeWatchBlock, &protocol.WatchBlock{Block: block, NodeId: node}, fn)
}

// AgentBlocksOn lists the blocks node's agents show.
func (c *Client) AgentBlocksOn(ctx context.Context, node string) (*protocol.AgentBlocks, error) {
	return request[*protocol.AgentBlocks](ctx, c, protocol.TypeListAgentBlocks, &protocol.ListAgentBlocks{NodeId: node})
}
