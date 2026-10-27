// Package agy is mako's driver for Antigravity's CLI, agy: its options,
// the hooks it reads from its global hooks.json, and the transcripts it
// keeps in a folder for each conversation.
package agy

import (
	"image/color"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is agy.
type Agent struct {
	agent.Flags
}

// New is the agy driver. agy picks a new conversation's ID itself.
func New() *Agent {
	return &Agent{Flags: agent.Flags{
		ResumeFlags: []string{"--conversation"},
		PickerFlags: []string{"--continue", "-c"},
		// What starts the conversation, which a resumed one already has.
		Inputs: strings.Fields("--prompt-interactive -i --print -p --prompt"),
		Values: strings.Fields("--add-dir --agent --effort --mode --model --project --log-file --print-timeout --input-format --output-format --json-schema"),
	}}
}

func (*Agent) Info() agent.Info {
	return agent.Info{
		Name: "agy", Programs: []string{"agy"}, Command: []string{"agy"},
		Icon: "agent-agy", Color: color.NRGBA{0x3e, 0x8b, 0xff, 0xff},
	}
}
