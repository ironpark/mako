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
)

// The states of a plugin, PluginInfo.state.
const (
	PluginWaiting  = "waiting"  // for the user to allow what it asks for
	PluginDenied   = "denied"   // the user refused what it asks for
	PluginReady    = "ready"    // allowed, and not running
	PluginRunning  = "running"  // its runtime is up
	PluginFailed   = "failed"   // stopped, as error says, until its folder changes or it is enabled
	PluginDisabled = "disabled" // by the user
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
)

// The kinds of BlockEvent.
const (
	UiTap    = "tap"
	UiChange = "change"
	UiSubmit = "submit"
)
