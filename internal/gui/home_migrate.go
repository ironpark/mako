package gui

import (
	"context"
	"os"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// homeMigrateState holds the UI state for the global Mako home isolation
// migration dialog. UI goroutine only.
type homeMigrateState struct {
	Open          *ggui.StateValue[bool]
	TargetIsolate *ggui.StateValue[bool]
	Info          *ggui.StateValue[*protocol.AgentHomeInfo]
	CopyConfig    *ggui.StateValue[bool]
	MoveSessions  *ggui.StateValue[bool]
	Busy          *ggui.StateValue[bool]
	Err           *ggui.StateValue[string]
}

func newHomeMigrateState() *homeMigrateState {
	return &homeMigrateState{
		Open:          ggui.State(false),
		TargetIsolate: ggui.State(false),
		Info:          ggui.State((*protocol.AgentHomeInfo)(nil)),
		CopyConfig:    ggui.State(true),
		MoveSessions:  ggui.State(true),
		Busy:          ggui.State(false),
		Err:           ggui.State(""),
	}
}

// isolateHomeCard is the global setting at the top of Settings > Agents that
// toggles Mako's isolated home directory (~/.mako/home) for all supported
// agents.
func isolateHomeCard(m *model) ggui.Widget {
	base, _ := agentspec.IsolatedHomeBase()
	on := ggui.Bind(func() bool {
		return m.IsolateHome.Get()
	}, func(v bool) {
		m.requestIsolateHome(v)
	})
	return card(row(
		T("Use isolated Mako home"),
		Tf("Store agent settings and Mako sessions under %s instead of the default home directory.", prettyPath(base)),
		ui.Switch(on, "").Name(T("Use isolated Mako home")),
	))
}

// requestIsolateHome inspects what changing Mako's global home isolation needs:
// blocks if any supported agent has running sessions, asks for migration if
// sessions or config exist in the source directories, or saves immediately if
// nothing exists yet.
func (m *model) requestIsolateHome(isolate bool) {
	linked := m.link != nil && m.link.isLinked()
	ggui.Async(func() (*protocol.AgentHomeInfo, error) {
		if linked {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := m.link.client(ctx)
			if err == nil {
				return c.InspectAgentHome(ctx, &protocol.InspectAgentHome{Isolate: isolate})
			}
		}
		isoBase, _ := agentspec.IsolatedHomeBase()
		userHome, _ := os.UserHomeDir()
		fromDir, toDir := userHome, isoBase
		if !isolate {
			fromDir, toDir = isoBase, userHome
		}
		return &protocol.AgentHomeInfo{
			Supported: len(agentspec.IsolatableAgents()) > 0,
			FromDir:   fromDir,
			ToDir:     toDir,
		}, nil
	}, func(info *protocol.AgentHomeInfo, err error) {
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not inspect Mako home"), err.Error()).Destructive())
			return
		}
		if !info.GetSupported() {
			return
		}
		if info.GetRunningSessions() > 0 {
			m.toaster.Push(ui.Toast(
				T("Stop running agent sessions first"),
				Tf("%d agent session(s) are currently running. Stop them before switching Mako home.", info.GetRunningSessions()),
			).Destructive())
			return
		}
		if info.GetSavedSessions() > 0 || info.GetHasConfig() {
			h := m.HomeMigrate
			h.TargetIsolate.Set(isolate)
			h.Info.Set(info)
			h.CopyConfig.Set(info.GetHasConfig())
			h.MoveSessions.Set(info.GetSavedSessions() > 0)
			h.Busy.Set(false)
			h.Err.Set("")
			h.Open.Set(true)
			return
		}
		m.persistIsolateHome(isolate)
	})
}

func (m *model) applyHomeMigration(migrate bool) {
	h := m.HomeMigrate
	if ggui.Peek(h.Busy) {
		return
	}
	isolate := ggui.Peek(h.TargetIsolate)
	if !migrate {
		h.Open.Set(false)
		m.persistIsolateHome(isolate)
		return
	}
	copyCfg := ggui.Peek(h.CopyConfig)
	moveSess := ggui.Peek(h.MoveSessions)
	if !copyCfg && !moveSess {
		h.Open.Set(false)
		m.persistIsolateHome(isolate)
		return
	}
	if m.link == nil || !m.link.isLinked() {
		h.Err.Set(T("Connect to the daemon before migrating data."))
		return
	}
	h.Busy.Set(true)
	h.Err.Set("")
	ggui.Async(func() (*protocol.AgentHomeMigrated, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.MigrateAgentHome(ctx, &protocol.MigrateAgentHome{
			Isolate:      isolate,
			CopyConfig:   copyCfg,
			MoveSessions: moveSess,
		})
	}, func(res *protocol.AgentHomeMigrated, err error) {
		h.Busy.Set(false)
		if err != nil {
			h.Err.Set(err.Error())
			return
		}
		h.Open.Set(false)
		m.persistIsolateHome(isolate)
		if res.GetMovedSessions() > 0 {
			m.toaster.Push(ui.Toast(
				T("Mako home updated"),
				Tf("Moved %d Mako session(s).", res.GetMovedSessions()),
			))
		}
	})
}

func (m *model) persistIsolateHome(isolate bool) {
	next := *m.cfg
	next.IsolateHome = isolate
	if err := config.Save(&next); err != nil {
		m.HostNote.Set(Tf("Could not save: %v", err))
		return
	}
	*m.cfg = next
	m.IsolateHome.Set(isolate)
	if m.link != nil {
		m.link.pollNow()
	}
	for agentName := range ggui.Peek(m.AgentCatalogs) {
		m.loadAgentCatalog(agentName, true)
	}
	m.HostNote.Set(T("Saved. Sessions started from now on use it."))
}

func homeMigrateDialog(m *model) ggui.Widget {
	h := m.HomeMigrate
	hasErr := ggui.Map(h.Err, func(s string) bool { return s != "" })
	body := ggui.Reactive(func() ggui.Widget {
		info := h.Info.Get()
		isolate := h.TargetIsolate.Get()
		if info == nil {
			return widget.Box()
		}
		desc := T("Mako will move its tracked sessions and copy configuration to its isolated home directory. Sessions outside Mako are left untouched.")
		if !isolate {
			desc = T("Mako will move its tracked sessions back to the default home directory.")
		}
		mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
		paths := widget.Box(widget.Column(
			muted(widget.Text(Tf("From: %s", prettyPath(info.GetFromDir()))).Style(mono)),
			muted(widget.Text(Tf("To: %s", prettyPath(info.GetToDir()))).Style(mono)),
		).Gap(4).Stretch()).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(10, 12)

		var checks []ggui.Widget
		if info.GetHasConfig() {
			checks = append(checks, ui.Checkbox(h.CopyConfig, T("Copy configuration and credentials")))
		}
		if info.GetSavedSessions() > 0 {
			checks = append(checks, ui.Checkbox(h.MoveSessions, Tf("Move Mako sessions (%d)", info.GetSavedSessions())))
		}
		rows := []ggui.Widget{
			muted(widget.Text(desc).Size(12.5)),
			paths,
		}
		if len(checks) > 0 {
			rows = append(rows, widget.Column(checks...).Gap(8).Stretch())
		}
		rows = append(rows,
			ggui.If(hasErr, func() ggui.Widget {
				return widget.TextOf(h.Err).Size(12).Color(skin.DotDown)
			}),
			widget.Row(
				ui.Button(T("Cancel"), func() { h.Open.Set(false) }).Outline().BindDisabled(h.Busy),
				widget.Spacer(),
				ui.Button(T("Switch without migrating"), func() { m.applyHomeMigration(false) }).Outline().BindDisabled(h.Busy),
				ui.Button(T("Migrate & apply"), func() { m.applyHomeMigration(true) }).BindDisabled(h.Busy),
			).Gap(8).Align(widget.AlignCenter),
		)
		return widget.Column(rows...).Gap(14).Stretch()
	})
	title := ggui.Reactive(func() ggui.Widget {
		txt := T("Use isolated Mako home?")
		if !h.TargetIsolate.Get() {
			txt = T("Use default home directory?")
		}
		return widget.Text(txt).Size(15).Weight(ggui.WeightSemibold)
	})
	return keyedDialog(h.Open, widget.Column(title, body).Gap(12).Stretch()).Width(500)
}
