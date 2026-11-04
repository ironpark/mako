package gui

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agentspec"
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
	IsProfile bool // a profile's name may be blank while it is typed
	// PickServers gives a profile's sessions the MCP servers in Servers,
	// one ID a line, rather than every one enabled.
	PickServers bool
	Servers     string
}

// draftAgents lays out cfg's agents for editing: every agent the node can
// launch, built-ins included, each followed by its profiles.
func draftAgents(agents map[string]agentspec.Agent) []agentDraft {
	var out []agentDraft
	for _, name := range agentspec.Names(agents) {
		a, _ := agentspec.Lookup(name, agents)
		d := agentDraft{ID: len(out) + 1, Agent: name,
			Command: agentspec.JoinArgs(a.Command), Args: agentspec.JoinArgs(a.Args), Env: envText(a.Env)}
		if a.Enabled != nil {
			d.Enabled = pick(*a.Enabled, "on", "off")
		}
		out = append(out, d)
		for _, p := range a.Profiles {
			d := agentDraft{ID: len(out) + 1, Agent: name, Profile: p.Name, IsProfile: true,
				Description: p.Description, Args: agentspec.JoinArgs(p.Args), Env: envText(p.Env)}
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
		a := agentspec.Agent{Command: command, Args: args, Env: env}
		if r.Enabled != "" {
			on := r.Enabled == "on"
			a.Enabled = &on
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
		p := agentspec.Profile{Name: r.Profile, Description: r.Description, Args: args, Env: env}
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
	row := agentDraft{ID: m.nextAgentID(), Agent: agent, Profile: name, IsProfile: true}
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
	m.AgentDraft.Set(append(slices.Clone(rows), agentDraft{ID: m.nextAgentID(), Agent: name, Command: name}))
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
	// go.
	open := ggui.State("")
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
	agents := ggui.Key(shape, func(string) ggui.Widget {
		rows := ggui.Peek(m.AgentDraft)
		var ready, missing []ggui.Widget
		for i := 0; i < len(rows); {
			j := i + 1
			for j < len(rows) && rows[j].IsProfile && rows[j].Agent == rows[i].Agent {
				j++
			}
			item := agentItem(m, open, rows[i], rows[i+1:j])
			if agentReady(rows[i]) {
				ready = append(ready, item)
			} else {
				missing = append(missing, item)
			}
			i = j
		}
		var out []ggui.Widget
		if len(ready) > 0 {
			out = append(out, card(ready...))
		}
		if len(missing) > 0 {
			out = append(out, section(T("Not installed"), card(missing...)))
		}
		return ggui.Column(out...).Gap(28).Stretch()
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
	hasNote := ggui.Map(note, func(s string) bool { return s != "" })
	addCard := card(
		row(T("Add an agent"), T("Any program that runs in a terminal, by a name sessions start it with."),
			ggui.Row(
				ggui.Box(ui.TextField(name).Placeholder("name").Name(T("New agent name")).OnSubmit(func(string) { add() })).Width(160),
				ui.Button(T("Add"), add).Outline(),
			).Gap(8)),
		ggui.If(hasNote, func() ggui.Widget {
			return ggui.Box(ggui.TextOf(note).Size(12).Color(tokDotDown)).Pad(0, 16, 12, 16)
		}),
	)

	return page(T("Agents"), T("The programs a session can start on this node, the flags and environment each starts with, and profiles: named variations of an agent, such as claude on another model or account. A session picks one as agent:profile. Changes apply to the next session started."),
		agents,
		addCard,
		saveBar(m),
	)
}

// agentItem is an agent's line in the list: its glyph, its name, what it
// runs and its profiles, and whether it is offered; tapped, it unfolds its
// settings and its profiles under it.
func agentItem(m *model, open *ggui.StateValue[string], a agentDraft, profiles []agentDraft) ggui.Widget {
	name := a.Agent
	offered := agentOffered(m, a.ID, name)
	isOpen := ggui.Map(open, func(o string) bool { return o == name })
	summary := ggui.Reactive(func() ggui.Widget {
		d := m.agentRow(a.ID)
		text := strings.TrimSpace(d.Command + " " + d.Args)
		if d.Command == "" {
			text = strings.TrimSpace(strings.Join(agentspec.Builtin[name], " ") + " " + d.Args)
		}
		if n := m.profileCount(name); n > 0 {
			text += " · " + Tf("%d profiles", n)
		}
		return ggui.Text(text).Size(11.5).Color(tokExtraMuted).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()
	})
	head := ggui.Row(
		ggui.Expanded(ui.ButtonOf(ggui.Row(
			agentGlyph(name, tokMuted).Size(15),
			ggui.Text(name).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Expanded(summary),
			ggui.View(isOpen, func(o bool) ggui.Widget {
				return icon("chevron-down").Size(13).Color(tokExtraMuted).Rotate(pick(o, 0.0, -math.Pi/2))
			}),
		).Gap(10).Align(ggui.AlignCenter), func() { open.Set(pick(ggui.Peek(open) == name, "", name)) }).
			Ghost().Pad(8, 10).Radius(7).Name(Tf("%s settings", name))),
		offered,
	).Gap(10).Align(ggui.AlignCenter)
	return ggui.Box(ggui.Column(
		head,
		ggui.If(isOpen, func() ggui.Widget { return agentEditor(m, a, profiles) }),
	).Stretch()).Pad(4, 8)
}

// agentEditor is an unfolded agent's settings: its command, flags and
// environment, then its profiles, and what adds a profile or removes it.
func agentEditor(m *model, a agentDraft, profiles []agentDraft) ggui.Widget {
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
	for _, p := range profiles {
		children = append(children, profileCard(m, p, mono))
	}
	actions := []ggui.Widget{
		ui.ButtonOf(ggui.Row(icon("plus").Size(14), ggui.Text(T("Add profile")).Size(13)).Gap(6),
			func() { m.addProfile(a.Agent) }).Ghost().Pad(6, 10).Radius(7).Name(Tf("Add %s profile", a.Agent)),
		ggui.Spacer(),
	}
	if !builtin {
		id := a.ID
		actions = append(actions, ui.Button(T("Remove agent"), func() { m.removeAgentRow(id) }).Ghost().Name(Tf("Remove %s", a.Agent)))
	}
	children = append(children, ggui.Row(actions...).Gap(8))
	return ggui.Padding(ggui.Column(children...).Gap(10).Stretch(), 6, 10, 10, 35)
}

// compactField is a field under its label, its hint as the label's
// tooltip, for the list's unfolded agent.
func compactField(label, hint string, field ggui.Widget) ggui.Widget {
	return ggui.Column(
		ui.Tooltip(ggui.Text(label).Size(12).Color(tokMuted), hint),
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
	head := ggui.Row(
		icon("layers").Size(14).Color(tokMuted),
		muted(ggui.Text(p.Agent+":").Size(13).Style(mono)),
		ggui.Box(nameField).Width(180),
		ggui.Spacer(),
		ui.ButtonOf(icon("trash-2").Size(14), func() { m.removeAgentRow(id) }).Ghost().Pad(6).Radius(6).
			Name(Tf("Remove %s", target)),
	).Gap(8).Align(ggui.AlignCenter)
	return ggui.Box(ggui.Column(
		head,
		compactField(T("Description"), T("Describe its specialties and when to choose it. Agents use this to select a profile when delegating work."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Description })).
				Multiline().Lines(2).Placeholder(T("For example: Reviews Go concurrency, cancellation and race conditions.")).
				Name(Tf("%s description", target))),
		compactField(T("Flags"), T("Added after the agent's own."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Args })).
				Style(mono).Placeholder("--model opus").Name(Tf("%s flags", target))),
		compactField(T("Environment"), T("NAME=value lines over the agent's own, which $NAME reads."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Env })).
				Multiline().Lines(2).Style(mono).Name(Tf("%s environment", target))),
		profileServers(m, id, target),
	).Gap(10).Stretch()).Border(1, tokBorder).Radius(8).Pad(10, 12)
}

// mcpServerIDs are the MCP servers this node has: the installed
// extensions' and those configured with a command, sorted.
func (m *model) mcpServerIDs() []string {
	var ids []string
	for _, e := range m.Extensions.Get() {
		ids = append(ids, e.ContextServerIDs()...)
	}
	for id, s := range m.MCPServers.Get() {
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
			return ggui.Box()
		}
		pick := ggui.Bind(func() bool { return draft().PickServers }, func(v bool) {
			update(func(d *agentDraft) {
				d.PickServers = v
				if v && d.Servers == "" { // start from what it gets now
					var on []string
					servers := ggui.Peek(m.MCPServers)
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
		rows := []ggui.Widget{ggui.Row(
			ggui.Expanded(ui.Tooltip(ggui.Text(T("Choose its MCP servers")).Size(12).Color(tokMuted), hint)),
			ui.Switch(pick, "").Name(Tf("%s chooses its MCP servers", name)),
		).Gap(8).Align(ggui.AlignCenter)}
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
			rows = append(rows, ggui.Column(boxes...).Gap(8).Stretch())
		}
		return ggui.Column(rows...).Gap(8).Stretch()
	})
}
