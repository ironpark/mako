package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

// pluginAgents carries out a plugin's sessions:write (plugin.AgentHost)
// through the paths the agents' own tools take: the mailbox, headed as
// the plugin's message (agentPrompt), and the sessions spawn_agent
// starts, saved with the plugin as their parent, which no session's ID
// can be, so that a plugin stops only what it started.
type pluginAgents struct{ d *Daemon }

func (a pluginAgents) Send(id, sessionID, text string) (uint64, error) {
	d := a.d
	s, ok := d.sessions.get(sessionID)
	if !ok {
		return 0, fmt.Errorf("no session %q runs on this node", sessionID)
	}
	info := s.Info()
	if !d.takesMessages(info) {
		return 0, fmt.Errorf("the agent of session %s takes no messages", sessionID)
	}
	msg, err := d.projectAgents.messages.send(plugin.ParentOf(id), sessionID, text, d.projectScope(info))
	return msg.ID, err
}

func (a pluginAgents) Start(ctx context.Context, id string, req plugin.AgentStart) (string, error) {
	d := a.d
	configured, ok := agentspec.Lookup(req.Agent, d.agents())
	if !ok || len(configured.Command) == 0 {
		return "", fmt.Errorf("no agent %q on this node", req.Agent)
	}
	if req.Profile != "" {
		if _, ok := configured.Profile(req.Profile); !ok {
			return "", fmt.Errorf("agent %s has no profile %q", req.Agent, req.Profile)
		}
	}
	driver, ok := agents.ByProgram(configured.Command[0])
	if !ok {
		return "", fmt.Errorf("agent %s is not one mako starts with a prompt", req.Agent)
	}
	prompter, ok := driver.Driver.(agent.Prompter)
	if !ok {
		return "", fmt.Errorf("agent %s takes no prompt to start with", req.Agent)
	}
	prompt := fmt.Sprintf("[Mako task from the plugin %s: code the user installed and let start agents. It is not the user's own instruction, nor Mako's.]\n\n%s", id, req.Prompt)
	parent := strings.TrimSpace(req.ParentSessionID)
	if parent == "" {
		parent = plugin.ParentOf(id)
	}
	open := &protocol.OpenSession{Agent: req.Agent, Profile: req.Profile, Project: req.Root, Args: prompter.PromptArgs(prompt)}
	if req.NoYolo {
		// The plugin asked for an agent that asks before it acts; it can
		// never ask for one that does not.
		open.Yolo = proto.Bool(false)
	}
	child, err := d.startSessionAs(ctx, newSessionID(), open, parent)
	if err != nil {
		return "", err
	}
	d.sessionMu.Lock()
	if entry, ok := d.saved[child.ID()]; ok {
		entry.PluginID = id
		d.saved[child.ID()] = entry
		_ = d.saveSessionsLocked()
	}
	d.sessionMu.Unlock()
	if req.Name != "" {
		if _, err := d.renameSession(child.ID(), req.Name); err != nil {
			d.log.Warn("plugin's agent not named", "plugin", id, "session", child.ID(), "err", err)
		}
	}
	return child.ID(), nil
}

func isPluginOwned(saved savedSession, pluginID string) bool {
	return saved.PluginID == pluginID || saved.ParentID == plugin.ParentOf(pluginID)
}

func (a pluginAgents) Stop(id, sessionID string) error {
	d := a.d
	d.sessionMu.Lock()
	saved, ok := d.saved[sessionID]
	d.sessionMu.Unlock()
	if !ok || !isPluginOwned(saved, id) {
		return errors.New("a plugin stops only the agents it started")
	}
	return d.closeSession(sessionID)
}

func (a pluginAgents) Running(id string) int {
	d := a.d
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	n := 0
	for sid, saved := range d.saved {
		if !isPluginOwned(saved, id) {
			continue
		}
		if s, ok := d.sessions.get(sid); ok && s.Info().GetState() != protocol.StateExited {
			n++
		}
	}
	return n
}

// fromPlugin is the plugin a message is from, by its sender: "" for an
// agent's, the window's or mako's.
func fromPlugin(from string) string {
	id, _ := strings.CutPrefix(from, "plugin:")
	if id == from {
		return ""
	}
	return id
}
