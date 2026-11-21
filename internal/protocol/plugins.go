package protocol

// The messages of plugins, a node's own (plugins.proto).
const (
	TypeListPlugins  Type = "list_plugins"
	TypePlugins      Type = "plugins"
	TypePluginAction Type = "plugin_action"
	TypePluginDone   Type = "plugin_done"
	TypeWatchBlock   Type = "watch_block"
	TypeBlockUpdate  Type = "block_update"
	TypeBlockEvent   Type = "block_event"

	TypeListAgentBlocks Type = "list_agent_blocks"
	TypeAgentBlocks     Type = "agent_blocks"

	TypeListPanels Type = "list_panels"
	TypePanels     Type = "panels"

	TypeListRegistries  Type = "list_registries"
	TypeRegistries      Type = "registries"
	TypeRegistryAction  Type = "registry_action"
	TypeSearchPlugins   Type = "search_plugins"
	TypePluginListings  Type = "plugin_listings"
	TypeInstallPlugin    Type = "install_plugin"
	TypePluginInstalled  Type = "plugin_installed"
	TypeSyncPluginDB     Type = "sync_plugin_db"
	TypePluginSyncBundle Type = "plugin_sync_bundle"
)

// The states of a plugin, PluginInfo.state.
const (
	PluginWaiting  = "waiting"  // for the user to allow what it asks for
	PluginDenied   = "denied"   // the user refused what it asks for
	PluginReady    = "ready"    // allowed, and not running
	PluginRunning  = "running"  // its runtime is up
	PluginFailed   = "failed"   // stopped, as error says, until its folder changes or it is enabled
	PluginDisabled = "disabled" // by the user
	PluginBlocked  = "blocked"  // by a registry's blocklist, until it is lifted
)

// The ops of InstallPlugin and RegistryAction.
const (
	InstallOp = "install"
	UpdateOp  = "update"
	RemoveOp  = "remove"

	RegistryAdd     = "add"
	RegistryRemove  = "remove"
	RegistryRefresh = "refresh"
)

// The ops of PluginAction.
const (
	PluginGrant   = "grant"
	PluginDeny    = "deny"
	PluginEnable  = "enable"
	PluginDisable = "disable"
	PluginReload  = "reload"
	PluginRun     = "run"
	PluginSet     = "set"
	// An agent a plugin asks to start (revision 32): allowed with value
	// AgentOnce or AgentAlways, refused, or asked of again every time.
	PluginAllowAgent   = "allow_agent"
	PluginDenyAgent    = "deny_agent"
	PluginForgetAgents = "forget_agents"
	AgentOnce          = "once"
	AgentAlways        = "always"
)

// The kinds of BlockEvent.
const (
	UiTap    = "tap"
	UiChange = "change"
	UiSubmit = "submit"
	UiDrop   = "drop"
)

// The codes of the errors the plugin store and plugins' actions return
// (revision 34), with the args each names, in order, for a front-end to
// say them in its user's language; the message stays the daemon's
// English.
const (
	CodeRegistryUnreachable = "registry_unreachable" // registry
	CodeRegistryNeeded      = "registry_needed"      // id
	CodeNotListed           = "not_listed"           // id
	CodeAPIMismatch         = "api_mismatch"         // id, registry
	CodePluginBlocked       = "plugin_blocked"       // id, version, why
	CodePinnedKey           = "pinned_key"           // id, registry
	CodeInstalledAlready    = "installed_already"    // id, version
	CodeDowngrade           = "downgrade"            // id, version, installed version
	CodeDevFolder           = "dev_folder"           // id
	CodePackageUnreachable  = "package_unreachable"  // id, version
	CodeTimedOut            = "timed_out"            // id, version
	CodeTooLarge            = "too_large"            // id, version
	CodeHashMismatch        = "hash_mismatch"        // id, version, registry
	CodeBadPackage          = "bad_package"          // id, version
	CodeSignatureMismatch   = "signature_mismatch"   // id, version, registry
	CodePermissionsDiffer   = "permissions_differ"   // id, version, registry
	CodeNotInstalled        = "not_installed"        // id
)

// The kinds of a Panel.
const (
	PanelBuiltin = "builtin"
	PanelPlugin  = "plugin"
	PanelAgent   = "agent"
	PanelMissing = "missing"
)
