package gui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

// sshModeLabels maps config.SSHHost.Mode values to English labels for T.
var sshModeLabels = map[string]string{
	config.SSHModeAuto:   "Auto",
	config.SSHModeDaemon: "Remote daemon",
	config.SSHModeDirect: "Direct SSH",
}

// sshHostAsk holds the Add / Edit SSH Host modal dialog state.
type sshHostAsk struct {
	Open         *ggui.StateValue[bool]
	EditingID    *ggui.StateValue[string] // "" when adding a new host
	Name         *ggui.StateValue[string]
	Host         *ggui.StateValue[string]
	User         *ggui.StateValue[string]
	Port         *ggui.StateValue[string]
	IdentityFile *ggui.StateValue[string]
	ProxyJump    *ggui.StateValue[string]
	DefaultDir   *ggui.StateValue[string]
	Mode         *ggui.StateValue[string]
	RemoteBinary *ggui.StateValue[string]
	ExtraArgs    *ggui.StateValue[string]
	ShowAdvanced *ggui.StateValue[bool]

	ConfigHosts *ggui.StateValue[[]sshremote.ConfigHost]
	Testing     *ggui.StateValue[bool]
	TestOK      *ggui.StateValue[bool]
	TestMsg     *ggui.StateValue[string]
	Installing  *ggui.StateValue[bool]
	InstallOK   *ggui.StateValue[bool]
	InstallMsg  *ggui.StateValue[string]
	HasMakod    *ggui.StateValue[bool]
	Err         *ggui.StateValue[string]

	onSaved func(nodeID string)
}

func newSSHHostAsk() *sshHostAsk {
	return &sshHostAsk{
		Open:         ggui.State(false),
		EditingID:    ggui.State(""),
		Name:         ggui.State(""),
		Host:         ggui.State(""),
		User:         ggui.State(""),
		Port:         ggui.State(""),
		IdentityFile: ggui.State(""),
		ProxyJump:    ggui.State(""),
		DefaultDir:   ggui.State(""),
		Mode:         ggui.State(config.SSHModeAuto),
		RemoteBinary: ggui.State(""),
		ExtraArgs:    ggui.State(""),
		ShowAdvanced: ggui.State(false),
		ConfigHosts:  ggui.State([]sshremote.ConfigHost(nil)).WithEqual(slices.Equal),
		Testing:      ggui.State(false),
		TestOK:       ggui.State(false),
		TestMsg:      ggui.State(""),
		Installing:   ggui.State(false),
		InstallOK:    ggui.State(false),
		InstallMsg:   ggui.State(""),
		HasMakod:     ggui.State(false),
		Err:          ggui.State(""),
	}
}

func equalSSHHosts(a, b []config.SSHHost) bool {
	return slices.EqualFunc(a, b, func(x, y config.SSHHost) bool { return x.Equal(y) })
}

// openAddSSHHost opens the modal to configure a new SSH remote host.
// If onSaved is non-nil, it is called with the new host's node ID after saving.
func (m *model) openAddSSHHost(onSaved func(nodeID string)) {
	a := m.SSH
	a.onSaved = onSaved
	a.EditingID.Set("")
	a.Name.Set("")
	a.Host.Set("")
	a.User.Set("")
	a.Port.Set("")
	a.IdentityFile.Set("")
	a.ProxyJump.Set("")
	a.DefaultDir.Set("")
	a.Mode.Set(config.SSHModeAuto)
	a.RemoteBinary.Set("")
	a.ExtraArgs.Set("")
	a.ShowAdvanced.Set(false)
	a.Testing.Set(false)
	a.TestOK.Set(false)
	a.TestMsg.Set("")
	a.Installing.Set(false)
	a.InstallOK.Set(false)
	a.InstallMsg.Set("")
	a.HasMakod.Set(false)
	a.Err.Set("")
	a.Open.Set(true)

	// Scan ~/.ssh/config off the UI goroutine.
	post := m.post
	go func() {
		found := sshremote.DiscoverConfigHosts()
		if post != nil {
			post(func() { a.ConfigHosts.Set(found) })
		} else {
			a.ConfigHosts.Set(found)
		}
	}()
}

// openEditSSHHost opens the modal pre-filled with an existing SSH host.
func (m *model) openEditSSHHost(h config.SSHHost) {
	a := m.SSH
	a.onSaved = nil
	a.EditingID.Set(h.ID)
	a.Name.Set(h.Name)
	a.Host.Set(h.Host)
	a.User.Set(h.User)
	if h.Port > 0 {
		a.Port.Set(strconv.Itoa(h.Port))
	} else {
		a.Port.Set("")
	}
	a.IdentityFile.Set(h.IdentityFile)
	a.ProxyJump.Set(h.ProxyJump)
	a.DefaultDir.Set(h.DefaultDir)
	a.Mode.Set(h.EffectiveMode())
	a.RemoteBinary.Set(h.RemoteBinary)
	a.ExtraArgs.Set(agentspec.JoinArgs(h.ExtraArgs))
	hasAdv := h.ProxyJump != "" || h.RemoteBinary != "" || len(h.ExtraArgs) > 0 || h.EffectiveMode() != config.SSHModeAuto
	a.ShowAdvanced.Set(hasAdv)
	a.Testing.Set(false)
	a.TestOK.Set(false)
	a.TestMsg.Set("")
	a.Installing.Set(false)
	a.InstallOK.Set(false)
	a.InstallMsg.Set("")
	hasMakod := false
	if live, ok := nodeIn(ggui.Peek(m.Nodes), h.NodeID()); ok && live.HasFeature(sshremote.FeatureSSHDaemon) {
		hasMakod = true
	}
	a.HasMakod.Set(hasMakod)
	a.Err.Set("")
	a.Open.Set(true)
}

func (a *sshHostAsk) fillFromConfig(ch sshremote.ConfigHost) {
	a.Name.Set(ch.Alias)
	a.Host.Set(ch.Alias)
	a.User.Set(ch.User)
	if ch.Port > 0 {
		a.Port.Set(strconv.Itoa(ch.Port))
	} else {
		a.Port.Set("")
	}
	a.IdentityFile.Set(ch.IdentityFile)
	a.ProxyJump.Set(ch.ProxyJump)
	if ch.ProxyJump != "" {
		a.ShowAdvanced.Set(true)
	}
	a.Err.Set("")
	a.TestMsg.Set("")
	a.InstallMsg.Set("")
	a.HasMakod.Set(false)
}

func (a *sshHostAsk) buildHost() (config.SSHHost, error) {
	port := 0
	if rawPort := strings.TrimSpace(ggui.Peek(a.Port)); rawPort != "" {
		n, err := strconv.Atoi(rawPort)
		if err != nil || n < 1 || n > 65535 {
			return config.SSHHost{}, fmt.Errorf("%s", T("Port must be a number between 1 and 65535."))
		}
		port = n
	}
	var extra []string
	if rawExtra := strings.TrimSpace(ggui.Peek(a.ExtraArgs)); rawExtra != "" {
		var err error
		extra, err = agentspec.SplitArgs(rawExtra)
		if err != nil {
			return config.SSHHost{}, fmt.Errorf(T("Extra SSH flags: %v"), err)
		}
	}
	id := ggui.Peek(a.EditingID)
	if id == "" {
		id = config.NewSSHHostID()
	}
	h := config.SSHHost{
		ID:           id,
		Name:         strings.TrimSpace(ggui.Peek(a.Name)),
		Host:         strings.TrimSpace(ggui.Peek(a.Host)),
		User:         strings.TrimSpace(ggui.Peek(a.User)),
		Port:         port,
		IdentityFile: strings.TrimSpace(ggui.Peek(a.IdentityFile)),
		ProxyJump:    strings.TrimSpace(ggui.Peek(a.ProxyJump)),
		DefaultDir:   strings.TrimSpace(ggui.Peek(a.DefaultDir)),
		Mode:         strings.TrimSpace(ggui.Peek(a.Mode)),
		RemoteBinary: strings.TrimSpace(ggui.Peek(a.RemoteBinary)),
		ExtraArgs:    extra,
	}
	if h.Name == "" {
		h.Name = h.Host
	}
	if err := h.Validate(); err != nil {
		return config.SSHHost{}, err
	}
	return h, nil
}

func (m *model) testSSHHostDraft() {
	a := m.SSH
	if ggui.Peek(a.Testing) {
		return
	}
	h, err := a.buildHost()
	if err != nil {
		a.Err.Set(err.Error())
		return
	}
	a.Err.Set("")
	a.Testing.Set(true)
	a.TestOK.Set(false)
	a.TestMsg.Set(T("Testing SSH connection…"))

	post := m.post
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := sshremote.TestConnection(ctx, h)
		finish := func() {
			a.Testing.Set(false)
			if err != nil {
				a.TestOK.Set(false)
				a.TestMsg.Set(err.Error())
				return
			}
			a.TestOK.Set(true)
			a.HasMakod.Set(res.MakodPath != "")
			platform := (&protocol.Node{Os: res.OS, Arch: res.Arch}).Platform()
			modeLabel := T("Direct SSH")
			if res.MakodPath != "" && h.EffectiveMode() != config.SSHModeDirect {
				modeLabel = T("Remote daemon")
			}
			parts := []string{Tf("Connected in %dms", max(res.Latency.Milliseconds(), 1))}
			if platform != "" {
				parts = append(parts, platform)
			}
			parts = append(parts, modeLabel)
			if len(res.Agents) > 0 {
				parts = append(parts, Tf("Agents: %s", strings.Join(res.Agents, ", ")))
			}
			a.TestMsg.Set(strings.Join(parts, " · "))
		}
		if post != nil {
			post(finish)
		} else {
			finish()
		}
	}()
}

// installSSHHostMakod builds or uploads makod to the host configured in the
// SSH host dialog and verifies it remotely.
func (m *model) installSSHHostMakod() {
	a := m.SSH
	if ggui.Peek(a.Installing) || ggui.Peek(a.Testing) {
		return
	}
	h, err := a.buildHost()
	if err != nil {
		a.Err.Set(err.Error())
		return
	}
	a.Err.Set("")
	a.Installing.Set(true)
	a.InstallOK.Set(false)
	a.InstallMsg.Set(T("Probing remote host…"))

	post := m.post
	editingID := ggui.Peek(a.EditingID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		setStep := func(step string) {
			update := func() { a.InstallMsg.Set(step) }
			if post != nil {
				post(update)
			} else {
				update()
			}
		}
		res, err := sshremote.InstallMakod(ctx, h, sshremote.InstallOptions{
			OnProgress: setStep,
		})
		finish := func() {
			a.Installing.Set(false)
			if err != nil {
				a.InstallOK.Set(false)
				a.InstallMsg.Set(err.Error())
				return
			}
			a.InstallOK.Set(true)
			a.HasMakod.Set(true)
			ver := res.Version
			if ver == "" {
				ver = "makod"
			}
			a.InstallMsg.Set(Tf("Installed %s at %s", ver, res.RemotePath))
			// If editing an already-saved host, immediately reconnect so it
			// upgrades from direct SSH to remote makod daemon mode.
			if editingID != "" {
				if live, ok := nodeIn(ggui.Peek(m.Nodes), h.NodeID()); ok {
					m.relink(live)
				}
			}
		}
		if post != nil {
			post(finish)
		} else {
			finish()
		}
	}()
}

func (m *model) submitSSHHost() {
	a := m.SSH
	h, err := a.buildHost()
	if err != nil {
		a.Err.Set(err.Error())
		return
	}
	editing := ggui.Peek(a.EditingID)
	cur := config.CloneSSHHosts(ggui.Peek(m.SSHHosts))
	if editing != "" {
		found := false
		for i := range cur {
			if cur[i].ID == editing {
				h.Disabled = cur[i].Disabled
				cur[i] = h
				found = true
				break
			}
		}
		if !found {
			cur = append(cur, h)
		}
	} else {
		cur = append(cur, h)
	}
	if !m.saveSSHHosts(cur) {
		return
	}
	onSaved := a.onSaved
	a.onSaved = nil
	a.Open.Set(false)
	if onSaved != nil {
		onSaved(h.NodeID())
	}
}

func (m *model) saveSSHHosts(hosts []config.SSHHost) bool {
	norm := config.NormalizeSSHHosts(hosts)
	cfg, err := config.Load()
	if err == nil {
		cfg.SSHHosts = norm
		err = config.Save(cfg)
	}
	if err != nil {
		if m.toaster != nil {
			m.toaster.Push(ui.Toast(Tf("Could not save: %v", err), "").Destructive())
		}
		return false
	}
	m.cfg.SSHHosts = config.CloneSSHHosts(norm)
	m.SSHHosts.Set(config.CloneSSHHosts(norm))
	if m.link != nil {
		m.link.pollNow()
	}
	return true
}

func (m *model) setSSHHostDisabled(id string, disabled bool) {
	cur := config.CloneSSHHosts(ggui.Peek(m.SSHHosts))
	for i := range cur {
		if cur[i].ID == id {
			cur[i].Disabled = disabled
			break
		}
	}
	m.saveSSHHosts(cur)
}

func (m *model) removeSSHHost(h config.SSHHost) {
	nodeID := h.NodeID()
	var active []string
	for _, s := range ggui.Peek(m.Sessions) {
		if s.GetNodeId() == nodeID {
			active = append(active, s.GetId())
		}
	}
	doRemove := func() {
		cur := slices.DeleteFunc(config.CloneSSHHosts(ggui.Peek(m.SSHHosts)), func(x config.SSHHost) bool {
			return x.ID == h.ID
		})
		m.saveSSHHosts(cur)
	}
	if len(active) == 0 {
		doRemove()
		return
	}
	m.askConfirm(confirmation{
		Title:  Tf("Remove SSH host %s?", h.DisplayName()),
		Body:   T("Its connection closes, and sessions on this host will no longer be reachable from this machine.") + "\n" + sessionList(m, active),
		Action: T("Remove"),
		Run:    doRemove,
	})
}

// sshHostFor returns the configured SSH host for nodeID, if any.
func (m *model) sshHostFor(nodeID string) (config.SSHHost, bool) {
	if !config.IsSSHNodeID(nodeID) {
		return config.SSHHost{}, false
	}
	for _, h := range ggui.Peek(m.SSHHosts) {
		if h.NodeID() == nodeID {
			return h, true
		}
	}
	return config.SSHHost{}, false
}

// sshHostDialog renders the Add / Edit SSH Host modal over the window.
func sshHostDialog(m *model) ggui.Widget {
	a := m.SSH
	return ggui.If(a.Open, func() ggui.Widget {
		title := T("Add SSH host")
		submitLabel := T("Add host")
		if ggui.Peek(a.EditingID) != "" {
			title = T("Edit SSH host")
			submitLabel = T("Save")
		}
		return keyedDialog(a.Open, sshHostForm(m, submitLabel)).Title(title).Width(560)
	})
}

func sshHostForm(m *model, submitLabel string) ggui.Widget {
	a := m.SSH
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}

	// Quick import chips from ~/.ssh/config when adding a new host.
	importSection := ggui.Reactive(func() ggui.Widget {
		if a.EditingID.Get() != "" {
			return widget.Box()
		}
		hosts := a.ConfigHosts.Get()
		if len(hosts) == 0 {
			return widget.Box()
		}
		existing := map[string]bool{}
		for _, h := range m.SSHHosts.Get() {
			existing[h.Host] = true
		}
		chips := make([]ggui.Widget, 0, min(len(hosts), 12))
		for _, ch := range hosts[:min(len(hosts), 12)] {
			item := ch
			label := item.Alias
			if existing[item.Alias] {
				label += " ✓"
			}
			var chip ggui.Widget = ui.ButtonOf(
				widget.Row(icon("server").Size(12).Color(skin.Muted), widget.Text(label).Size(12).NoWrap()).Gap(5).Align(widget.AlignCenter),
				func() { a.fillFromConfig(item) },
			).Outline().Pad(4, 8).Radius(6).Name(item.Alias)
			if sum := item.Summary(); sum != "" && sum != item.Alias {
				chip = ui.Tooltip(chip, sum)
			}
			chips = append(chips, chip)
		}
		return widget.Column(
			muted(widget.Text(T("From ~/.ssh/config")).Size(11.5)),
			widget.Wrap(chips...).Gap(6).RunGap(6),
		).Gap(6).Stretch()
	})

	browseKey := browseButton(a.IdentityFile, func(d runtime.Dialogs) (string, error) {
		return d.OpenFile(runtime.FileDialog{Title: T("Identity file"), Directory: ggui.Peek(a.IdentityFile)})
	})

	fieldLabel := func(text string) ggui.Widget {
		return widget.Text(text).Size(12).Color(skin.Muted)
	}

	nameField := widget.Column(
		fieldLabel(T("Connection name")),
		ui.TextField(a.Name).Placeholder("gpu-box").Name(T("Connection name")),
	).Gap(4).Stretch()

	hostPortRow := widget.Row(
		widget.Expanded(widget.Column(
			fieldLabel(T("Host")),
			ui.TextField(a.Host).Style(mono).Placeholder(T("user@host or ~/.ssh/config alias")).Name(T("Host")).
				OnSubmit(func(string) { m.submitSSHHost() }),
		).Gap(4).Stretch()),
		widget.Box(widget.Column(
			fieldLabel(T("Port")),
			ui.TextField(a.Port).Style(mono).Placeholder("22").Name(T("Port")).
				OnSubmit(func(string) { m.submitSSHHost() }),
		).Gap(4).Stretch()).Width(88),
	).Gap(10)

	userDirRow := widget.Row(
		widget.Box(widget.Column(
			fieldLabel(T("User")),
			ui.TextField(a.User).Style(mono).Placeholder(T("Optional")).Name(T("User")),
		).Gap(4).Stretch()).Width(160),
		widget.Expanded(widget.Column(
			fieldLabel(T("Default folder")),
			ui.TextField(a.DefaultDir).Style(mono).Placeholder("~/projects").Name(T("Default folder")),
		).Gap(4).Stretch()),
	).Gap(10)

	keyRow := widget.Column(
		fieldLabel(T("Identity file")),
		widget.Row(
			widget.Expanded(ui.TextField(a.IdentityFile).Style(mono).Placeholder("~/.ssh/id_ed25519").Name(T("Identity file"))),
			browseKey,
		).Gap(8),
	).Gap(4).Stretch()

	advToggle := ui.ButtonOf(ggui.View(a.ShowAdvanced, func(open bool) ggui.Widget {
		chev := pick(open, "chevron-down", "chevron-right")
		return widget.Row(icon(chev).Size(13).Color(skin.Muted), widget.Text(T("Advanced options")).Size(12.5).Color(skin.Muted)).Gap(5).Align(widget.AlignCenter)
	}), func() { ggui.Toggle(a.ShowAdvanced) }).Ghost().Pad(4, 6).Radius(6).Name(T("Advanced options"))

	advBody := ggui.If(a.ShowAdvanced, func() ggui.Widget {
		modeGroup := ui.ToggleGroup(a.Mode).
			Options([]string{config.SSHModeAuto, config.SSHModeDaemon, config.SSHModeDirect}).
			Format(func(v string) string { return T(sshModeLabels[v]) }).
			Name(T("Connection mode"))
		return widget.Box(widget.Column(
			widget.Column(
				fieldLabel(T("Connection mode")),
				modeGroup,
				muted(widget.Text(T("Auto uses remote makod over an SSH tunnel when installed, and falls back to direct SSH otherwise.")).Size(11.5)),
			).Gap(5).Stretch(),
			widget.Column(
				fieldLabel(T("Jump host (ProxyJump)")),
				ui.TextField(a.ProxyJump).Style(mono).Placeholder("bastion.example.com").Name(T("Jump host (ProxyJump)")),
			).Gap(4).Stretch(),
			widget.Column(
				fieldLabel(T("Remote makod path")),
				ui.TextField(a.RemoteBinary).Style(mono).Placeholder("~/.local/bin/makod").Name(T("Remote makod path")),
			).Gap(4).Stretch(),
			widget.Column(
				fieldLabel(T("Extra SSH flags")),
				ui.TextField(a.ExtraArgs).Style(mono).Placeholder("-o ForwardAgent=yes").Name(T("Extra SSH flags")),
			).Gap(4).Stretch(),
		).Gap(10).Stretch()).Pad(12).Fill(skin.Surface1).Border(1, skin.Border).Radius(8)
	})

	canAction := ggui.Derived(func() bool {
		return !a.Testing.Get() && !a.Installing.Get() && strings.TrimSpace(a.Host.Get()) != ""
	})

	installBtn := ui.ButtonOf(ggui.Reactive(func() ggui.Widget {
		busy := a.Installing.Get()
		installed := a.HasMakod.Get()
		label := T("Install makod")
		if busy {
			label = T("Installing…")
		} else if installed {
			label = T("Update makod")
		}
		return widget.Row(icon("cloud-download").Size(14), widget.Text(label).Size(12.5).NoWrap()).Gap(6).Align(widget.AlignCenter)
	}), m.installSSHHostMakod).Outline().BindDisabled(ggui.Not(canAction)).Name(T("Install makod"))

	makodBox := widget.Box(widget.Column(
		widget.Row(
			widget.Expanded(widget.Column(
				widget.Text(T("Remote makod daemon")).Size(12.5).Weight(ggui.WeightMedium),
				muted(widget.Text(T("Install makod to ~/.mako/bin/makod on the remote host for full chat, hooks, approvals, MCP, and persistent sessions.")).Size(11.5)),
			).Gap(2).Stretch()),
			installBtn,
		).Gap(12).Align(widget.AlignCenter),
		ggui.Reactive(func() ggui.Widget {
			msg := a.InstallMsg.Get()
			if msg == "" {
				return widget.Box()
			}
			busy := a.Installing.Get()
			ok := a.InstallOK.Get()
			col := skin.DotDown
			ic := "circle-x"
			if busy {
				col = skin.Muted
				ic = "clock"
			} else if ok {
				col = skin.DotRunning
				ic = "circle-check"
			}
			return widget.Row(
				iconColored(ic, col).Size(14),
				widget.Expanded(widget.Text(msg).Size(12).Color(col)),
			).Gap(8).Align(widget.AlignCenter)
		}),
	).Gap(8).Stretch()).Pad(10, 12).Fill(skin.Surface1).Border(1, skin.Border).Radius(8)

	testBanner := ggui.Reactive(func() ggui.Widget {
		msg := a.TestMsg.Get()
		if msg == "" {
			return widget.Box()
		}
		testing := a.Testing.Get()
		ok := a.TestOK.Get()
		col := skin.DotDown
		ic := "circle-x"
		if testing {
			col = skin.Muted
			ic = "clock"
		} else if ok {
			col = skin.DotRunning
			ic = "circle-check"
		}
		return widget.Box(widget.Row(
			iconColored(ic, col).Size(14),
			widget.Expanded(widget.Text(msg).Size(12).Color(col)),
		).Gap(8).Align(widget.AlignCenter)).Pad(8, 10).Fill(skin.Surface1).Border(1, skin.Border).Radius(7)
	})

	errLine := ggui.View(a.Err, func(s string) ggui.Widget {
		if s == "" {
			return widget.Box()
		}
		return widget.Text(s).Size(12).Color(skin.DotDown)
	})

	testBtn := ui.ButtonOf(ggui.View(a.Testing, func(busy bool) ggui.Widget {
		return widget.Text(pick(busy, T("Testing…"), T("Test connection"))).Size(13).NoWrap()
	}), m.testSSHHostDraft).Outline().BindDisabled(ggui.Not(canAction)).Name(T("Test connection"))

	footer := widget.Row(
		testBtn,
		widget.Spacer(),
		ui.Button(T("Cancel"), func() { a.Open.Set(false) }).Outline(),
		ui.Button(submitLabel, m.submitSSHHost).BindDisabled(ggui.Map(a.Host, func(s string) bool { return strings.TrimSpace(s) == "" })),
	).Gap(8)

	return widget.Column(
		muted(widget.Text(T("Connect to a remote server over OpenSSH using your ~/.ssh/config, SSH agent, and keys.")).Size(12.5)),
		importSection,
		nameField,
		hostPortRow,
		userDirRow,
		keyRow,
		makodBox,
		advToggle,
		advBody,
		testBanner,
		errLine,
		footer,
	).Gap(12).Stretch()
}

// sshHostsSection renders the Remote SSH hosts card in Settings ▸ Nodes.
func sshHostsSection(m *model) ggui.Widget {
	addBtn := ui.ButtonOf(
		widget.Row(icon("plus").Size(14), widget.Text(T("Add SSH host…")).Size(12.5)).Gap(6).Align(widget.AlignCenter),
		func() { m.openAddSSHHost(nil) },
	).Outline().Name(T("Add SSH host…"))

	head := widget.Row(
		widget.Expanded(widget.Text(T("Remote SSH hosts")).Size(12.5).Weight(ggui.WeightSemibold).Color(skin.Muted)),
		addBtn,
	).Align(widget.AlignCenter)

	list := ggui.Reactive(func() ggui.Widget {
		hosts := m.SSHHosts.Get()
		nodes := m.Nodes.Get()
		if len(hosts) == 0 {
			return card(row(
				T("No SSH hosts configured yet."),
				T("Add a remote server over SSH to run agents, browse files, inspect git diffs, and forward ports without configuring libp2p."),
				ui.Button(T("Add SSH host…"), func() { m.openAddSSHHost(nil) }).Outline(),
			))
		}
		rows := make([]ggui.Widget, 0, len(hosts))
		for _, h := range hosts {
			host := h
			live, hasLive := nodeIn(nodes, host.NodeID())
			rows = append(rows, sshHostCardRow(m, host, live, hasLive))
		}
		return widget.Column(rows...).Gap(8).Stretch()
	})
	return widget.Column(head, list).Gap(10).Stretch()
}

func sshHostCardRow(m *model, h config.SSHHost, live *protocol.Node, hasLive bool) ggui.Widget {
	var statusDot ggui.Widget
	var modeBadge string
	switch {
	case h.Disabled:
		statusDot = dot(skin.Muted)
		modeBadge = T("Disabled")
	case hasLive && live.GetLink() == protocol.LinkUp:
		statusDot = dot(skin.DotRunning)
		if live.HasFeature(sshremote.FeatureSSHDaemon) {
			modeBadge = T("SSH · makod")
		} else {
			modeBadge = T("SSH · direct")
		}
	case hasLive && live.GetLink() == protocol.LinkLinking:
		statusDot = dot(skin.DotWaiting)
		modeBadge = T("SSH")
	default:
		statusDot = dot(skin.DotDown)
		modeBadge = T("SSH")
	}

	badge := widget.Box(widget.Text(modeBadge).Size(11).Color(skin.Muted)).Pad(1, 6).Radius(4).Border(1, skin.Border)
	var metaParts []string
	metaParts = append(metaParts, h.DisplayAddr())
	if hasLive && live.Platform() != "" {
		metaParts = append(metaParts, live.Platform())
	}
	if h.DefaultDir != "" {
		metaParts = append(metaParts, h.DefaultDir)
	}
	if h.ProxyJump != "" {
		metaParts = append(metaParts, Tf("via %s", h.ProxyJump))
	}

	var actions []ggui.Widget
	if !h.Disabled && hasLive && reachable(live) {
		id := hostID(h)
		if !live.HasFeature(sshremote.FeatureSSHDaemon) {
			actions = append(actions, iconButton("cloud-download", T("Install makod"), func() {
				m.openEditSSHHost(h)
				m.installSSHHostMakod()
			}))
		}
		actions = append(actions,
			iconButton("hard-drive", T("Browse files"), func() { m.openBrowser(id, cmpOr(h.DefaultDir, "~"), "") }),
			iconButton("arrow-right-left", T("Forward a port…"), func() { m.askTunnel(id) }),
		)
	}
	if h.Disabled {
		actions = append(actions, ui.Button(T("Connect"), func() { m.setSSHHostDisabled(h.ID, false) }).Outline())
	} else {
		if hasLive && linkDown(live) {
			actions = append(actions, relinkButton(m, live))
		}
		actions = append(actions, ui.Button(T("Disconnect"), func() { m.setSSHHostDisabled(h.ID, true) }).Outline())
	}
	actions = append(actions,
		iconButton("pencil", T("Edit SSH host"), func() { m.openEditSSHHost(h) }),
		iconButton("trash-2", T("Remove SSH host"), func() { m.removeSSHHost(h) }),
	)

	cols := []ggui.Widget{
		widget.Row(widget.Text(h.DisplayName()).Size(13).Weight(ggui.WeightMedium), statusDot, badge).Gap(8).Align(widget.AlignCenter),
		widget.Text(strings.Join(metaParts, " · ")).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis(),
	}
	if !h.Disabled && hasLive && linkDown(live) {
		cols = append(cols, nodeLinkLine(m, live))
	}

	return widget.Box(widget.Row(
		icon("server").Size(16),
		widget.Expanded(widget.Column(cols...).Gap(3).Stretch()),
		widget.Row(actions...).Gap(4).Align(widget.AlignCenter),
	).Gap(12).Align(widget.AlignCenter)).Pad(10).Radius(8).Fill(skin.Surface1).Border(1, skin.Border)
}

func hostID(h config.SSHHost) string { return h.NodeID() }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
