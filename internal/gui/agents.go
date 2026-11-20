package gui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// agentDraft is one row of the agents page's draft: an agent's own
// settings when Profile is "", else one of its profiles. Flags are edited
// as a command line and the environment as NAME=value lines, so the row
// stays comparable. ID keeps a row's editor in place while its name is
// typed.
type agentDraft struct {
	ID                 int
	Agent, Profile     string
	Description        string
	Command, Args, Env string
	// Enabled is "on" or "off" as the user set it, or "" for while the
	// program is installed.
	Enabled   string
	Yolo      bool // bypass permission prompts when the agent supports it
	IsProfile bool // a profile's name may be blank while it is typed
	// PickServers gives a profile's sessions the MCP servers in Servers,
	// one ID a line, rather than every one enabled.
	PickServers bool
	Servers     string
	Model         string // a profile's; "" for the agent's default
	Effort        string // a profile's; "" for the agent's default
	ProfileYolo   string // "", "on", or "off" (profile override of agent Yolo)
	AllowAgent    bool   // profile: allow models/agents to spawn this profile via MCP
	AllowTemplate bool   // profile: allow prompt templates to run this profile
}

// draftAgents lays out cfg's agents for editing: every agent the node can
// launch, built-ins included, each followed by its profiles.
func draftAgents(agents map[string]agentspec.Agent) []agentDraft {
	var out []agentDraft
	for _, name := range agentspec.Names(agents) {
		a, _ := agentspec.Lookup(name, agents)
		agentArgs, lifted := agentspec.LiftYoloArgs(name, a.Args)
		d := agentDraft{ID: len(out) + 1, Agent: name,
			Command: agentspec.JoinArgs(a.Command), Args: agentspec.JoinArgs(agentArgs), Env: envText(a.Env),
			Yolo: a.Yolo || lifted, AllowAgent: a.AgentAllowed(), AllowTemplate: a.TemplateAllowed()}
		if a.Enabled != nil {
			d.Enabled = pick(*a.Enabled, "on", "off")
		}
		out = append(out, d)
		for _, p := range a.Profiles {
			pArgs, pLifted := agentspec.LiftYoloArgs(name, p.Args)
			var profYolo string
			if p.Yolo != nil {
				profYolo = pick(*p.Yolo, "on", "off")
			} else if pLifted {
				profYolo = "on"
			}
			d := agentDraft{ID: len(out) + 1, Agent: name, Profile: p.Name, IsProfile: true,
				Description: p.Description, Args: agentspec.JoinArgs(pArgs), Env: envText(p.Env),
				Model: p.Model, Effort: p.Effort, ProfileYolo: profYolo,
				AllowAgent: p.AgentAllowed(), AllowTemplate: p.TemplateAllowed()}
			if p.ContextServers != nil {
				d.PickServers, d.Servers = true, strings.Join(*p.ContextServers, "\n")
			}
			out = append(out, d)
		}
	}
	return out
}

// sameAgents compares two drafts by what they say, not by their rows' IDs.
func sameAgents(a, b []agentDraft) bool {
	return slices.EqualFunc(a, b, func(x, y agentDraft) bool {
		x.ID, y.ID = 0, 0
		return x == y
	})
}

// applyAgents reads a draft back into agents, or names the first field it
// cannot read.
func applyAgents(rows []agentDraft) (map[string]agentspec.Agent, error) {
	out := map[string]agentspec.Agent{}
	var order []string
	for _, r := range rows {
		if r.IsProfile {
			continue
		}
		if !agentspec.ValidName(r.Agent) {
			return nil, fmt.Errorf(T("agent %q: a name has no spaces or colons"), r.Agent)
		}
		command, err := agentspec.SplitArgs(r.Command)
		if err != nil {
			return nil, fmt.Errorf(T("%s, command: %v"), r.Agent, err)
		}
		if _, builtin := agentspec.Builtin[r.Agent]; len(command) == 0 && !builtin {
			return nil, fmt.Errorf(T("%s needs a command"), r.Agent)
		}
		args, err := agentspec.SplitArgs(r.Args)
		if err != nil {
			return nil, fmt.Errorf(T("%s, flags: %v"), r.Agent, err)
		}
		env, err := parseEnv(r.Env)
		if err != nil {
			return nil, fmt.Errorf(T("%s, environment: %v"), r.Agent, err)
		}
		args, lifted := agentspec.LiftYoloArgs(r.Agent, args)
		a := agentspec.Agent{Command: command, Args: args, Env: env, Yolo: r.Yolo || lifted}
		if r.Enabled != "" {
			on := r.Enabled == "on"
			a.Enabled = &on
		}
		if !r.AllowAgent {
			off := false
			a.AllowAgent = &off
		}
		if !r.AllowTemplate {
			off := false
			a.AllowTemplate = &off
		}
		out[r.Agent] = a
		order = append(order, r.Agent)
	}
	for _, r := range rows {
		if !r.IsProfile {
			continue
		}
		where := r.Agent + ":" + r.Profile
		if !agentspec.ValidName(r.Profile) {
			return nil, fmt.Errorf(T("%s has a profile named %q: a name has no spaces or colons"), r.Agent, r.Profile)
		}
		a := out[r.Agent]
		if _, dup := a.Profile(r.Profile); dup {
			return nil, fmt.Errorf(T("%s has two profiles named %s"), r.Agent, r.Profile)
		}
		args, err := agentspec.SplitArgs(r.Args)
		if err != nil {
			return nil, fmt.Errorf(T("%s, flags: %v"), where, err)
		}
		env, err := parseEnv(r.Env)
		if err != nil {
			return nil, fmt.Errorf(T("%s, environment: %v"), where, err)
		}
		args, lifted := agentspec.LiftYoloArgs(r.Agent, args)
		p := agentspec.Profile{Name: r.Profile, Description: r.Description, Args: args, Env: env, Model: strings.TrimSpace(r.Model), Effort: strings.TrimSpace(r.Effort)}
		if !r.AllowAgent {
			off := false
			p.AllowAgent = &off
		}
		if !r.AllowTemplate {
			off := false
			p.AllowTemplate = &off
		}
		switch r.ProfileYolo {
		case "on":
			on := true
			p.Yolo = &on
		case "off":
			off := false
			p.Yolo = &off
		default:
			if lifted {
				on := true
				p.Yolo = &on
			}
		}
		if r.PickServers {
			ids := lines(r.Servers)
			if ids == nil {
				ids = []string{} // JSON [] means none; null would restore all servers.
			}
			p.ContextServers = &ids
		}
		a.Profiles = append(a.Profiles, p)
		out[r.Agent] = a
	}
	return out, nil
}

// envText writes an environment as NAME=value lines, sorted.
func envText(env map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// parseEnv reads NAME=value lines; blank lines and # comments are skipped.
func parseEnv(s string) (map[string]string, error) {
	var env map[string]string
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf(T("line %d: write it as NAME=value"), i+1)
		}
		if env == nil {
			env = map[string]string{}
		}
		env[k] = v
	}
	return env, nil
}

// targetLabel shows a launch target as the pickers and tabs name it.
func targetLabel(target string) string {
	agent, profile := agentspec.SplitTarget(target)
	if profile == "" {
		return agent
	}
	return agent + " · " + profile
}

// sessionTitle is the title a session's program set, or its label.
func sessionTitle(s *protocol.SessionInfo, title string) string {
	if s.GetName() != "" {
		return s.GetName()
	}
	if title != "" {
		return title
	}
	return sessionLabel(s)
}

func sessionLabel(s *protocol.SessionInfo) string {
	return targetLabel(agentspec.Target(s.GetAgent(), s.GetProfile()))
}

// nodeTargets are what can be launched on a node: what it advertises, or,
// for a daemon too old to, this node's own agents.
func nodeTargets(nodes []*protocol.Node, nodeID string, local []string) []string {
	for _, n := range nodes {
		if n.GetId() != nodeID || len(n.GetAgents()) == 0 {
			continue
		}
		var out []string
		for _, a := range n.GetAgents() {
			out = append(out, a.GetName())
			for _, p := range a.GetProfiles() {
				out = append(out, agentspec.Target(a.GetName(), p))
			}
		}
		return out
	}
	return local
}

// Editing ---------------------------------------------------------------------

// agentField binds one field of the draft row id.
func (m *model) agentField(id int, f func(*agentDraft) *string) ggui.Binding[string] {
	return ggui.Bind(func() string {
		for _, r := range m.AgentDraft.Get() {
			if r.ID == id {
				return *f(&r)
			}
		}
		return ""
	}, func(v string) {
		rows := slices.Clone(ggui.Peek(m.AgentDraft))
		for i := range rows {
			if rows[i].ID == id {
				*f(&rows[i]) = v
			}
		}
		m.AgentDraft.Set(rows)
	})
}

func (m *model) agentBoolField(id int, f func(*agentDraft) *bool) ggui.Binding[bool] {
	return ggui.Bind(func() bool {
		for _, r := range m.AgentDraft.Get() {
			if r.ID == id {
				return *f(&r)
			}
		}
		return false
	}, func(v bool) {
		rows := slices.Clone(ggui.Peek(m.AgentDraft))
		for i := range rows {
			if rows[i].ID == id {
				*f(&rows[i]) = v
			}
		}
		m.AgentDraft.Set(rows)
	})
}

func (m *model) nextAgentID() int {
	id := 0
	for _, r := range ggui.Peek(m.AgentDraft) {
		id = max(id, r.ID)
	}
	return id + 1
}

// addProfile adds a profile to agent after its last one, named so it does
// not clash.
func (m *model) addProfile(agent string) {
	rows := slices.Clone(ggui.Peek(m.AgentDraft))
	at, taken := 0, map[string]bool{}
	for i, r := range rows {
		if r.Agent == agent {
			at = i + 1
			taken[r.Profile] = true
		}
	}
	name := "profile"
	for n := 2; taken[name]; n++ {
		name = "profile" + strconv.Itoa(n)
	}
	row := agentDraft{ID: m.nextAgentID(), Agent: agent, Profile: name, IsProfile: true, AllowAgent: true, AllowTemplate: true}
	m.AgentDraft.Set(slices.Insert(rows, at, row))
}

// addAgent adds an agent the node can launch by name.
func (m *model) addAgent(name string) error {
	name = strings.TrimSpace(name)
	if !agentspec.ValidName(name) {
		return errors.New(T("an agent's name has no spaces or colons"))
	}
	rows := ggui.Peek(m.AgentDraft)
	if slices.ContainsFunc(rows, func(r agentDraft) bool { return r.Agent == name }) {
		return fmt.Errorf(T("there is already an agent named %s"), name)
	}
	m.AgentDraft.Set(append(slices.Clone(rows), agentDraft{ID: m.nextAgentID(), Agent: name, Command: name, AllowAgent: true, AllowTemplate: true}))
	return nil
}

// removeAgentRow drops a profile, or an agent with its profiles.
func (m *model) removeAgentRow(id int) {
	rows := ggui.Peek(m.AgentDraft)
	i := slices.IndexFunc(rows, func(r agentDraft) bool { return r.ID == id })
	if i < 0 {
		return
	}
	gone := rows[i]
	m.AgentDraft.Set(slices.DeleteFunc(slices.Clone(rows), func(r agentDraft) bool {
		return r.ID == id || (!gone.IsProfile && r.Agent == gone.Agent)
	}))
}

// Page ------------------------------------------------------------------------

func agentSettings(m *model) ggui.Widget {
	// One agent unfolds at a time, by name, and stays so as rows come and
	// go; beside the list, on a wide page, the one open shows, or the
	// first while none is.
	open := ggui.State("")
	shown := ggui.Derived(func() string {
		rows := m.AgentDraft.Get()
		o := open.Get()
		if slices.ContainsFunc(rows, func(r agentDraft) bool { return !r.IsProfile && r.Agent == o }) {
			return o
		}
		for _, r := range rows {
			if !r.IsProfile {
				return r.Agent
			}
		}
		return ""
	})
	// The editors are rebuilt when rows come or go, not as their text is
	// typed, so a field keeps its focus.
	// An agent whose program is not installed moves to a card of its own,
	// below the ones that can start.
	shape := ggui.Derived(func() string {
		var b strings.Builder
		for _, r := range m.AgentDraft.Get() {
			fmt.Fprintf(&b, "%d", r.ID)
			if !agentReady(r) {
				b.WriteByte('-')
			}
			b.WriteByte(',')
		}
		return b.String()
	})

	name := ggui.State("")
	note := ggui.State("")
	add := func() {
		n := strings.TrimSpace(ggui.Peek(name))
		if err := m.addAgent(n); err != nil {
			note.Set(err.Error())
			return
		}
		name.Set("")
		note.Set("")
		open.Set(n)
	}
	addAgent := func(compact bool) ggui.Widget {
		return card(addField(T("Add an agent"), T("Any program that runs in a terminal, by a name sessions start it with."),
			"name", T("New agent name"), name, note, add, compact))
	}

	agents := ggui.Key(shape, func(string) ggui.Widget {
		groups := agentDraftGroups(ggui.Peek(m.AgentDraft))
		list := func(inline bool) ggui.Widget {
			var ready, missing []ggui.Widget
			for _, g := range groups {
				item := agentItem(m, open, shown, g.agent, g.profiles, inline)
				if agentReady(g.agent) {
					ready = append(ready, item)
				} else {
					missing = append(missing, item)
				}
			}
			var out []ggui.Widget
			if len(ready) > 0 {
				out = append(out, card(ready...))
			}
			if len(missing) > 0 {
				out = append(out, section(T("Not installed"), card(missing...)))
			}
			return widget.Column(append(out, addAgent(!inline))...).Gap(pick[float64](inline, pageGap, 20)).Stretch()
		}
		detail := ggui.Key(shown, func(name string) ggui.Widget {
			for _, g := range groups {
				if g.agent.Agent == name {
					return agentDetail(m, g.agent, g.profiles)
				}
			}
			return widget.Box()
		})
		return listBeside(list(false), detail, list(true))
	})

	return listPage(T("Agents"), T("The programs a session can start on this node, the flags and environment each starts with, and profiles: named variations of an agent, such as claude on another model or account. A session picks one as agent:profile. Changes apply to the next session started."),
		isolateHomeCard(m),
		builtinMCPCard(m),
		agents,
		saveBar(m),
	)
}

func builtinMCPCard(m *model) ggui.Widget {
	on := ggui.Bind(func() bool {
		return m.ext.MCPServers.Get()["mako"].On()
	}, func(v bool) {
		next := ggui.Peek(m.ext.MCPServers)["mako"]
		next.Enabled = nil
		if !v {
			next.Enabled = &v
		}
		m.saveMCPServer("mako", next)
	})
	return card(row(
		T("Built-in Mako MCP"),
		T("Give supported agents built-in tools to spawn teammates, exchange messages, show UI blocks and propose layouts. Changes apply immediately to running sessions unless a profile overrides MCP servers."),
		ui.Switch(on, "").Name(T("Built-in Mako MCP")),
	))
}

// agentEntry is an agent's row of the draft and its profiles' after it.
type agentEntry struct {
	agent    agentDraft
	profiles []agentDraft
}

// agentDraftGroups are the draft's agents, each with its profiles.
func agentDraftGroups(rows []agentDraft) []agentEntry {
	var out []agentEntry
	for i := 0; i < len(rows); {
		j := i + 1
		for j < len(rows) && rows[j].IsProfile && rows[j].Agent == rows[i].Agent {
			j++
		}
		out = append(out, agentEntry{rows[i], rows[i+1 : j]})
		i = j
	}
	return out
}

// agentItem is an agent's line in the list: its glyph, its name, what it
// runs and its profiles, and whether it is offered. Inline, tapped, it
// unfolds its settings and its profiles under it; otherwise it is the one
// shown beside the list.
func agentItem(m *model, open *ggui.StateValue[string], shown ggui.Readable[string], a agentDraft, profiles []agentDraft, inline bool) ggui.Widget {
	name := a.Agent
	offered := agentOffered(m, a.ID, name)
	summary := ggui.Reactive(func() ggui.Widget {
		d := m.agentRow(a.ID)
		text := strings.TrimSpace(d.Command + " " + d.Args)
		if d.Command == "" {
			text = strings.TrimSpace(strings.Join(agentspec.Builtin[name], " ") + " " + d.Args)
		}
		if n := m.profileCount(name); n > 0 {
			text += " · " + Tf("%d profiles", n)
		}
		if d.Yolo {
			text += " · YOLO"
		}
		return widget.Text(text).Size(11.5).Color(skin.ExtraMuted).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()
	})
	label := []ggui.Widget{
		agentGlyph(name, skin.Muted).Size(15),
		widget.Text(name).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
		widget.Expanded(summary),
	}
	if !inline {
		chosen := ggui.Map(shown, func(s string) bool { return s == name })
		return widget.Box(widget.Row(
			widget.Expanded(listItem(widget.Row(label...).Gap(10).Align(widget.AlignCenter), chosen, func() { open.Set(name) }, Tf("%s settings", name))),
			offered,
		).Gap(10).Align(widget.AlignCenter)).Pad(4, 8)
	}
	isOpen := ggui.Map(open, func(o string) bool { return o == name })
	head := widget.Row(
		widget.Expanded(ui.ButtonOf(widget.Row(append(label, chevron(isOpen, 13, skin.ExtraMuted))...).Gap(10).Align(widget.AlignCenter),
			func() { open.Set(pick(ggui.Peek(open) == name, "", name)) }).
			Ghost().Pad(8, 10).Radius(7).Name(Tf("%s settings", name))),
		offered,
	).Gap(10).Align(widget.AlignCenter)
	return widget.Box(widget.Column(
		head,
		reveal(isOpen, func() ggui.Widget {
			return widget.Padding(agentEditor(m, a, profiles), 6, 10, 10, 35)
		}),
	).Stretch()).Pad(4, 8)
}

// agentDetail is the agent shown beside the list: its name over its
// settings and its profiles.
func agentDetail(m *model, a agentDraft, profiles []agentDraft) ggui.Widget {
	return detailCard(widget.Row(
		agentGlyph(a.Agent, skin.Muted).Size(18),
		widget.Text(a.Agent).Size(16).Weight(ggui.WeightSemibold).NoWrap(),
	).Gap(10).Align(widget.AlignCenter), agentEditor(m, a, profiles))
}

// agentEditor is an unfolded agent's settings: its command, flags and
// environment, then its profiles, and what adds a profile or removes it.
func agentEditor(m *model, a agentDraft, profiles []agentDraft) ggui.Widget {
	m.ensureAgentCatalog(a.Agent)
	_, builtin := agentspec.Builtin[a.Agent]
	commandHint := T("The program and any arguments it always needs.")
	if builtin {
		commandHint = Tf("The program and any arguments it always needs; empty runs %s.", strings.Join(agentspec.Builtin[a.Agent], " "))
	}
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
	children := []ggui.Widget{
		compactField(T("Command"), commandHint,
			ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Command })).
				Style(mono).Name(Tf("%s command", a.Agent))),
		compactField(T("Flags"), T("Added to every session of it, before a profile's. Quote as in a shell."),
			ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Args })).
				Style(mono).Placeholder("--flag value").Name(Tf("%s flags", a.Agent))),
		compactField(T("Environment"), T("One per line as NAME=value, added to the daemon's. $NAME, ${NAME} and a leading ~ expand when a session starts; $$ is a $."),
			ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Env })).
				Multiline().Lines(2).Style(mono).Name(Tf("%s environment", a.Agent))),
	}
	if _, supportsYolo := agents.As[agent.YoloFlag](a.Agent); supportsYolo {
		yoloSwitch := ui.Switch(m.agentBoolField(a.ID, func(d *agentDraft) *bool { return &d.Yolo }), "").
			Name(Tf("%s YOLO", a.Agent))
		yoloRow := widget.Box(widget.Row(
			icon("shield-alert").Size(15).Color(skin.DotWaiting),
			widget.Expanded(widget.Column(
				widget.Text(T("YOLO mode")).Size(13).Weight(ggui.WeightMedium),
				muted(widget.Text(T("Bypass permission prompts and sandbox confirmations automatically.")).Size(11.5)),
			).Gap(2).Stretch()),
			yoloSwitch,
		).Gap(10).Align(widget.AlignCenter)).Fill(skin.Surface0).Border(1, skin.Border).Radius(8).Pad(10, 12)
		children = append(children, yoloRow)
	}
	children = append(children, executionSwitches(m, a.ID, a.Agent, false))
	for _, p := range profiles {
		children = append(children, profileCard(m, p, mono))
	}
	actions := []ggui.Widget{
		ui.ButtonOf(widget.Row(icon("plus").Size(14), widget.Text(T("Add profile")).Size(13)).Gap(6),
			func() { m.addProfile(a.Agent) }).Ghost().Pad(6, 10).Radius(7).Name(Tf("Add %s profile", a.Agent)),
		widget.Spacer(),
	}
	if !builtin {
		id := a.ID
		actions = append(actions, ui.Button(T("Remove agent"), func() { m.removeAgentRow(id) }).Ghost().Name(Tf("Remove %s", a.Agent)))
	}
	children = append(children, widget.Row(actions...).Gap(8))
	return widget.Column(children...).Gap(10).Stretch()
}

// compactField is a field under its label, its hint as the label's
// tooltip, for the list's unfolded agent.
func compactField(label, hint string, field ggui.Widget) ggui.Widget {
	return widget.Column(
		ui.Tooltip(widget.Text(label).Size(12).Color(skin.Muted), hint),
		field,
	).Gap(4).Stretch()
}

// agentRow is the draft's row id, its zero value once gone. Reactive.
func (m *model) agentRow(id int) agentDraft {
	for _, r := range m.AgentDraft.Get() {
		if r.ID == id {
			return r
		}
	}
	return agentDraft{}
}

// profileCount is how many profiles the draft gives agent. Reactive.
func (m *model) profileCount(agent string) int {
	n := 0
	for _, r := range m.AgentDraft.Get() {
		if r.IsProfile && r.Agent == agent {
			n++
		}
	}
	return n
}

// agentOffered is an agent's switch, whether new sessions can start it.
// Until the user sets it, it follows whether the program is installed, as
// its command now reads.
func agentOffered(m *model, id int, name string) ggui.Widget {
	installed := ggui.Derived(func() bool { return draftInstalled(m.agentRow(id)) })
	on := ggui.Bind(func() bool {
		switch m.agentRow(id).Enabled {
		case "on":
			return true
		case "off":
			return false
		}
		return installed.Get()
	}, func(v bool) {
		rows := slices.Clone(ggui.Peek(m.AgentDraft))
		for i := range rows {
			if rows[i].ID == id {
				rows[i].Enabled = pick(v, "on", "off")
			}
		}
		m.AgentDraft.Set(rows)
	})
	hint := T("Offered to start sessions with, in the new session dialog and beside the +.")
	return ui.Tooltip(ui.Switch(on, "").Name(Tf("Offer %s", name)), hint)
}

// agentReady reports whether the row r goes in the card of agents that
// can start: a profile goes with its agent.
func agentReady(r agentDraft) bool { return r.IsProfile || draftInstalled(r) }

// draftInstalled reports whether the program of the agent row d, as its
// command now reads, is installed.
func draftInstalled(d agentDraft) bool {
	command, err := agentspec.SplitArgs(d.Command)
	if err != nil {
		return false
	}
	if len(command) == 0 {
		command = agentspec.Builtin[d.Agent]
	}
	return agentspec.Agent{Command: command}.Installed()
}

// profileCard is a profile of an unfolded agent: its name, which removes
// it beside, what it is for, its flags and environment over the agent's,
// and the MCP servers its sessions get.
func profileCard(m *model, p agentDraft, mono ggui.TextStyle) ggui.Widget {
	id := p.ID
	target := p.Agent + ":" + p.Profile
	nameField := ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Profile })).
		Style(mono).Name(Tf("%s profile name", p.Agent))
	head := widget.Row(
		icon("layers").Size(14).Color(skin.Muted),
		muted(widget.Text(p.Agent+":").Size(13).Style(mono)),
		widget.Box(nameField).Width(180),
		widget.Spacer(),
		ui.ButtonOf(icon("trash-2").Size(14), func() { m.removeAgentRow(id) }).Ghost().Pad(6).Radius(6).
			Name(Tf("Remove %s", target)),
	).Gap(8).Align(widget.AlignCenter)
	return widget.Box(widget.Column(
		head,
		compactField(T("Description"), T("Describe its specialties and when to choose it. Agents use this to select a profile when delegating work."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Description })).
				Multiline().Lines(2).Placeholder(T("For example: Reviews Go concurrency, cancellation and race conditions.")).
				Name(Tf("%s description", target))),
		profileModelAndEffort(m, p, target, mono),
		profileYoloRow(m, id, p.Agent, target),
		executionSwitches(m, id, target, true),
		compactField(T("Flags"), T("Added after the agent's own."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Args })).
				Style(mono).Placeholder("--flag value").Name(Tf("%s flags", target))),
		compactField(T("Environment"), T("NAME=value lines over the agent's own, which $NAME reads."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Env })).
				Multiline().Lines(2).Style(mono).Name(Tf("%s environment", target))),
		profileServers(m, id, target),
	).Gap(10).Stretch()).Border(1, skin.Border).Radius(8).Pad(10, 12)
}

func profileYoloRow(m *model, id int, agentName, target string) ggui.Widget {
	if _, ok := agents.As[agent.YoloFlag](agentName); !ok {
		return widget.Box()
	}
	ctrl := widget.Box(ui.Select(m.agentField(id, func(d *agentDraft) *string { return &d.ProfileYolo })).
		Options([]string{"", "on", "off"}).
		Format(func(v string) string {
			switch v {
			case "on":
				return T("Enabled")
			case "off":
				return T("Disabled")
			default:
				return T("Inherit from agent")
			}
		}).
		Name(Tf("%s YOLO", target))).Width(200)
	return compactField(T("YOLO mode"), T("Override the agent's YOLO setting for this profile."), ctrl)
}

func executionSwitches(m *model, id int, target string, isProfile bool) ggui.Widget {
	agentDesc := T("Let AI models discover and spawn this agent's default configuration via MCP.")
	tmplDesc := T("Let prompt templates execute this agent's default configuration via mako.ask or mako.spawn.")
	if isProfile {
		agentDesc = T("Let AI models discover and spawn this profile via MCP.")
		tmplDesc = T("Let prompt templates execute this profile via mako.ask or mako.spawn.")
	}
	agentSwitch := widget.Row(
		widget.Expanded(widget.Column(
			widget.Text(T("Allow agent spawning")).Size(12.5).Weight(ggui.WeightMedium),
			muted(widget.Text(agentDesc).Size(11.5)),
		).Gap(2).Stretch()),
		ui.Switch(m.agentBoolField(id, func(d *agentDraft) *bool { return &d.AllowAgent }), "").
			Name(Tf("%s allow agent spawning", target)),
	).Gap(10).Align(widget.AlignCenter)
	templateSwitch := widget.Row(
		widget.Expanded(widget.Column(
			widget.Text(T("Allow in templates")).Size(12.5).Weight(ggui.WeightMedium),
			muted(widget.Text(tmplDesc).Size(11.5)),
		).Gap(2).Stretch()),
		ui.Switch(m.agentBoolField(id, func(d *agentDraft) *bool { return &d.AllowTemplate }), "").
			Name(Tf("%s allow in templates", target)),
	).Gap(10).Align(widget.AlignCenter)
	return widget.Box(widget.Column(agentSwitch, hline(), templateSwitch).Gap(8).Stretch()).
		Fill(skin.Surface0).Border(1, skin.Border).Radius(8).Pad(10, 12)
}

// mcpServerIDs are the MCP servers this node has: the built-in mako server,
// installed extensions' servers, and those configured with a command, sorted.
func (m *model) mcpServerIDs() []string {
	ids := []string{"mako"}
	for _, e := range m.ext.Installed.Get() {
		ids = append(ids, e.ContextServerIDs()...)
	}
	for id, s := range m.ext.MCPServers.Get() {
		if s.Command != nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// profileServers chooses the MCP servers a profile's sessions get: every
// one enabled, or those ticked. It shows only when there are servers.
func profileServers(m *model, id int, name string) ggui.Widget {
	draft := func() agentDraft {
		for _, r := range ggui.Peek(m.AgentDraft) {
			if r.ID == id {
				return r
			}
		}
		return agentDraft{}
	}
	update := func(f func(*agentDraft)) {
		rows := slices.Clone(ggui.Peek(m.AgentDraft))
		for i := range rows {
			if rows[i].ID == id {
				f(&rows[i])
			}
		}
		m.AgentDraft.Set(rows)
	}
	return ggui.Reactive(func() ggui.Widget {
		m.AgentDraft.Get()
		d := draft()
		ids := m.mcpServerIDs()
		picked := lines(d.Servers)
		for _, p := range picked { // one since removed stays, to untick
			if !slices.Contains(ids, p) {
				ids = append(ids, p)
			}
		}
		if len(ids) == 0 {
			return widget.Box()
		}
		pick := ggui.Bind(func() bool { return draft().PickServers }, func(v bool) {
			update(func(d *agentDraft) {
				d.PickServers = v
				if v && d.Servers == "" { // start from what it gets now
					var on []string
					servers := ggui.Peek(m.ext.MCPServers)
					for _, s := range m.mcpServerIDs() {
						if servers[s].On() {
							on = append(on, s)
						}
					}
					d.Servers = strings.Join(on, "\n")
				}
			})
		})
		hint := T("Every MCP server enabled for this node.")
		if d.PickServers {
			hint = T("Only the servers ticked, even one turned off for the node.")
		}
		rows := []ggui.Widget{widget.Row(
			widget.Expanded(ui.Tooltip(widget.Text(T("Choose its MCP servers")).Size(12).Color(skin.Muted), hint)),
			ui.Switch(pick, "").Name(Tf("%s chooses its MCP servers", name)),
		).Gap(8).Align(widget.AlignCenter)}
		if d.PickServers {
			boxes := make([]ggui.Widget, 0, len(ids))
			for _, s := range ids {
				on := ggui.Bind(func() bool { return slices.Contains(lines(draft().Servers), s) }, func(v bool) {
					update(func(d *agentDraft) {
						cur := slices.DeleteFunc(lines(d.Servers), func(x string) bool { return x == s })
						if v {
							cur = append(cur, s)
							slices.Sort(cur)
						}
						d.Servers = strings.Join(cur, "\n")
					})
				})
				boxes = append(boxes, ui.Checkbox(on, s))
			}
			rows = append(rows, widget.Column(boxes...).Gap(8).Stretch())
		}
		return widget.Column(rows...).Gap(8).Stretch()
	})
}

func equalCatalogMaps(a, b map[string][]*protocol.AgentModel) bool {
	return maps.EqualFunc(a, b, func(xs, ys []*protocol.AgentModel) bool {
		return slices.EqualFunc(xs, ys, func(x, y *protocol.AgentModel) bool { return proto.Equal(x, y) })
	})
}

func (m *model) ensureAgentCatalog(agentName string) {
	if agentName == "" {
		return
	}
	if len(ggui.Peek(m.AgentCatalogs)[agentName]) > 0 {
		return
	}
	if m.catalogAsked[agentName] {
		return
	}
	entry, ok := agents.Lookup(agentName)
	if !ok {
		return
	}
	if _, hasCat := entry.Driver.(agent.ModelCatalog); !hasCat {
		if _, hasModels := entry.Driver.(agent.Models); !hasModels {
			return
		}
	}
	m.catalogAsked[agentName] = true
	m.loadAgentCatalog(agentName, false)
}

func (m *model) loadAgentCatalog(agentName string, refresh bool) {
	if agentName == "" {
		return
	}
	m.catalogAsked[agentName] = true
	if refresh {
		m.setCatalogLoading(agentName, true)
	}
	linked := m.link != nil && m.link.isLinked()
	cfgAgents := maps.Clone(m.cfg.Agents)
	isolate := ggui.Peek(m.IsolateHome)
	ggui.Async(func() ([]*protocol.AgentModel, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if linked {
			c, err := m.link.client(ctx)
			if err == nil {
				res, err := c.ListAgentModels(ctx, &protocol.ListAgentModels{Agent: agentName, Refresh: refresh})
				if err == nil {
					return res.GetModels(), nil
				}
			}
		}
		return localAgentCatalog(ctx, agentName, isolate, cfgAgents, refresh)
	}, func(models []*protocol.AgentModel, err error) {
		if refresh {
			m.setCatalogLoading(agentName, false)
		}
		if err != nil {
			if refresh {
				m.toaster.Push(ui.Toast(Tf("Could not load %s models", agentName), err.Error()).Destructive())
			}
			return
		}
		m.AgentCatalogs.Update(func(prev map[string][]*protocol.AgentModel) map[string][]*protocol.AgentModel {
			next := maps.Clone(prev)
			if next == nil {
				next = map[string][]*protocol.AgentModel{}
			}
			next[agentName] = models
			return next
		})
	})
}

func (m *model) setCatalogLoading(agentName string, loading bool) {
	m.AgentCatalogLoading.Update(func(prev map[string]bool) map[string]bool {
		next := maps.Clone(prev)
		if next == nil {
			next = map[string]bool{}
		}
		if loading {
			next[agentName] = true
		} else {
			delete(next, agentName)
		}
		return next
	})
}

func localAgentCatalog(ctx context.Context, agentName string, isolate bool, configured map[string]agentspec.Agent, refresh bool) ([]*protocol.AgentModel, error) {
	entry, ok := agents.Lookup(agentName)
	if !ok {
		return nil, nil
	}
	env := agentspec.IsolatedHomeEnvForAgent(agentName, isolate, configured)
	if cat, ok := entry.Driver.(agent.ModelCatalog); ok {
		got, err := cat.Catalog(ctx, env, refresh)
		if err != nil {
			return nil, err
		}
		out := make([]*protocol.AgentModel, len(got))
		for i, info := range got {
			out[i] = &protocol.AgentModel{
				Id:       info.ID,
				Name:     info.Name,
				Efforts:  slices.Clone(info.Efforts),
				Overflow: info.Overflow,
			}
		}
		return out, nil
	}
	if known, ok := entry.Driver.(agent.Models); ok {
		efforts := known.Efforts()
		var out []*protocol.AgentModel
		for _, id := range known.Models() {
			out = append(out, &protocol.AgentModel{Id: id, Name: id, Efforts: slices.Clone(efforts)})
		}
		return out, nil
	}
	return nil, nil
}

// profileModelAndEffort is the model and reasoning effort a profile's
// sessions start with.
func profileModelAndEffort(m *model, p agentDraft, target string, mono ggui.TextStyle) ggui.Widget {
	entry, _ := agents.Lookup(p.Agent)
	modelFlag, okModel := entry.Driver.(agent.ModelFlag)
	hasModel := okModel && modelFlag.ModelArgs("x") != nil
	effortFlag, okEffort := entry.Driver.(agent.EffortFlag)
	hasEffort := okEffort && effortFlag.EffortArgs("high") != nil
	if !hasModel && !hasEffort {
		return widget.Box()
	}
	_, hasCatalog := entry.Driver.(agent.ModelCatalog)

	return ggui.Reactive(func() ggui.Widget {
		catalog := m.AgentCatalogs.Get()[p.Agent]
		loading := m.AgentCatalogLoading.Get()[p.Agent]
		row := m.agentRow(p.ID)
		if row.ID == 0 {
			return widget.Box()
		}
		models := catalog
		if len(models) == 0 {
			if known, ok := entry.Driver.(agent.Models); ok {
				efforts := known.Efforts()
				for _, id := range known.Models() {
					models = append(models, &protocol.AgentModel{Id: id, Name: id, Efforts: efforts})
				}
			}
		}

		var modelCol, effortCol ggui.Widget
		if hasModel {
			hint := T("The model its sessions start on, over the agent's default and any its flags name.")
			var modelCtrl ggui.Widget
			if len(models) == 0 {
				modelBind := m.agentField(p.ID, func(d *agentDraft) *string { return &d.Model })
				modelCtrl = ui.TextField(modelBind).Style(mono).Placeholder(T("The agent's default")).Name(Tf("%s model", target))
			} else {
				options := []string{""}
				labels := map[string]string{"": T("The agent's default")}
				var overflow []string
				for _, am := range models {
					id := am.GetId()
					if id == "" {
						continue
					}
					label := id
					if am.GetName() != "" && am.GetName() != id {
						label = am.GetName()
					}
					labels[id] = label
					if am.GetOverflow() {
						overflow = append(overflow, id)
					} else if !slices.Contains(options, id) {
						options = append(options, id)
					}
				}
				for _, id := range overflow {
					if !slices.Contains(options, id) {
						options = append(options, id)
					}
				}
				if row.Model != "" && !slices.Contains(options, row.Model) {
					options = append(options, row.Model)
					labels[row.Model] = row.Model
				}
				modelBind := ggui.Bind(func() string {
					return m.agentRow(p.ID).Model
				}, func(v string) {
					rows := slices.Clone(ggui.Peek(m.AgentDraft))
					for i := range rows {
						if rows[i].ID == p.ID {
							rows[i].Model = v
							if v != "" {
								if found := findAgentModel(models, v); found != nil {
									if len(found.GetEfforts()) == 0 || (rows[i].Effort != "" && !slices.Contains(found.GetEfforts(), rows[i].Effort)) {
										rows[i].Effort = ""
									}
								}
							}
						}
					}
					m.AgentDraft.Set(rows)
				})
				modelCtrl = ui.Select(modelBind).Options(options).Format(func(s string) string {
					if l, ok := labels[s]; ok {
						return l
					}
					return s
				}).Name(Tf("%s model", target))
			}
			var ctrlRow ggui.Widget = modelCtrl
			if hasCatalog {
				refreshBtn := ui.Tooltip(ui.ButtonOf(icon("refresh-cw").Size(13).Color(skin.Muted), func() {
					m.loadAgentCatalog(p.Agent, true)
				}).Ghost().Pad(6).Radius(6).BindDisabled(ggui.Const(loading)).Name(Tf("Refresh %s models", p.Agent)), T("Refresh models"))
				ctrlRow = widget.Row(widget.Expanded(modelCtrl), refreshBtn).Gap(4).Align(widget.AlignCenter)
			}
			modelCol = compactField(T("Model"), hint, ctrlRow)
		}

		if hasEffort {
			efforts := effortChoices(models, row.Model, entry.Driver)
			if len(efforts) > 0 || row.Effort != "" {
				options := append([]string{""}, efforts...)
				if row.Effort != "" && !slices.Contains(options, row.Effort) {
					options = append(options, row.Effort)
				}
				effortBind := m.agentField(p.ID, func(d *agentDraft) *string { return &d.Effort })
				effortHint := T("The reasoning effort its sessions start with, over the agent's default.")
				effortCtrl := ui.Select(effortBind).Options(options).Format(func(s string) string {
					if s == "" {
						return T("The agent's default")
					}
					return capitalized(s)
				}).Name(Tf("%s effort", target))
				effortCol = compactField(T("Effort"), effortHint, effortCtrl)
			}
		}

		switch {
		case modelCol != nil && effortCol != nil:
			return widget.Row(
				widget.Expanded(modelCol),
				widget.Box(effortCol).Width(180),
			).Gap(12).Align(widget.AlignStart)
		case modelCol != nil:
			return widget.Box(modelCol).MaxWidth(280)
		case effortCol != nil:
			return widget.Box(effortCol).Width(180)
		default:
			return widget.Box()
		}
	})
}

func findAgentModel(models []*protocol.AgentModel, id string) *protocol.AgentModel {
	for _, am := range models {
		if am.GetId() == id {
			return am
		}
	}
	return nil
}

func effortChoices(models []*protocol.AgentModel, selectedModel string, driver agent.Driver) []string {
	if selectedModel != "" {
		if found := findAgentModel(models, selectedModel); found != nil {
			return slices.Clone(found.GetEfforts())
		}
		if norm, ok := driver.(agent.ModelEffortNormalizer); ok {
			for _, probe := range []string{"low", "high"} {
				if base, _ := norm.NormalizeModelEffort(selectedModel, probe); base != "" && base != selectedModel {
					if found := findAgentModel(models, base); found != nil {
						return slices.Clone(found.GetEfforts())
					}
					break
				}
			}
		}
	}
	var out []string
	// Collect efforts from main models first, then overflow models.
	for _, pass := range []bool{false, true} {
		for _, am := range models {
			if am.GetOverflow() != pass {
				continue
			}
			for _, e := range am.GetEfforts() {
				if e != "" && !slices.Contains(out, e) {
					out = append(out, e)
				}
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if known, ok := driver.(agent.Models); ok {
		return slices.Clone(known.Efforts())
	}
	return []string{"low", "medium", "high", "xhigh", "max"}
}
