package claude

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Claude Code tells the model the skills it may use in a skill_listing
// attachment, at a conversation's start and as they change: those it
// bundles, as simplify or loop, beside the user's, the project's and the
// plugins'. Each is a command too, and the listing is the one place that
// names the bundled ones, whatever the version.

// maxListedDescription bounds a listed command's description: the
// listing's is written for the model, often at length.
const maxListedDescription = 160

func (*Agent) LogCommands(line []byte) []agent.Command {
	if !bytes.Contains(line, []byte(`"skill_listing"`)) {
		return nil
	}
	var r struct {
		Type       string `json:"type"`
		Attachment struct {
			Type    string   `json:"type"`
			Content string   `json:"content"`
			Names   []string `json:"names"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "attachment" || r.Attachment.Type != "skill_listing" {
		return nil
	}
	// Each skill's line: "- name: what it does".
	described := map[string]string{}
	for _, l := range strings.Split(r.Attachment.Content, "\n") {
		if name, desc, ok := strings.Cut(strings.TrimPrefix(l, "- "), ": "); ok && strings.HasPrefix(l, "- ") {
			described[name] = agent.Clip(strings.TrimSpace(desc), maxListedDescription)
		}
	}
	out := make([]agent.Command, 0, len(r.Attachment.Names))
	for _, name := range r.Attachment.Names {
		c := agent.Command{Name: name, Description: described[name]}
		if plugin, _, ok := strings.Cut(name, ":"); ok {
			c.Source = plugin
		}
		out = append(out, c)
	}
	return out
}
