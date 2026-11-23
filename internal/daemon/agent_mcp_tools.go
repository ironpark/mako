package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

type agentSummary struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	Profile   string `json:"profile,omitempty"`
	Name      string `json:"name,omitempty"`
	ParentID  string `json:"parent_session_id,omitempty"`
	State     string `json:"state"`
	Status    string `json:"status,omitempty"`
	Messaging bool   `json:"messaging"`
	// Folder is where it works: in a project of several folders, which
	// of them.
	Folder string `json:"folder,omitempty"`
	// Node names the node it is on when it is another's: an agent of the
	// project there.
	Node string `json:"node,omitempty"`
	Hint string `json:"hint,omitempty"`
}

type availableAgent struct {
	Name      string             `json:"name"`
	Spawnable bool               `json:"spawnable" jsonschema:"Whether this agent can be spawned without a profile."`
	Profiles  []availableProfile `json:"profiles"`
}
type availableProfile struct {
	Name        string `json:"name"`
	Description string `json:"description" jsonschema:"Specialties and guidance on when to choose this profile."`
	Spawnable   bool   `json:"spawnable" jsonschema:"Whether this profile enables the Mako MCP tools required for spawning."`
}
type spawnablePreset struct {
	Agent       string `json:"agent" jsonschema:"Agent name to pass to spawn_agent."`
	Profile     string `json:"profile,omitempty" jsonschema:"Profile name to pass to spawn_agent; omitted for the agent's default configuration."`
	Target      string `json:"target" jsonschema:"Target identifier: 'agent' for default or 'agent:profile' for a named profile."`
	Description string `json:"description,omitempty" jsonschema:"Specialties and guidance on when to choose this preset."`
	Model       string `json:"model,omitempty" jsonschema:"Configured model for this preset, if set."`
	Effort      string `json:"effort,omitempty" jsonschema:"Configured reasoning effort for this preset, if set."`
	Yolo        bool   `json:"yolo,omitempty" jsonschema:"Whether YOLO (auto-approve permissions) mode is enabled for this preset."`
}
type listAgentProfilesResult struct {
	Presets []spawnablePreset `json:"presets" jsonschema:"Spawnable agent configurations and profile presets ready to pass to spawn_agent."`
	Agents  []availableAgent  `json:"agents" jsonschema:"Spawnable agents grouped with their spawnable profiles."`
}
type listAgentsResult struct {
	SelfID  string `json:"self_id"`
	Project string `json:"project"`
	// ProjectName names the project of several folders the session is in,
	// whose agents in every folder it lists.
	ProjectName string           `json:"project_name,omitempty"`
	Agents      []agentSummary   `json:"agents"`
	Available   []availableAgent `json:"available"`
}
type spawnAgentArgs struct {
	Agent   string `json:"agent,omitempty" jsonschema:"Agent to launch; defaults to this session's agent. Use list_agent_profiles for spawnable choices."`
	Profile string `json:"profile,omitempty" jsonschema:"Exact profile name from list_agent_profiles. Choose a spawnable profile whose description fits the task, and set agent to its owning agent name. Omit for the agent's default settings."`
	Name    string `json:"name,omitempty"`
	Prompt  string `json:"prompt" jsonschema:"The task for the new agent. It starts in this project's directory."`
}
type sendMessageArgs struct {
	To   string `json:"to"`
	Text string `json:"text"`
}
type readMessagesArgs struct {
	AfterID    uint64 `json:"after_id,omitempty" jsonschema:"Return messages newer than this cursor. Reading does not consume messages."`
	AckThrough uint64 `json:"ack_through,omitempty" jsonschema:"Acknowledge messages through this ID from a previous successful read."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum messages, 1 to 100; defaults to 50."`
}
type readMessagesResult struct {
	Messages    []agentMessage `json:"messages"`
	NextAfterID uint64         `json:"next_after_id"`
}
type stopAgentArgs struct {
	SessionID string `json:"session_id"`
}
type stopAgentResult struct {
	Stopped bool `json:"stopped"`
}

const agentMCPSigDisabled = "disabled"

var agentMCPToolNames = []string{
	"list_agents", "list_agent_profiles", "spawn_agent", "send_message", "read_messages", "watch_agent", "stop_agent",
	toolListBlocks, toolGetLayout, toolSetLayout, toolPatchLayout, toolUndoLayout,
	toolShowUI, toolUpdateUI, toolCloseUI, toolSaveTemplate,
}

type agentMCPSnapshot struct {
	available             []availableAgent
	presets               []spawnablePreset
	blocks                []layoutdoc.Block
	listAgentsDesc        string
	listAgentProfilesDesc string
	spawnAgentDesc        string
	listBlocksDesc        string
	signature             string
}

func (d *Daemon) agentMCPSnapshot() agentMCPSnapshot {
	available := d.availableAgents()
	presets := d.spawnablePresets()
	blocks := d.blocks()
	sigPayload, _ := json.Marshal(struct {
		Available []availableAgent  `json:"available"`
		Presets   []spawnablePreset `json:"presets"`
		Blocks    []layoutdoc.Block `json:"blocks"`
	}{available, presets, blocks})
	sum := sha256.Sum256(sigPayload)
	agentSummaryText := summarizeAvailableForSchema(available)
	listAgentsDesc := "List active and saved agent sessions in this project, on this node and, for a project of several folders, on other nodes, with their IDs, status and messaging support, plus available agent types and profiles."
	listAgentProfilesDesc := "List only the spawnable agent configurations and profile presets available on this node, with each preset's target, description, model, effort and YOLO setting. Call this before spawn_agent to pick the best configuration for a task."
	spawnAgentDesc := "Start a new Mako agent session in the current project with a task. First inspect list_agent_profiles and choose a spawnable preset by its description and settings; supply its agent and profile names. The spawned agent runs asynchronously and reports results back via send_message, which Mako pushes into your session automatically. After spawning, continue other work or end your turn to receive its reply (or register a one-shot notification with watch_agent)."
	if agentSummaryText != "" {
		listAgentsDesc += " Current agents: " + agentSummaryText
		spawnAgentDesc += " Current choices: " + agentSummaryText
	}
	if presetSummaryText := summarizePresetsForSchema(presets); presetSummaryText != "" {
		listAgentProfilesDesc += " Current presets: " + presetSummaryText
	}
	listBlocksDesc := "List the blocks a window layout document can place, with each one's props as a JSON Schema: the catalog to build a document from. A document's leaves are these blocks and the panes, which hold the session tabs."
	if blocksSummaryText := summarizeBlocksForSchema(blocks); blocksSummaryText != "" {
		listBlocksDesc += " Current blocks: " + blocksSummaryText
	}
	return agentMCPSnapshot{
		available:             available,
		presets:               presets,
		blocks:                blocks,
		listAgentsDesc:        listAgentsDesc,
		listAgentProfilesDesc: listAgentProfilesDesc,
		spawnAgentDesc:        spawnAgentDesc,
		listBlocksDesc:        listBlocksDesc,
		signature:             hex.EncodeToString(sum[:8]),
	}
}

func summarizePresetsForSchema(presets []spawnablePreset) string {
	var parts []string
	for _, p := range presets {
		var meta []string
		if p.Model != "" {
			meta = append(meta, "model="+p.Model)
		}
		if p.Effort != "" {
			meta = append(meta, "effort="+p.Effort)
		}
		if p.Yolo {
			meta = append(meta, "yolo")
		}
		entry := p.Target
		if len(meta) > 0 {
			entry += "(" + strings.Join(meta, ",") + ")"
		}
		if desc := strings.Join(strings.Fields(p.Description), " "); desc != "" {
			entry += ": " + clipRunes(desc, 60)
		}
		parts = append(parts, entry)
	}
	return clipRunes(strings.Join(parts, "; "), 1200)
}

func summarizeAvailableForSchema(list []availableAgent) string {
	var parts []string
	for _, a := range list {
		state := "disabled"
		if a.Spawnable {
			state = "spawnable"
		}
		entry := a.Name + "(" + state + ")"
		if len(a.Profiles) > 0 {
			var profiles []string
			for _, p := range a.Profiles {
				pState := "disabled"
				if p.Spawnable {
					pState = "spawnable"
				}
				pText := p.Name + "(" + pState + ")"
				if desc := strings.Join(strings.Fields(p.Description), " "); desc != "" {
					pText += ": " + clipRunes(desc, 60)
				}
				profiles = append(profiles, pText)
			}
			entry += "[" + strings.Join(profiles, "; ") + "]"
		}
		parts = append(parts, entry)
	}
	return clipRunes(strings.Join(parts, "; "), 1200)
}

func summarizeBlocksForSchema(blocks []layoutdoc.Block) string {
	names := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Name != "" {
			names = append(names, b.Name)
		}
	}
	return clipRunes(strings.Join(names, ", "), 600)
}

func clipRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (d *Daemon) agentInstructions(peer *agentPeer) string {
	info := peer.session.Info()
	mode := "main"
	if peer.parent != "" {
		mode = "subagent"
	}
	var waitGuidance string
	switch info.GetAgent() {
	case "agy":
		waitGuidance = "Incoming Mako messages, child status notes, and watch_agent notifications are pushed directly into your Antigravity conversation via agentapi/sidecar and wake you automatically. NEVER poll read_messages in a loop: after spawning or messaging a teammate, continue other work or end your turn and you will be woken up when replies arrive. Use watch_agent (non-blocking) if you want a push notification when an agent's turn ends or prints specific terminal text."
	case "claude":
		waitGuidance = "Incoming Mako messages, child status notes, and watch_agent notifications are pushed natively into your Claude Code session by the Mako mod as soon as your turn is idle (no PTY typing or polling needed). NEVER poll read_messages in a loop: once you have delegated work, simply report what you spawned and END YOUR TURN—Mako will automatically start a new turn with the child's reply or notification. Use watch_agent (non-blocking) to register a push notification for a turn ending or terminal output matching text, or `mako-msg wait --all-children` via Bash(run_in_background=true) if you must wait while staying in-turn."
	default:
		waitGuidance = "NEVER poll read_messages in a loop. Incoming messages, child status notes, and watch_agent notifications are delivered automatically once your turn ends. Use watch_agent (non-blocking) to register a one-shot push notification when another agent finishes a turn or prints specific terminal text, or run `mako-msg wait --all-children` as a background shell command for event-driven waiting."
	}
	pluginHint := ""
	if ids := d.usablePluginIDs(peer); len(ids) > 0 {
		pluginHint = fmt.Sprintf(" Available plugins via CLI (`mako-plugin list`, `mako-plugin help <plugin> [tool]`, `mako-plugin call <plugin> <tool> [--param val | --json ...]`): %s.", strings.Join(ids, ", "))
	}
	return fmt.Sprintf("You are Mako session %s (mode=%s, AGENT_MAKO_SESSION=%s, AGENT_MAKO_MODE=%s) in project %s. Parent session: %s. Before delegating, use list_agent_profiles to inspect spawnable agent configurations and profile presets (with descriptions, models, efforts and YOLO settings), or list_agents to inspect active project sessions. Choose a spawnable preset whose specialties match the task, then pass its agent and profile names to spawn_agent (omit profile for an agent default). Use send_message to communicate, with agents of this project on other nodes too, which list_agents lists with their node. %s Report results to the parent with send_message. Of an agent you spawned that waits on the user, or exits without reporting, you hear in a Mako note. stop_agent only stops agents you spawned. The mako window's layout is a document you read with get_layout and list_blocks; set_layout, patch_layout and undo_layout change it once the user allows, and only what the user keeps stays. show_ui shows a block of UI of yours in the window, a form say, and the user's taps on it come back to you as messages from the window; update_ui and close_ui change and close it. A UI to keep is a plugin: list_blocks names the folder to write one into.%s",
		peer.id, mode, peer.id, mode, info.GetProject(), peer.parent, waitGuidance, pluginHint)
}

func (d *Daemon) usablePluginIDs(peer *agentPeer) []string {
	if d.plugins == nil || peer == nil || peer.session == nil {
		return nil
	}
	scopeCtx := d.sessionScopeContext(peer.session.ID(), peer.session.Info())
	seen := map[string]bool{}
	var ids []string
	for _, tl := range d.plugins.Tools() {
		if !toolUsableInScope(tl.Scope, scopeCtx) || seen[tl.PluginID] {
			continue
		}
		seen[tl.PluginID] = true
		ids = append(ids, tl.PluginID)
	}
	slices.Sort(ids)
	return ids
}

func (d *Daemon) newProjectAgentServer(peer *agentPeer, snap agentMCPSnapshot, enabled bool) (*sdk.Server, string) {
	server := sdk.NewServer(&sdk.Implementation{Name: "mako", Version: config.Version}, &sdk.ServerOptions{
		Logger: d.log,
		Capabilities: &sdk.ServerCapabilities{
			Tools: &sdk.ToolCapabilities{ListChanged: true},
		},
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 0 },
		Instructions: d.agentInstructions(peer),
	})
	if !enabled {
		return server, agentMCPSigDisabled
	}
	d.registerProjectAgentTools(server, peer, snap)
	return server, snap.signature
}

func (d *Daemon) addDynamicAgentTools(server *sdk.Server, peer *agentPeer, snap agentMCPSnapshot) {
	sdk.AddTool(server, &sdk.Tool{Name: "list_agents", Description: snap.listAgentsDesc, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listAgentsResult, error) {
		out, err := d.listProjectAgents(peer)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "list_agent_profiles", Description: snap.listAgentProfilesDesc, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listAgentProfilesResult, error) {
		out, err := d.listAgentProfiles(peer)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "spawn_agent", Description: snap.spawnAgentDesc}, func(ctx context.Context, _ *sdk.CallToolRequest, in spawnAgentArgs) (*sdk.CallToolResult, agentSummary, error) {
		out, err := d.spawnProjectAgent(ctx, peer, in)
		return nil, out, err
	})
	d.addListBlocksTool(server, peer, snap.listBlocksDesc)
}

func (d *Daemon) registerProjectAgentTools(server *sdk.Server, peer *agentPeer, snap agentMCPSnapshot) {
	d.addDynamicAgentTools(server, peer, snap)
	sdk.AddTool(server, &sdk.Tool{Name: "send_message", Description: "Send a message to another active agent in this project, on this node or another. Mako delivers it automatically to the recipient (via native push on Claude Code/Antigravity or as a prompt when idle). Replies are pushed back to you automatically once your turn ends—do not poll afterwards."}, func(ctx context.Context, _ *sdk.CallToolRequest, in sendMessageArgs) (*sdk.CallToolResult, agentMessage, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, agentMessage{}, err
		}
		if err := checkAgentText(in.Text); err != nil {
			return nil, agentMessage{}, err
		}
		msg, err := d.sendAgentMessage(ctx, peer, in.To, in.Text)
		return nil, msg, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "read_messages", Description: "Read pending messages currently in your inbox (instant, non-blocking snapshot). Incoming messages and watch_agent notifications are pushed into your session automatically once your turn ends; do not poll read_messages in a loop. Use after_id for pagination and ack_through to acknowledge processed messages.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, in readMessagesArgs) (*sdk.CallToolResult, readMessagesResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, readMessagesResult{}, err
		}
		if in.Limit == 0 {
			in.Limit = 50
		}
		if in.Limit < 1 || in.Limit > 100 {
			return nil, readMessagesResult{}, errors.New("limit must be 1..100")
		}
		messages, err := d.projectAgents.messages.read(ctx, peer.id, in.AfterID, in.AckThrough, in.Limit, 0)
		next := max(in.AfterID, in.AckThrough)
		if len(messages) > 0 {
			next = messages[len(messages)-1].ID
		}
		return nil, readMessagesResult{Messages: messages, NextAfterID: next}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "watch_agent", Description: "Register a non-blocking one-shot notification for another agent in this project: Mako pushes a note into your session when its current turn ends (until=turn, default) or when its terminal prints matching text (until=output, this node only), or early if it waits on the user or exits. Returns immediately—continue other work or end your turn to receive the notification."}, func(ctx context.Context, _ *sdk.CallToolRequest, in watchAgentArgs) (*sdk.CallToolResult, watchAgentResult, error) {
		out, err := d.watchProjectAgent(peer, in)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "stop_agent", Description: "Terminate and remove an agent session spawned directly by you. Other project sessions cannot be stopped with this tool."}, func(ctx context.Context, _ *sdk.CallToolRequest, in stopAgentArgs) (*sdk.CallToolResult, stopAgentResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, stopAgentResult{}, err
		}
		d.sessionMu.Lock()
		saved, exists := d.saved[in.SessionID]
		d.sessionMu.Unlock()
		// Scoping reads the folders, off the lock.
		owned := exists && saved.ParentID == peer.id && d.projectScope(saved.Info) == d.projectScope(peer.session.Info())
		if !owned {
			return nil, stopAgentResult{}, errors.New("only agents you spawned can be stopped")
		}
		err := d.closeSession(in.SessionID)
		return nil, stopAgentResult{Stopped: err == nil}, err
	})
	d.addStaticLayoutTools(server, peer)
	d.addUITools(server, peer)
	d.addTemplateTools(server, peer)
}

func (d *Daemon) syncPeerServerLocked(peer *agentPeer, srv *sdk.Server, snap agentMCPSnapshot, enabled bool) {
	d.projectAgents.mu.Lock()
	prev, tracked := peer.servers[srv]
	d.projectAgents.mu.Unlock()
	if !tracked {
		return
	}
	var nextSig string
	switch {
	case !enabled && prev == agentMCPSigDisabled:
		return
	case !enabled:
		srv.RemoveTools(agentMCPToolNames...)
		nextSig = agentMCPSigDisabled
	case prev == agentMCPSigDisabled:
		d.registerProjectAgentTools(srv, peer, snap)
		nextSig = snap.signature
	case prev != snap.signature:
		d.addDynamicAgentTools(srv, peer, snap)
		nextSig = snap.signature
	default:
		return
	}
	d.projectAgents.mu.Lock()
	if d.projectAgents.peers[peer.id] == peer && peer.ctx.Err() == nil {
		if _, ok := peer.servers[srv]; ok {
			peer.servers[srv] = nextSig
		}
	}
	d.projectAgents.mu.Unlock()
}

// syncProjectAgents updates every running session's built-in MCP server
// tools, descriptions and per-session MCP configuration files in real time.
func (d *Daemon) syncProjectAgents() {
	d.projectAgents.syncMu.Lock()
	defer d.projectAgents.syncMu.Unlock()

	snap := d.agentMCPSnapshot()
	type tracked struct {
		peer    *agentPeer
		session *session.Session
		servers []*sdk.Server
		refresh func(context.Context, *protocol.SessionInfo)
	}
	var list []tracked
	d.projectAgents.mu.Lock()
	for _, peer := range d.projectAgents.peers {
		if peer == nil || peer.ctx.Err() != nil || peer.session == nil {
			continue
		}
		if peer.session.Info().GetState() != protocol.StateRunning {
			continue
		}
		list = append(list, tracked{
			peer:    peer,
			session: peer.session,
			servers: slices.Collect(maps.Keys(peer.servers)),
			refresh: peer.refresh,
		})
	}
	d.projectAgents.mu.Unlock()

	baseCtx := d.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	for _, item := range list {
		info := item.session.Info()
		enabled := d.sessionAgentMCPEnabled(info)
		for _, srv := range item.servers {
			d.syncPeerServerLocked(item.peer, srv, snap, enabled)
		}
		if item.refresh != nil {
			rctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			item.refresh(rctx, info)
			cancel()
		}
	}
}
