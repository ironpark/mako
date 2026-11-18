package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
)

// What a plugin with sessions:read learns of this node's sessions beyond
// their listing: a page of a session's conversation, and the events it
// subscribed to. Both are of this node's own sessions alone: a peer's
// are not the plugin's to read.

// pluginTranscript reads a page of a running session's conversation for
// a plugin: up to the byte offset before, or the log's end as it is now,
// which it returns so that the next page is read from the same window.
func (d *Daemon) pluginTranscript(_ context.Context, id string, before int64) (*protocol.Chat, int64, error) {
	s, ok := d.sessions.get(id)
	if !ok {
		return nil, 0, fmt.Errorf("session %q is not running on this node", id)
	}
	path := s.TranscriptPath()
	if path == "" {
		return &protocol.Chat{}, 0, nil
	}
	if before <= 0 {
		info, err := os.Stat(path)
		if err != nil {
			return nil, 0, err
		}
		before = info.Size()
		if before == 0 {
			return &protocol.Chat{Supported: true}, 0, nil
		}
	}
	span := transcript.Span{Before: before}
	var chat *protocol.Chat
	var err error
	if cache := d.conversations.chatCache(s); cache != nil {
		chat, err = cache.ReadBound(s.Info(), path, span)
	} else {
		chat, err = transcript.ReadBound(s.Info(), path, span)
	}
	return chat, before, err
}

// feedPluginEvents tells the plugins what happens to this node's sessions,
// while one is subscribed: their listing each time the notifier says the
// sessions changed, as it tells the front-ends, and a message for an
// agent as it is left (agentMessages.added).
func (d *Daemon) feedPluginEvents(ctx context.Context) {
	for {
		wanted, changed := d.plugins.EventsWanted()
		if !wanted {
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return
			}
		}
		feed, stop := d.changes.watch()
		d.plugins.SessionsChanged(d.listSessions())
	follow:
		for {
			select {
			case c := <-feed:
				if c.GetSessions() {
					d.plugins.SessionsChanged(d.listSessions())
				}
			case <-changed:
				break follow
			case <-ctx.Done():
				stop()
				return
			}
		}
		stop()
	}
}

// pluginMessage tells the plugins of a message left for an agent.
func (d *Daemon) pluginMessage(msg agentMessage) {
	if d.plugins != nil {
		d.plugins.AgentMessage(msg.ID, msg.From, msg.To, msg.Text, msg.Note)
	}
}

// pluginProjects are the projects this node knows, with their folders
// that are on it, for a plugin's fs:project: a plugin names one of them,
// never a folder of its own choosing.
func (d *Daemon) pluginProjects() []plugin.ProjectRoots {
	var out []plugin.ProjectRoots
	for _, p := range d.projects.get() {
		pr := plugin.ProjectRoots{ID: p.GetId(), Name: p.GetName()}
		for _, root := range p.GetPaths() {
			if info, err := os.Stat(root); err == nil && info.IsDir() && filepath.IsAbs(root) {
				pr.Roots = append(pr.Roots, root)
			}
		}
		if len(pr.Roots) > 0 {
			out = append(out, pr)
		}
	}
	return out
}
