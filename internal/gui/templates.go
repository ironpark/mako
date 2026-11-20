package gui

import (
	"cmp"
	"context"
	"errors"
	"image/color"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

// Prompt templates: Markdown files in the templates folder beside
// gui.json (see internal/prompttmpl), and those mako comes with, which a
// file of the name stands in for. A composer's draft that begins with
// two slashes, and has no space yet, lists the templates for the session's
// agent in the composer's menu (commands.go). Once a template's name is
// followed by a space, the menu's place shows the arguments it takes,
// offers value completions for select/choice and boolean arguments, and
// shows what is wrong with those given; Enter sends the prompt the
// template makes of them.

// templateMenu is the templates a composer's menu lists.
type templateMenu struct {
	templates *ggui.StateValue[[]*prompttmpl.Template]
	read      time.Time
	// failed is what was wrong with the last call Enter tried to send, and
	// the draft it was for; it goes once the draft changes.
	failed *ggui.StateValue[templateFailure]
	// running is true while a call's scripts or host calls execute.
	running *ggui.StateValue[bool]
}

type templateFailure struct{ draft, why string }

func newTemplateMenu() *templateMenu {
	return &templateMenu{
		// Read again every while, and the same as often as not.
		templates: ggui.State[[]*prompttmpl.Template](nil).WithEqual(deepEqual),
		failed:    ggui.State(templateFailure{}),
		running:   ggui.State(false),
	}
}

// templatesStale is how old the templates read are before a draft that
// calls one reads them again, for files written since.
const templatesStale = 2 * time.Second

func (m *model) invalidateSessionTemplates() {
	for _, state := range m.chats {
		if state != nil && state.templates != nil {
			state.templates.read = time.Time{}
		}
	}
}

// readTemplates reads the templates again, unless they were read lately
// and force is false. A file that does not read is left out.
func (m *model) readTemplates(state *chatState, force bool) {
	menu := state.templates
	if !force && time.Since(menu.read) < templatesStale {
		return
	}
	menu.read = time.Now()
	dir, err := config.TemplatesDir()
	if err != nil {
		return
	}
	got, _ := prompttmpl.LoadAll(dir)
	menu.templates.Set(got)
}

// templateItems are the templates for session id's agent whose names the
// draft begins or holds, or argument value completions once the template
// name is followed by whitespace. Reactive.
func (m *model) templateItems(id string, state *chatState, draft string) []menuItem {
	call, _ := prompttmpl.ParseCall(draft)
	s, _ := sessionIn(m.Sessions.Get(), id)
	var ours []*prompttmpl.Template
	for _, t := range state.templates.templates.Get() {
		if t.For(s.GetAgent(), s.GetProfile()) {
			ours = append(ours, t)
		}
	}
	if strings.ContainsAny(draft, " \t\n") {
		idx := slices.IndexFunc(ours, func(t *prompttmpl.Template) bool { return t.Name == call.Name })
		if idx < 0 {
			return nil
		}
		return m.templateArgItems(id, ours[idx], draft, call)
	}
	matches := matchByName(ours, strings.ToLower(call.Name), func(t *prompttmpl.Template) string { return t.Name })
	items := make([]menuItem, len(matches))
	for i, t := range matches {
		tag := T("template")
		if t.Builtin {
			tag = T("built-in")
		}
		items[i] = menuItem{
			name: "//" + t.Name, usage: t.Usage(), description: t.Description, tag: tag,
			complete: "//" + t.Name + " ", take: func() { m.takeTemplate(id, t) },
		}
	}
	return items
}

// takeTemplate puts t in the composer: sent at once when it needs no
// argument given, and otherwise followed by a space to give them after.
func (m *model) takeTemplate(id string, t *prompttmpl.Template) {
	if slices.ContainsFunc(t.Args, func(a prompttmpl.Arg) bool { return a.Required }) {
		m.setComposerDraft(id, "//"+t.Name+" ")
		return
	}
	m.setComposerDraft(id, "//"+t.Name)
	m.sendComposer(id)
}

// callAnalysis summarizes the partially typed arguments of a //name call.
type callAnalysis struct {
	bound          map[string]string
	active         *prompttmpl.Arg
	headPrefix     string // draft up to the start of the current token
	typedVal       string // typed value prefix for the current token
	namedArg       string // non-empty when editing name=val
	namedValueMode bool
	inRest         bool
}

type argToken struct {
	raw   string
	value string
	start int
}

func scanArgTokens(argsText string) (completed []argToken, curRaw string, curStart int, hasCur bool) {
	var quote byte
	inTok := false
	tokStart := 0
	for i := 0; i < len(argsText); i++ {
		c := argsText[i]
		if !inTok {
			if c == ' ' || c == '\t' || c == '\n' {
				continue
			}
			inTok = true
			tokStart = i
		}
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' && i+1 < len(argsText) && strings.IndexByte(`"\$`, argsText[i+1]) >= 0 {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(argsText) && strings.IndexByte(" \t\n'\"$", argsText[i+1]) >= 0:
			i++
		case c == ' ' || c == '\t' || c == '\n':
			raw := argsText[tokStart:i]
			completed = append(completed, argToken{raw: raw, value: unquoteArgWord(raw), start: tokStart})
			inTok = false
		}
	}
	if inTok {
		return completed, argsText[tokStart:], tokStart, true
	}
	return completed, "", len(argsText), false
}

func unquoteArgWord(raw string) string {
	words, err := agentspec.SplitArgs(raw)
	if err == nil && len(words) == 1 {
		return words[0]
	}
	s := strings.TrimSpace(raw)
	if len(s) >= 1 && (s[0] == '"' || s[0] == '\'') {
		s = s[1:]
	}
	return s
}

func templateArgByName(t *prompttmpl.Template, name string) *prompttmpl.Arg {
	for i := range t.Args {
		if t.Args[i].Name == name {
			return &t.Args[i]
		}
	}
	return nil
}

func analyzeTemplateCall(t *prompttmpl.Template, draft string, call prompttmpl.Call) callAnalysis {
	out := callAnalysis{bound: map[string]string{}}
	prefixLen := len(draft) - len(call.Args)
	completed, curRaw, curStart, _ := scanArgTokens(call.Args)
	out.headPrefix = draft[:prefixLen+curStart]

	nextPos := 0
	for _, tok := range completed {
		if name, val, ok := strings.Cut(tok.value, "="); ok {
			if a := templateArgByName(t, name); a != nil && !a.Rest {
				out.bound[a.Name] = val
				continue
			}
		}
		for nextPos < len(t.Args) {
			if _, given := out.bound[t.Args[nextPos].Name]; !given {
				break
			}
			nextPos++
		}
		if nextPos < len(t.Args) {
			a := &t.Args[nextPos]
			out.bound[a.Name] = tok.value
			if a.Rest {
				out.active = a
				out.inRest = true
				return out
			}
			nextPos++
		}
	}

	curWord := unquoteArgWord(curRaw)
	if name, val, ok := strings.Cut(curWord, "="); ok {
		if a := templateArgByName(t, name); a != nil && !a.Rest {
			out.namedArg = a.Name
			out.namedValueMode = true
			out.active = a
			out.typedVal = val
			return out
		}
	}

	out.typedVal = curWord
	for nextPos < len(t.Args) {
		if _, given := out.bound[t.Args[nextPos].Name]; !given {
			out.active = &t.Args[nextPos]
			if out.active.Rest {
				out.inRest = true
			}
			break
		}
		nextPos++
	}
	return out
}

func formatArgWord(val string) string {
	if val == "" || strings.ContainsAny(val, " \t\n\"'\\") {
		return strconv.Quote(val)
	}
	return val
}

func (m *model) templateArgItems(id string, t *prompttmpl.Template, draft string, call prompttmpl.Call) []menuItem {
	if len(t.Args) == 0 {
		return nil
	}
	an := analyzeTemplateCall(t, draft, call)
	if an.inRest {
		return nil
	}
	_, bindErr := t.Bind(call.Args)
	callValid := bindErr == nil

	var items []menuItem
	if an.active != nil {
		var candidates []string
		switch {
		case an.active.Type.HasOptions():
			candidates = an.active.Options
		case an.active.Type == prompttmpl.Boolean:
			candidates = []string{"true", "false"}
		}
		if len(candidates) > 0 {
			matched := matchByName(candidates, strings.ToLower(an.typedVal), func(s string) string { return s })
			for _, opt := range matched {
				token := formatArgWord(opt)
				if an.namedValueMode {
					token = an.namedArg + "=" + token
				}
				nextDraft := an.headPrefix + token + " "
				tag := string(an.active.Type)
				if an.active.Default != nil && *an.active.Default == opt {
					tag = T("default")
				}
				items = append(items, menuItem{
					name:        opt,
					usage:       an.active.Name,
					description: an.active.Description,
					tag:         tag,
					complete:    nextDraft,
					passEnter:   callValid,
					take:        func() { m.setComposerDraft(id, nextDraft) },
				})
			}
		}
	}

	if !an.namedValueMode {
		q := strings.ToLower(an.typedVal)
		for i := range t.Args {
			a := &t.Args[i]
			if a.Rest {
				continue
			}
			if _, given := an.bound[a.Name]; given {
				continue
			}
			prefix := a.Name + "="
			if q != "" && !strings.HasPrefix(strings.ToLower(a.Name), q) && !strings.Contains(strings.ToLower(prefix), q) {
				continue
			}
			nextDraft := an.headPrefix + prefix
			hint := string(a.Type)
			if a.Type.HasOptions() && len(a.Options) > 0 {
				hint = strings.Join(a.Options, "|")
			}
			items = append(items, menuItem{
				name:        prefix,
				usage:       hint,
				description: a.Description,
				tag:         T("argument"),
				complete:    nextDraft,
				passEnter:   callValid,
				take:        func() { m.setComposerDraft(id, nextDraft) },
			})
			if len(items) >= maxMenuItems {
				break
			}
		}
	}
	if len(items) > maxMenuItems {
		items = items[:maxMenuItems]
	}
	return items
}

// expandDraft gives send what session id's draft sends: the prompt its
// template makes, when it calls one, read from its file as it is now, or
// the built-in of the name; otherwise, and for a name neither has, the
// draft as it is. A template that runs JavaScript makes its prompt off
// the UI goroutine, and sends once it has. A wrong call sends nothing,
// saying why over the composer.
func (m *model) expandDraft(id string, state *chatState, draft string, send func(string)) {
	call, ok := prompttmpl.ParseCall(draft)
	if !ok || !prompttmpl.ValidName(call.Name) {
		send(draft)
		return
	}
	fail := func(why string) { state.templates.failed.Set(templateFailure{draft, why}) }
	dir, err := config.TemplatesDir()
	if err != nil {
		send(draft)
		return
	}
	t, err := prompttmpl.Find(dir, call.Name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		send(draft)
		return
	case err != nil:
		fail(err.Error())
		return
	}
	s, _ := m.session(id)
	if !t.For(s.GetAgent(), s.GetProfile()) {
		fail(Tf("//%s is not for %s", t.Name, s.GetAgent()))
		return
	}
	builtins := m.templateBuiltins(id, state)
	done := func(out string, err error) {
		if err != nil {
			fail(templateError(err))
			return
		}
		// Headed by what made it, which Chat shows it under (chatmarks.go);
		// not a prompt that is the agent's command, which must come first.
		if !strings.HasPrefix(strings.TrimSpace(out), "/") {
			out = templateMark(t.Name) + "\n" + out
		}
		send(out)
	}
	if !t.HasScripts() {
		done(t.Expand(context.Background(), call.Args, builtins))
		return
	}
	if ggui.Peek(state.templates.running) {
		return // Enter again while its scripts run
	}
	state.templates.running.Set(true)
	host := m.templateHost(id)
	post := m.post
	go func() {
		out, err := t.ExpandWith(context.Background(), call.Args, builtins, host)
		finish := func() {
			state.templates.running.Set(false)
			done(out, err)
		}
		if post != nil {
			post(finish)
		} else {
			finish()
		}
	}()
}

// templateHost snapshots the current session/node state on the UI goroutine and
// exposes Agents/Spawn/Ask/Decide to background prompt template scripts.
func (m *model) templateHost(sessionID string) prompttmpl.Host {
	s, _ := m.session(sessionID)
	conn := ggui.Peek(m.Conn)
	nodeID := cmp.Or(s.GetNodeId(), conn.Self.GetId())
	var ctxServers map[string]mcp.Server
	if m.ext != nil && m.ext.MCPServers != nil {
		ctxServers = maps.Clone(ggui.Peek(m.ext.MCPServers))
	}
	return &guiTemplateHost{
		m:          m,
		sessionID:  sessionID,
		nodeID:     nodeID,
		remoteNode: m.remoteID(nodeID),
		defAgent:   s.GetAgent(),
		defProfile: s.GetProfile(),
		defProject: cmp.Or(s.GetProject(), m.launchProject(nodeID)),
		nodes:      slices.Clone(ggui.Peek(m.Nodes)),
		cfgAgents:  maps.Clone(m.cfg.Agents),
		ctxServers: ctxServers,
		catalogs:   maps.Clone(ggui.Peek(m.AgentCatalogs)),
		isolate:    ggui.Peek(m.IsolateHome),
	}
}

type guiTemplateHost struct {
	m          *model
	sessionID  string
	nodeID     string
	remoteNode string
	defAgent   string
	defProfile string
	defProject string
	nodes      []*protocol.Node
	cfgAgents  map[string]agentspec.Agent
	ctxServers map[string]mcp.Server
	catalogs   map[string][]*protocol.AgentModel
	isolate    bool
}

func (h *guiTemplateHost) localAgentInfos() []*protocol.AgentInfo {
	nodeMCPEnabled := true
	if srv, ok := h.ctxServers["mako"]; ok {
		nodeMCPEnabled = srv.On()
	}
	agentMCPEnabled := func(picks *[]string) bool {
		if picks != nil {
			return slices.Contains(*picks, "mako")
		}
		return nodeMCPEnabled
	}
	names := agentspec.Offered(h.cfgAgents)
	infos := make([]*protocol.AgentInfo, 0, len(names))
	for _, name := range names {
		a, _ := agentspec.Lookup(name, h.cfgAgents)
		allowAgent := a.AgentAllowed()
		allowTemplate := a.TemplateAllowed()
		info := &protocol.AgentInfo{
			Name:          name,
			Profiles:      a.ProfileNames(),
			Yolo:          a.Yolo,
			AllowAgent:    &allowAgent,
			AllowTemplate: &allowTemplate,
		}
		var hasServers, hasPrompter bool
		if e, ok := agents.Lookup(name); ok {
			_, hasServers = e.Driver.(agent.Servers)
			_, hasPrompter = e.Driver.(agent.Prompter)
		}
		if !hasServers && len(a.Command) > 0 {
			if drv, ok := agents.ByProgram(a.Command[0]); ok {
				_, hasServers = drv.Driver.(agent.Servers)
				_, hasPrompter = drv.Driver.(agent.Prompter)
			}
		}
		for _, p := range a.Profiles {
			y := a.Yolo
			if p.Yolo != nil {
				y = *p.Yolo
			}
			allowAgent := p.AgentAllowed()
			allowTemplate := p.TemplateAllowed()
			info.ProfileDetails = append(info.ProfileDetails, &protocol.AgentProfileInfo{
				Name:          p.Name,
				Description:   p.Description,
				Model:         p.Model,
				Effort:        p.Effort,
				Yolo:          y,
				Spawnable:     hasServers && hasPrompter && agentMCPEnabled(p.ContextServers) && allowAgent,
				AllowAgent:    &allowAgent,
				AllowTemplate: &allowTemplate,
			})
		}
		infos = append(infos, info)
	}
	return infos
}

func (h *guiTemplateHost) Agents(ctx context.Context, agentName string, refresh bool) (prompttmpl.AgentCatalog, error) {
	var node *protocol.Node
	for _, n := range h.nodes {
		if n.GetId() == h.nodeID {
			node = n
			break
		}
	}
	var infos []*protocol.AgentInfo
	switch {
	case h.remoteNode == "" && len(h.cfgAgents) > 0:
		infos = h.localAgentInfos()
	case node != nil && len(node.GetAgents()) > 0:
		infos = node.GetAgents()
	default:
		infos = h.localAgentInfos()
	}

	out := prompttmpl.AgentCatalog{NodeID: h.nodeID}
	for _, ai := range infos {
		name := ai.GetName()
		if agentName != "" && name != agentName {
			continue
		}
		ae := prompttmpl.AgentEntry{
			Name:          name,
			Yolo:          ai.GetYolo(),
			AllowAgent:    ai.AllowAgent == nil || ai.GetAllowAgent(),
			AllowTemplate: ai.AllowTemplate == nil || ai.GetAllowTemplate(),
		}
		if a, ok := agentspec.Lookup(name, h.cfgAgents); ok && len(a.Command) > 0 {
			ae.Command = a.Command[0]
		}
		if _, ok := agents.As[agent.YoloFlag](name); ok {
			ae.SupportsYolo = true
		}
		if mf, ok := agents.As[agent.ModelFlag](name); ok && mf.ModelArgs("x") != nil {
			ae.SupportsModel = true
		}
		if ef, ok := agents.As[agent.EffortFlag](name); ok && ef.EffortArgs("high") != nil {
			ae.SupportsEffort = true
		}
		if _, ok := agents.As[agent.OneShot](name); ok {
			ae.SupportsOneShot = true
		}
		if _, ok := agents.As[agent.Prompter](name); ok {
			ae.SupportsSpawn = true
		}

		if details := ai.GetProfileDetails(); len(details) > 0 {
			for _, p := range details {
				allowAgent := p.AllowAgent == nil || p.GetAllowAgent()
				allowTemplate := p.AllowTemplate == nil || p.GetAllowTemplate()
				ae.Profiles = append(ae.Profiles, prompttmpl.ProfileEntry{
					Name:          p.GetName(),
					Description:   p.GetDescription(),
					Model:         p.GetModel(),
					Effort:        p.GetEffort(),
					Yolo:          p.GetYolo(),
					Spawnable:     p.GetSpawnable(),
					AllowAgent:    allowAgent,
					AllowTemplate: allowTemplate,
				})
			}
		} else {
			for _, pName := range ai.GetProfiles() {
				ae.Profiles = append(ae.Profiles, prompttmpl.ProfileEntry{Name: pName, AllowAgent: true, AllowTemplate: true})
			}
		}

		var models []*protocol.AgentModel
		if !refresh && len(h.catalogs[name]) > 0 {
			models = h.catalogs[name]
		} else if agentName == name || refresh || len(h.catalogs[name]) == 0 {
			if h.m != nil && h.m.link != nil && h.m.link.isLinked() {
				if c, err := h.m.link.client(ctx); err == nil {
					if res, err := c.ListAgentModels(ctx, &protocol.ListAgentModels{NodeId: h.remoteNode, Agent: name, Refresh: refresh}); err == nil {
						models = res.GetModels()
					}
					c.Close()
				}
			}
			if len(models) == 0 {
				if local, err := localAgentCatalog(ctx, name, h.isolate, h.cfgAgents, refresh); err == nil {
					models = local
				}
			}
		}
		for _, m := range models {
			ae.Models = append(ae.Models, prompttmpl.ModelEntry{
				ID:      m.GetId(),
				Name:    m.GetName(),
				Efforts: slices.Clone(m.GetEfforts()),
			})
		}
		out.Agents = append(out.Agents, ae)
	}
	return out, nil
}

func (h *guiTemplateHost) checkTemplateProfileAllowed(agentName, profile string) error {
	var infos []*protocol.AgentInfo
	if h.remoteNode == "" && len(h.cfgAgents) > 0 {
		infos = h.localAgentInfos()
	} else {
		for _, n := range h.nodes {
			if n.GetId() == h.nodeID {
				infos = n.GetAgents()
				break
			}
		}
	}
	for _, ai := range infos {
		if ai.GetName() != agentName {
			continue
		}
		if profile == "" {
			if ai.AllowTemplate != nil && !ai.GetAllowTemplate() {
				return errors.New("agent " + agentName + " default configuration is not enabled for prompt templates")
			}
			return nil
		}
		for _, p := range ai.GetProfileDetails() {
			if p.GetName() == profile && p.AllowTemplate != nil && !p.GetAllowTemplate() {
				return errors.New("profile " + agentspec.Target(agentName, profile) + " is not enabled for prompt templates")
			}
		}
	}
	return nil
}

func (h *guiTemplateHost) Spawn(ctx context.Context, req prompttmpl.SpawnRequest) (prompttmpl.SpawnResult, error) {
	if h.m == nil || h.m.link == nil {
		return prompttmpl.SpawnResult{}, errors.New("daemon link is unavailable")
	}
	agentName, profile := prompttmpl.SplitTarget(req.Target, req.Agent, req.Profile)
	if agentName == "" {
		agentName = h.defAgent
		if profile == "" {
			profile = h.defProfile
		}
	}
	if agentName == "" {
		return prompttmpl.SpawnResult{}, errors.New("spawn requires an agent")
	}
	if err := h.checkTemplateProfileAllowed(agentName, profile); err != nil {
		return prompttmpl.SpawnResult{}, err
	}
	project := cmp.Or(strings.TrimSpace(req.Project), h.defProject)
	c, err := h.m.link.client(ctx)
	if err != nil {
		return prompttmpl.SpawnResult{}, err
	}
	defer c.Close()

	info, err := c.Open(ctx, &protocol.OpenSession{
		NodeId:          h.remoteNode,
		Agent:           agentName,
		Profile:         profile,
		Model:           strings.TrimSpace(req.Model),
		Effort:          strings.TrimSpace(req.Effort),
		Yolo:            req.Yolo,
		Name:            strings.TrimSpace(req.Name),
		Project:         project,
		Prompt:          req.Prompt,
		ParentSessionId: h.sessionID,
		Cols:            120,
		Rows:            36,
	})
	if err != nil {
		return prompttmpl.SpawnResult{}, err
	}
	if h.m.post != nil {
		h.m.post(func() {
			if _, known := h.m.session(info.GetId()); !known {
				h.m.Sessions.Set(append(slices.Clone(ggui.Peek(h.m.Sessions)), info))
			}
		})
	}
	return prompttmpl.SpawnResult{
		SessionID: info.GetId(),
		Agent:     info.GetAgent(),
		Profile:   info.GetProfile(),
		Name:      info.GetName(),
		Project:   info.GetProject(),
	}, nil
}

func (h *guiTemplateHost) Ask(ctx context.Context, req prompttmpl.AskRequest) (prompttmpl.AskResult, error) {
	if h.m == nil || h.m.link == nil {
		return prompttmpl.AskResult{}, errors.New("daemon link is unavailable")
	}
	agentName, profile := prompttmpl.SplitTarget(req.Target, req.Agent, req.Profile)
	if req.Target == "" && req.Agent == "" {
		agentName = h.defAgent
		if profile == "" {
			profile = h.defProfile
		}
	}
	if agentName == "" {
		return prompttmpl.AskResult{}, errors.New("ask requires an agent")
	}
	if err := h.checkTemplateProfileAllowed(agentName, profile); err != nil {
		return prompttmpl.AskResult{}, err
	}
	schemaJSON, err := prompttmpl.SchemaJSON(req.Schema)
	if err != nil {
		return prompttmpl.AskResult{}, err
	}
	format := strings.TrimSpace(req.Format)
	if format == "" && schemaJSON != "" {
		format = "json"
	}
	project := cmp.Or(strings.TrimSpace(req.Project), h.defProject)

	c, err := h.m.link.client(ctx)
	if err != nil {
		return prompttmpl.AskResult{}, err
	}
	defer c.Close()

	res, err := c.RunAgentPrompt(ctx, &protocol.RunAgentPrompt{
		NodeId:          h.remoteNode,
		Agent:           agentName,
		Profile:         profile,
		Model:           strings.TrimSpace(req.Model),
		Effort:          strings.TrimSpace(req.Effort),
		Yolo:            req.Yolo,
		Project:         project,
		Prompt:          req.Prompt,
		Format:          format,
		JsonSchema:      schemaJSON,
		ParentSessionId: h.sessionID,
	})
	if err != nil {
		return prompttmpl.AskResult{}, err
	}
	return prompttmpl.AskResult{Output: res.GetOutput(), Format: res.GetFormat()}, nil
}

func (h *guiTemplateHost) Decide(ctx context.Context, rawJSON []byte) ([]byte, error) {
	if h.m == nil || h.m.link == nil {
		return nil, errors.New("daemon link is unavailable")
	}
	c, err := h.m.link.client(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	res, err := c.RunDecision(ctx, &protocol.RunDecision{RequestJson: string(rawJSON)})
	if err != nil {
		return nil, err
	}
	return []byte(res.GetResponseJson()), nil
}

// templateBuiltins are the built-in variables' values for session id.
func (m *model) templateBuiltins(id string, state *chatState) map[string]string {
	s, _ := m.session(id)
	return map[string]string{
		"agent":   s.GetAgent(),
		"profile": s.GetProfile(),
		"model":   ggui.Peek(state.result).Chat.GetUsage().GetModel(),
		"project": s.GetProject(),
		"branch":  s.GetBranch(),
		"repo":    s.GetRemote(),
		"date":    time.Now().Format(time.DateOnly),
	}
}

// templateError says what is wrong with a call's arguments.
func templateError(err error) string {
	var ae *prompttmpl.ArgError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	switch ae.Kind {
	case prompttmpl.Missing:
		return Tf("%s is required", ae.Arg)
	case prompttmpl.Extra:
		return Tf("%q is one value more than the template takes", ae.Value)
	case prompttmpl.Twice:
		return Tf("%s is given twice", ae.Arg)
	case prompttmpl.BadValue:
		return Tf("%s takes %s, not %q", ae.Arg, typeWant(ae.Type, ae.Options), ae.Value)
	case prompttmpl.Unquoted:
		return T("A quote is not closed")
	}
	return err.Error()
}

// typeWant says what an argument of a type takes.
func typeWant(t prompttmpl.Type, options []string) string {
	switch t {
	case prompttmpl.String:
		return T("one line")
	case prompttmpl.Integer:
		return T("a whole number")
	case prompttmpl.Number:
		return T("a number")
	case prompttmpl.Boolean:
		return T("true or false")
	case prompttmpl.Choice, prompttmpl.Select:
		return Tf("one of %s", strings.Join(options, ", "))
	}
	return string(t)
}

// templateHint is, for a draft that calls a template with arguments after
// its name, an interactive pill bar of bound, active, and remaining arguments,
// active argument hints, validation errors, or running status. Reactive.
func templateHint(state *chatState) ggui.Widget {
	draft := state.draft.Get()
	call, ok := prompttmpl.ParseCall(draft)
	if !ok || !strings.ContainsAny(draft, " \t\n") {
		return nil
	}
	i := slices.IndexFunc(state.templates.templates.Get(), func(t *prompttmpl.Template) bool { return t.Name == call.Name })
	if i < 0 {
		return nil
	}
	t := ggui.Peek(state.templates.templates)[i]
	an := analyzeTemplateCall(t, draft, call)

	headerParts := []ggui.Widget{
		widget.Text("//" + t.Name).Size(13).Weight(ggui.WeightSemibold).NoWrap(),
	}
	if len(t.Args) == 0 {
		headerParts = append(headerParts, widget.Expanded(widget.Text(T("no arguments")).Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()))
	} else {
		var pills []ggui.Widget
		for idx := range t.Args {
			a := &t.Args[idx]
			val, isBound := an.bound[a.Name]
			isActive := an.active != nil && an.active.Name == a.Name
			pills = append(pills, templateArgPill(a, val, isBound, isActive))
		}
		headerParts = append(headerParts, widget.Expanded(widget.Wrap(pills...).Gap(4).RunGap(4)))
	}
	lines := []ggui.Widget{widget.Row(headerParts...).Gap(8).Align(widget.AlignCenter)}

	why := ""
	if f := state.templates.failed.Get(); f.draft == draft {
		why = f.why
	} else if _, err := t.Bind(call.Args); err != nil && strings.TrimSpace(call.Args) != "" {
		why = templateError(err)
	}
	switch {
	case state.templates.running.Get():
		lines = append(lines, widget.Row(ui.Spinner().Size(12), muted(widget.Text(T("Running template…")).Size(12))).Gap(6).Align(widget.AlignCenter))
	case why != "":
		lines = append(lines, widget.Text(why).Size(12).Color(uitheme.Destructive))
	case an.active != nil && an.active.Description != "":
		lines = append(lines, muted(widget.Text(an.active.Name+": "+an.active.Description).Size(12)))
	case t.Description != "":
		lines = append(lines, muted(widget.Text(t.Description).Size(12)))
	}
	return widget.Box(widget.Column(lines...).Gap(4).Stretch()).Pad(6, 10)
}

func templateArgPill(a *prompttmpl.Arg, boundVal string, isBound, isActive bool) ggui.Widget {
	kind := string(a.Type)
	if a.Type.HasOptions() && len(a.Options) > 0 {
		kind = strings.Join(a.Options, "|")
	}
	if a.Rest {
		kind += "…"
	}
	var label string
	switch {
	case isBound:
		label = a.Name + "=" + boundVal
	case a.Required:
		label = "<" + a.Name + ": " + kind + ">"
	case a.Default != nil:
		label = "[" + a.Name + "=" + *a.Default + "]"
	default:
		label = "[" + a.Name + ": " + kind + "]"
	}

	var fill, border, fg color.Color
	switch {
	case isActive:
		fill, border, fg = skin.SidebarSel, skin.BorderAccent, skin.AccentBright
	case isBound:
		fill, border, fg = skin.Surface2, skin.Border, skin.Fg
	default:
		fill, border, fg = skin.Surface0, skin.Border, skin.ExtraMuted
	}
	return widget.Box(widget.Text(label).Size(11.5).Font(ggui.DefaultMonoFont()).Color(fg).NoWrap()).
		Pad(2, 6).Radius(5).Fill(fill).Border(1, border)
}
