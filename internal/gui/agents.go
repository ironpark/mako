package gui

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/protocol"
)

// agentDraft is one row of the agents page's draft: an agent's own
// settings when Profile is "", else one of its profiles. Flags are edited
// as a command line and the environment as NAME=value lines, so the row
// stays comparable. ID keeps a row's editor in place while its name is
// typed.
type agentDraft struct {
	ID                 int
	Agent, Profile     string
	Command, Args, Env string
	IsProfile          bool // a profile's name may be blank while it is typed
}

// draftAgents lays out cfg's agents for editing: every agent the node can
// launch, built-ins included, each followed by its profiles.
func draftAgents(agents map[string]agentspec.Agent) []agentDraft {
	var out []agentDraft
	for _, name := range agentspec.Names(agents) {
		a, _ := agentspec.Lookup(name, agents)
		out = append(out, agentDraft{ID: len(out) + 1, Agent: name,
			Command: agentspec.JoinArgs(a.Command), Args: agentspec.JoinArgs(a.Args), Env: envText(a.Env)})
		for _, p := range a.Profiles {
			out = append(out, agentDraft{ID: len(out) + 1, Agent: name, Profile: p.Name, IsProfile: true,
				Args: agentspec.JoinArgs(p.Args), Env: envText(p.Env)})
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
		out[r.Agent] = agentspec.Agent{Command: command, Args: args, Env: env}
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
		a.Profiles = append(a.Profiles, agentspec.Profile{Name: r.Profile, Args: args, Env: env})
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

func sessionLabel(s protocol.SessionInfo) string {
	return targetLabel(agentspec.Target(s.Agent, s.Profile))
}

// nodeTargets are what can be launched on a node: what it advertises, or,
// for a daemon too old to, this node's own agents.
func nodeTargets(nodes []protocol.Node, nodeID string, local []string) []string {
	for _, n := range nodes {
		if n.ID != nodeID || len(n.Agents) == 0 {
			continue
		}
		var out []string
		for _, a := range n.Agents {
			out = append(out, a.Name)
			for _, p := range a.Profiles {
				out = append(out, agentspec.Target(a.Name, p))
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
	// The editors are rebuilt when rows come or go, not as their text is
	// typed, so a field keeps its focus.
	shape := ggui.Derived(func() string {
		var b strings.Builder
		for _, r := range m.AgentDraft.Get() {
			fmt.Fprintf(&b, "%d,", r.ID)
		}
		return b.String()
	})
	agents := ggui.Key(shape, func(string) ggui.Widget {
		rows := ggui.Peek(m.AgentDraft)
		var sections []ggui.Widget
		for i := 0; i < len(rows); {
			j := i + 1
			for j < len(rows) && rows[j].IsProfile && rows[j].Agent == rows[i].Agent {
				j++
			}
			sections = append(sections, agentSection(m, rows[i], rows[i+1:j]))
			i = j
		}
		return ggui.Column(sections...).Gap(28).Stretch()
	})

	name := ggui.State("")
	note := ggui.State("")
	add := func() {
		if err := m.addAgent(ggui.Peek(name)); err != nil {
			note.Set(err.Error())
			return
		}
		name.Set("")
		note.Set("")
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
		section(T("Other programs"), addCard),
		saveBar(m),
	)
}

// agentSection is an agent's card, followed by a card for each profile.
func agentSection(m *model, a agentDraft, profiles []agentDraft) ggui.Widget {
	_, builtin := agentspec.Builtin[a.Agent]
	commandHint := T("The program and any arguments it always needs.")
	if builtin {
		commandHint = Tf("The program and any arguments it always needs; empty runs %s.", strings.Join(agentspec.Builtin[a.Agent], " "))
	}
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
	children := []ggui.Widget{
		card(
			row(T("Command"), commandHint,
				ggui.Box(ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Command })).
					Style(mono).Name(Tf("%s command", a.Agent))).Width(300)),
			row(T("Flags"), T("Added to every session of it, before a profile's. Quote as in a shell."),
				ggui.Box(ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Args })).
					Style(mono).Placeholder("--flag value").Name(Tf("%s flags", a.Agent))).Width(300)),
			stackedRow(T("Environment"), T("One per line as NAME=value, added to the daemon's. $NAME, ${NAME} and a leading ~ expand when a session starts; $$ is a $."),
				ui.TextField(m.agentField(a.ID, func(d *agentDraft) *string { return &d.Env })).
					Multiline().Lines(3).Style(mono).Name(Tf("%s environment", a.Agent))),
		),
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
	return section(a.Agent, children...)
}

func profileCard(m *model, p agentDraft, mono ggui.TextStyle) ggui.Widget {
	id := p.ID
	nameField := ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Profile })).
		Style(mono).Name(Tf("%s profile name", p.Agent))
	head := ggui.Box(ggui.Row(
		icon("layers").Size(15),
		muted(ggui.Text(p.Agent+":").Size(13).Style(mono)),
		ggui.Box(nameField).Width(180),
		ggui.Spacer(),
		ui.ButtonOf(icon("trash-2").Size(14), func() { m.removeAgentRow(id) }).Ghost().Pad(6).Radius(6).
			Name(Tf("Remove %s", p.Agent+":"+p.Profile)),
	).Gap(8)).Pad(10, 16)
	return card(
		head,
		row(T("Flags"), T("Added after the agent's own."),
			ggui.Box(ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Args })).
				Style(mono).Placeholder("--model opus").Name(Tf("%s flags", p.Agent+":"+p.Profile))).Width(300)),
		stackedRow(T("Environment"), T("NAME=value lines over the agent's own, which $NAME reads."),
			ui.TextField(m.agentField(id, func(d *agentDraft) *string { return &d.Env })).
				Multiline().Lines(3).Style(mono).Name(Tf("%s environment", p.Agent+":"+p.Profile))),
	)
}
