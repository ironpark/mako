package protocol

import (
	"cmp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
)

// DialAddrs are where the node is on libp2p, each address ending in its
// peer ID, as another node's peer or relay entry takes them; a relayed one
// names its relay's ID before. None without libp2p.
func (n *Node) DialAddrs() []string {
	if n.GetPeerId() == "" {
		return nil
	}
	own := "/p2p/" + n.GetPeerId()
	out := make([]string, 0, len(n.GetMultiaddrs()))
	for _, a := range n.GetMultiaddrs() {
		if !strings.HasSuffix(a, own) {
			a += own
		}
		out = append(out, a)
	}
	return out
}

// Revision numbers what a daemon of this build understands. A front-end
// that finds its daemon on an older one can say the daemon is out of date
// rather than leave the newer parts blank.
const Revision = 6

// AgentStatus is what an agent's own hooks last said it is doing,
// SessionInfo.Status.
type AgentStatus = string

const (
	AgentIdle    AgentStatus = "idle"    // at its prompt, nothing asked of it
	AgentWorking AgentStatus = "working" // on a turn the user gave it
	AgentWaiting AgentStatus = "waiting" // asking the user, an approval or a question
	AgentDone    AgentStatus = "done"    // finished its turn
	// AgentCompacting is at work compacting its conversation.
	AgentCompacting AgentStatus = "compacting"
)

// AtWork reports whether an agent of status is at work: on a turn, or
// compacting.
func AtWork(status AgentStatus) bool { return status == AgentWorking || status == AgentCompacting }

// Decisions of an AnswerApproval.
const (
	DecisionAllow    = "allow"
	DecisionDeny     = "deny"
	DecisionTerminal = "terminal" // ask in the terminal, as without mako
)

// RGBA packs a colour as AgentInfo.Color holds it, 0xRRGGBBAA.
func RGBA(r, g, b, a uint8) uint32 {
	return uint32(r)<<24 | uint32(g)<<16 | uint32(b)<<8 | uint32(a)
}

// Channels unpacks AgentInfo.Color.
func (a *AgentInfo) Channels() (r, g, b, alpha uint8) {
	c := a.GetColor()
	return uint8(c >> 24), uint8(c >> 16), uint8(c >> 8), uint8(c)
}

// Caps are what mako follows of an agent, SessionInfo.Caps: each bit is a
// capability the agent's driver has.
const (
	CapChat      uint32 = 1 << iota // its conversation reads from a log: chat, usage
	CapHooks                        // its hooks report: status, approvals
	CapSubAgents                    // the subagents its calls start are followed
)

// The kinds of tool, ChatMessage.Kind: what a tool does, whatever its agent
// calls it. A call of a known kind carries its arguments in one form for
// the kind, whatever its agent writes, as a JSON object of these fields,
// each optional:
//
//	shell      command, cwd, description
//	patch      patch: files changed, in Codex's patch format
//	edit       path, old, new
//	multiedit  path, edits: [{old, new}]
//	write      path, content
//	read       path, offset (its first line, from 1), limit
//	search     pattern, path, glob, type, mode
//	todo, plan explanation, items: [{text, status: pending, in_progress, completed}]
//	fetch      url, prompt
//	websearch  query, allowed_domains, blocked_domains
//	task       description, prompt, agent (the sort of agent)
//	question   question, options: [string]
//
// and what it gave back as the tool's own output, without what the agent
// wraps it in: a read's text without line numbers, a command's output
// without a header and its exit code apart, in ChatMessage.ExitCode.
const (
	ToolShell     = "shell"
	ToolPatch     = "patch" // a patch in its own format, as Codex's apply_patch
	ToolEdit      = "edit"
	ToolMultiEdit = "multiedit"
	ToolWrite     = "write"
	ToolRead      = "read"
	ToolSearch    = "search"
	ToolTodo      = "todo" // a todo list, as Claude's TodoWrite
	ToolPlan      = "plan" // a plan's steps, as Codex's update_plan
	ToolFetch     = "fetch"
	ToolWebSearch = "websearch"
	ToolTask      = "task"     // starts a subagent
	ToolQuestion  = "question" // asks the user
	ToolCommand   = "command"  // what a slash command the user typed printed
	ToolSummary   = "summary"  // the summary a compaction leaves
)

// The windows of the limits agents name, in minutes.
const (
	FiveHours = 5 * 60
	Week      = 7 * 24 * 60
)

// IsZero reports whether nothing is counted; nil counts nothing.
func (u *Usage) IsZero() bool { return proto.Size(u) == 0 }

// Merge takes what r says over what u says: the window and each limit r
// names.
func (u *Usage) Merge(r *Usage) {
	if r.GetWindow() != 0 {
		u.Window = r.GetWindow()
	}
	for _, l := range r.GetLimits() {
		u.SetLimit(l)
	}
}

// SetLimit records l in place of the limit of the same window, keeping
// the shortest window first. It changes u's list, not the limits in it.
func (u *Usage) SetLimit(l *RateLimit) {
	limits := slices.DeleteFunc(slices.Clone(u.Limits), func(o *RateLimit) bool { return o.GetMinutes() == l.GetMinutes() })
	i, _ := slices.BinarySearchFunc(limits, l.GetMinutes(), func(o *RateLimit, m int64) int { return cmp.Compare(o.GetMinutes(), m) })
	u.Limits = slices.Insert(limits, i, l)
}

// How a daemon and a node stand, as Standing says from the node's record
// in the daemon's list_nodes.
const (
	StandingSelf = "self" // the daemon itself
	StandingBoth = "both" // each lets the other in
	StandingUs   = "us"   // the node lets the daemon in
	StandingThem = "them" // the daemon lets the node in
	StandingNone = "none"
)

// Standing is how the daemon that listed n stands with it.
func (n *Node) Standing() string {
	switch {
	case n.GetSource() == "self":
		return StandingSelf
	case n.GetTrusted() && n.GetTrustsUs():
		return StandingBoth
	case n.GetTrustsUs():
		return StandingUs
	case n.GetTrusted():
		return StandingThem
	}
	return StandingNone
}
