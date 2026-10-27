// Package pi is mako's driver for pi and for the agents built on it: omp,
// oh-my-pi, and OmO's own edition, omo, on senpi. It knows their options,
// the extension mako has them load to hear their events, and their
// session logs, which have the same shape.
package pi

import (
	"image/color"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is pi or one built on it: what tells one from another.
type Agent struct {
	agent.Flags
	agent.CommonHooks
	info  agent.Info
	place agent.Place
	// settled is the extension event that says a prompt's work is done,
	// with no retry or continuation left.
	settled string
}

// Pi is pi, or omo, which also take MCP servers from an extension.
type Pi struct{ *Agent }

// newAgent is the agent called name, with its options, keeping its state
// in ~/.<name>/agent unless rootEnv says.
func newAgent(name, rootEnv string, c color.NRGBA, flags agent.Flags, settled string) *Agent {
	return &Agent{
		Flags: flags,
		info: agent.Info{
			Name: name, Programs: []string{name}, Command: []string{name},
			Icon: "agent-" + name, Color: c,
		},
		place:   agent.Place{RootEnv: rootEnv, HomeDir: filepath.Join("."+name, "agent"), Dirs: []string{"sessions", "*"}},
		settled: settled,
	}
}

// piFlags are pi's options, and omo's.
var piFlags = agent.Flags{
	PinFlag: "--session-id", ResumeFlags: []string{"--session"},
	PickerValues: []string{"--fork"}, PickerFlags: []string{"--continue", "-c", "--resume", "-r", "--no-session"},
	Values: strings.Fields("--provider --model --api-key --thinking --models --tools -t --exclude-tools -xt --extension -e --skill --prompt-template --theme --use-theme --system-prompt --append-system-prompt --tui-mode --session-dir --name -n --mode"),
}

// New is the pi driver.
func New() *Pi {
	return &Pi{newAgent("pi", "PI_CODING_AGENT_DIR", color.NRGBA{0x5b, 0xa3, 0x7c, 0xff}, piFlags, "agent_settled")}
}

// NewOMO is the driver for OmO's own edition, omo: pi's options and
// events, with OmO's tools.
func NewOMO() *Pi {
	return &Pi{newAgent("omo", "OMO_CODING_AGENT_DIR", color.NRGBA{0xf2, 0xa2, 0x3a, 0xff}, piFlags, "agent_settled")}
}

// NewOMP is the omp driver. omp picks a new session's ID itself, and has
// no event after the last of a prompt's runs. On Linux and macOS a
// $XDG_DATA_HOME/omp that exists holds its sessions instead, which mako
// does not follow.
func NewOMP() *Agent {
	return newAgent("omp", "PI_CODING_AGENT_DIR", color.NRGBA{0xe0, 0x6c, 0x9f, 0xff}, agent.Flags{
		ResumeFlags:  []string{"--resume", "-r", "--session"},
		PickerValues: []string{"--fork", "--restore"}, PickerFlags: []string{"--continue", "-c", "--no-session"},
		Values: strings.Fields("--cwd --config --add-dir --mode --provider --model --smol --slow --goal --plan --max-time --warn-cost --max-cost --service-tier --api-key --system-prompt --system-prompt-template --append-system-prompt --provider-session-id --prompt-cache-key --session-dir --models --tools --thinking --hook --extension -e --trusted-extension --plugin-dir --skills --approval-mode --profile --alias"),
	}, "agent_end")
}

func (a *Agent) Info() agent.Info { return a.info }
