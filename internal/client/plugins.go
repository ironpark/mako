package client

import (
	"context"

	"github.com/ironpark/mako/internal/protocol"
)

// Plugins lists the daemon's plugins; see protocol.ListPlugins.
func (c *Client) Plugins(ctx context.Context) (*protocol.Plugins, error) {
	return request[*protocol.Plugins](ctx, c, protocol.TypeListPlugins, &protocol.ListPlugins{})
}

// PluginAction acts on a plugin for the user: allows what it asks for,
// runs a command, sets a setting, ...; see protocol.PluginAction.
func (c *Client) PluginAction(ctx context.Context, req *protocol.PluginAction) (*protocol.PluginDone, error) {
	return request[*protocol.PluginDone](ctx, c, protocol.TypePluginAction, req)
}

// BlockEvent tells a plugin of the user acting on a node of its block.
func (c *Client) BlockEvent(ctx context.Context, req *protocol.BlockEvent) (*protocol.PluginDone, error) {
	return request[*protocol.PluginDone](ctx, c, protocol.TypeBlockEvent, req)
}

// WatchBlock follows a plugin's block, calling fn with each update until
// ctx ends or the stream does; see protocol.WatchBlock.
func (c *Client) WatchBlock(ctx context.Context, block string, fn func(*protocol.BlockUpdate)) error {
	return follow(ctx, c, protocol.TypeWatchBlock, &protocol.WatchBlock{Block: block}, fn)
}
