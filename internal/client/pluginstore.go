package client

import (
	"context"

	"github.com/ironpark/mako/internal/protocol"
)

// Registries lists the daemon's plugin registries; see
// protocol.ListRegistries.
func (c *Client) Registries(ctx context.Context) (*protocol.Registries, error) {
	return request[*protocol.Registries](ctx, c, protocol.TypeListRegistries, &protocol.ListRegistries{})
}

// RegistryAction adds, removes or reads again the daemon's registries;
// see protocol.RegistryAction.
func (c *Client) RegistryAction(ctx context.Context, req *protocol.RegistryAction) (*protocol.Registries, error) {
	return request[*protocol.Registries](ctx, c, protocol.TypeRegistryAction, req)
}

// SearchPlugins asks the daemon's registries for plugins; see
// protocol.SearchPlugins.
func (c *Client) SearchPlugins(ctx context.Context, req *protocol.SearchPlugins) (*protocol.PluginListings, error) {
	return request[*protocol.PluginListings](ctx, c, protocol.TypeSearchPlugins, req)
}

// InstallPlugin installs, updates or removes a plugin from a registry;
// see protocol.InstallPlugin.
func (c *Client) InstallPlugin(ctx context.Context, req *protocol.InstallPlugin) (*protocol.PluginInstalled, error) {
	return request[*protocol.PluginInstalled](ctx, c, protocol.TypeInstallPlugin, req)
}
