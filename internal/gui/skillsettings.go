package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/skills"
)

// Settings ▸ Skills lets the user inspect and customize Mako's built-in skills
// (such as mako-plugin and mako-template). Saved edits live under
// <configDir>/skills/<name>/SKILL.md and are synced immediately to every
// supported agent skills directory; resetting removes the override and restores
// the embedded default.

type skillDraft struct {
	Name        string
	Description string
	Content     string
	Saved       string
	Builtin     string
	Customized  bool
	Editing     bool
	ShowBuiltin bool
	Err         string
}

func (d skillDraft) dirty() bool { return d.Content != d.Saved }

type skillPage struct {
	drafts *ggui.StateValue[[]skillDraft]
	open   *ggui.StateValue[string]
}

func newSkillPage() *skillPage {
	pg := &skillPage{
		drafts: ggui.State[[]skillDraft](nil),
		open:   ggui.State(""),
	}
	pg.reload()
	if list := ggui.Peek(pg.drafts); len(list) > 0 {
		pg.open.Set(list[0].Name)
	}
	return pg
}

func (pg *skillPage) reload() {
	prev := map[string]skillDraft{}
	for _, d := range ggui.Peek(pg.drafts) {
		prev[d.Name] = d
	}
	eff := skills.Effective()
	out := make([]skillDraft, 0, len(eff))
	for _, sk := range eff {
		builtin, _ := skills.BuiltinByName(sk.Name)
		old := prev[sk.Name]
		text := string(sk.Content)
		out = append(out, skillDraft{
			Name:        sk.Name,
			Description: sk.Description,
			Content:     text,
			Saved:       text,
			Builtin:     string(builtin.Content),
			Customized:  sk.Customized,
			Editing:     old.Editing,
			ShowBuiltin: old.ShowBuiltin,
		})
	}
	pg.drafts.Set(out)
}

func (pg *skillPage) draft(name string) (skillDraft, bool) {
	for _, d := range ggui.Peek(pg.drafts) {
		if d.Name == name {
			return d, true
		}
	}
	return skillDraft{}, false
}

func (pg *skillPage) update(name string, fn func(*skillDraft)) {
	list := slices.Clone(ggui.Peek(pg.drafts))
	for i := range list {
		if list[i].Name == name {
			fn(&list[i])
			pg.drafts.Set(list)
			return
		}
	}
}

func (pg *skillPage) field(name string, get func(skillDraft) string, set func(*skillDraft, string)) ggui.Binding[string] {
	return ggui.Bind(func() string {
		for _, d := range pg.drafts.Get() {
			if d.Name == name {
				return get(d)
			}
		}
		return ""
	}, func(v string) {
		pg.update(name, func(d *skillDraft) {
			set(d, v)
			d.Err = ""
		})
	})
}

func (pg *skillPage) save(m *model, name string) {
	d, ok := pg.draft(name)
	if !ok {
		return
	}
	if err := skills.SaveOverride(name, []byte(d.Content)); err != nil {
		pg.update(name, func(x *skillDraft) { x.Err = err.Error() })
		if m != nil && m.toaster != nil {
			m.toaster.Push(ui.Toast(T("Could not save skill"), err.Error()).Destructive())
		}
		return
	}
	_, desc := skills.Frontmatter([]byte(d.Content))
	pg.update(name, func(x *skillDraft) {
		x.Saved = x.Content
		x.Customized = true
		x.Err = ""
		if desc != "" {
			x.Description = desc
		}
	})
	synced, err := m.syncSkillsToAgents()
	if m != nil && m.toaster != nil {
		if err != nil {
			m.toaster.Push(ui.Toast(Tf("Saved %s", name), err.Error()).Destructive())
		} else if synced > 0 {
			m.toaster.Push(ui.Toast(Tf("Saved %s", name), Tf("Synced to %d agent directories", synced)))
		} else {
			m.toaster.Push(ui.Toast(Tf("Saved %s", name), ""))
		}
	}
}

func (pg *skillPage) revert(name string) {
	pg.update(name, func(d *skillDraft) {
		d.Content = d.Saved
		d.Err = ""
	})
}

func (pg *skillPage) reset(m *model, name string) {
	if err := skills.ResetOverride(name); err != nil {
		pg.update(name, func(x *skillDraft) { x.Err = err.Error() })
		if m != nil && m.toaster != nil {
			m.toaster.Push(ui.Toast(T("Could not reset skill"), err.Error()).Destructive())
		}
		return
	}
	builtin, _ := skills.BuiltinByName(name)
	text := string(builtin.Content)
	pg.update(name, func(x *skillDraft) {
		x.Content = text
		x.Saved = text
		x.Builtin = text
		x.Description = builtin.Description
		x.Customized = false
		x.Err = ""
	})
	synced, err := m.syncSkillsToAgents()
	if m != nil && m.toaster != nil {
		if err != nil {
			m.toaster.Push(ui.Toast(Tf("Reset %s to built-in", name), err.Error()).Destructive())
		} else if synced > 0 {
			m.toaster.Push(ui.Toast(Tf("Reset %s to built-in", name), Tf("Synced to %d agent directories", synced)))
		} else {
			m.toaster.Push(ui.Toast(Tf("Reset %s to built-in", name), ""))
		}
	}
}

func (pg *skillPage) askReset(m *model, name string) {
	if m == nil {
		pg.reset(nil, name)
		return
	}
	m.askConfirm(confirmation{
		Title:  Tf("Reset %s to built-in?", name),
		Body:   T("Your customized copy in the skills folder will be deleted and the built-in version will be restored to your agents."),
		Action: T("Reset"),
		Run:    func() { pg.reset(m, name) },
	})
}

// syncSkillsToAgents distributes effective built-in skills to every initialized
// agent skills directory on this machine (honoring isolated home settings).
func (m *model) syncSkillsToAgents() (int, error) {
	var agentsMap map[string]agentspec.Agent
	var isolate bool
	if m != nil && m.cfg != nil {
		agentsMap = m.cfg.Agents
		isolate = m.cfg.IsolateHome
	}
	synced := 0
	var firstErr error
	seen := map[string]bool{}
	for _, agentName := range skills.SupportedAgents {
		env := agentspec.IsolatedHomeEnvForAgent(agentName, isolate, agentsMap)
		for _, dir := range skills.AgentSkillsDirs(agentName, env) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			if _, err := os.Stat(filepath.Dir(dir)); err != nil {
				continue // skip agents not initialized on this machine
			}
			if err := skills.SyncToDir(dir); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			synced++
		}
	}
	return synced, firstErr
}

// View ------------------------------------------------------------------------

func skillSettings(m *model) ggui.Widget {
	pg := newSkillPage()

	shown := ggui.Derived(func() string {
		drafts, o := pg.drafts.Get(), pg.open.Get()
		if slices.ContainsFunc(drafts, func(d skillDraft) bool { return d.Name == o }) {
			return o
		}
		if len(drafts) > 0 {
			return drafts[0].Name
		}
		return ""
	})

	syncNow := func() {
		synced, err := m.syncSkillsToAgents()
		if m == nil || m.toaster == nil {
			return
		}
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not sync skills"), err.Error()).Destructive())
			return
		}
		if synced == 0 {
			m.toaster.Push(ui.Toast(T("Synced skills to agents"), T("No agent skill directories found yet.")))
			return
		}
		m.toaster.Push(ui.Toast(T("Synced skills to agents"), Tf("Synced to %d agent directories", synced)))
	}

	toolsCard := func(compact bool) ggui.Widget {
		buttons := widget.Row(
			ui.Button(T("Open folder"), openFolder(config.SkillsDir)).Ghost(),
			ui.Button(T("Reload"), pg.reload).Ghost(),
			ui.Button(T("Sync to agents"), syncNow).Ghost(),
		).Gap(6)
		hint := T("Customized skills are saved in the skills folder and synced to your agents' skill directories.")
		if compact {
			return card(widget.Box(ui.Tooltip(buttons, hint)).Pad(6, 8))
		}
		return card(stackedRow(T("Skill files"), hint, buttons))
	}

	namesKey := ggui.Derived(func() string {
		var b strings.Builder
		for _, d := range pg.drafts.Get() {
			b.WriteString(d.Name)
			b.WriteByte(',')
		}
		return b.String()
	})

	body := ggui.Key(namesKey, func(string) ggui.Widget {
		drafts := ggui.Peek(pg.drafts)
		items := func(inline bool) ggui.Widget {
			rows := make([]ggui.Widget, 0, len(drafts))
			for _, d := range drafts {
				rows = append(rows, skillItem(m, pg, shown, d.Name, inline))
			}
			return card(rows...)
		}
		detail := ggui.Key(shown, func(name string) ggui.Widget {
			if name == "" {
				return widget.Box()
			}
			return skillDetail(m, pg, name)
		})
		return listBeside(
			widget.Column(items(false), toolsCard(true)).Gap(20).Stretch(),
			detail,
			widget.Column(items(true), toolsCard(false)).Gap(20).Stretch(),
		)
	})

	return widePage(T("Skills"),
		T("Built-in skills are synced to your agents' skills directories so they discover Mako workflows automatically. Edit any skill below to override it locally, or reset it to restore the default."),
		body,
	)
}

func skillTag(customized, dirty bool) ggui.Widget {
	tags := make([]ggui.Widget, 0, 2)
	if customized {
		tags = append(tags, widget.Box(widget.Text(T("Customized")).Size(10.5).Color(skin.AccentBright).NoWrap()).
			Pad(1, 6).Border(1, skin.BorderAccent).Radius(4))
	} else {
		tags = append(tags, builtinTag())
	}
	if dirty {
		tags = append(tags, widget.Box(widget.Text(T("Unsaved")).Size(10.5).Color(skin.DotWaiting).NoWrap()).
			Pad(1, 6).Border(1, skin.DotWaiting).Radius(4))
	}
	if len(tags) == 1 {
		return tags[0]
	}
	return widget.Row(tags...).Gap(4).Align(widget.AlignCenter)
}

func skillItem(m *model, pg *skillPage, shown ggui.Readable[string], name string, inline bool) ggui.Widget {
	meta := ggui.Reactive(func() ggui.Widget {
		var d skillDraft
		for _, it := range pg.drafts.Get() {
			if it.Name == name {
				d = it
				break
			}
		}
		return widget.Row(
			skillTag(d.Customized, d.dirty()),
			widget.Expanded(widget.Text(d.Description).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()),
		).Gap(8).Align(widget.AlignCenter)
	})
	label := []ggui.Widget{
		icon("sparkles").Size(15).Color(skin.Muted),
		widget.Text(name).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
		widget.Expanded(meta),
	}
	if !inline {
		chosen := ggui.Map(shown, func(s string) bool { return s == name })
		return widget.Box(listItem(widget.Row(label...).Gap(8).Align(widget.AlignCenter), chosen,
			func() { pg.open.Set(name) }, Tf("%s skill", name))).Pad(4, 8)
	}
	isOpen := ggui.Map(pg.open, func(o string) bool { return o == name })
	head := ui.ButtonOf(widget.Row(append(label, chevron(isOpen, 13, skin.ExtraMuted))...).Gap(8).Align(widget.AlignCenter),
		func() { pg.open.Set(pick(ggui.Peek(pg.open) == name, "", name)) }).
		Ghost().Pad(8, 10).Radius(7).Name(Tf("%s skill", name))
	return widget.Box(widget.Column(
		head,
		reveal(isOpen, func() ggui.Widget { return widget.Padding(skillBody(m, pg, name), 6, 10, 10, 35) }),
	).Stretch()).Pad(4, 8)
}

func skillDetail(m *model, pg *skillPage, name string) ggui.Widget {
	header := ggui.Reactive(func() ggui.Widget {
		var d skillDraft
		for _, it := range pg.drafts.Get() {
			if it.Name == name {
				d = it
				break
			}
		}
		modeBtn := ui.Button(pick(d.Editing, T("Preview"), T("Edit")), func() {
			pg.update(name, func(x *skillDraft) { x.Editing = !x.Editing })
		}).Outline()
		return widget.Row(
			icon("sparkles").Size(18).Color(skin.Muted),
			widget.Text(name).Size(16).Weight(ggui.WeightSemibold).NoWrap(),
			skillTag(d.Customized, d.dirty()),
			widget.Spacer(),
			modeBtn,
		).Gap(10).Align(widget.AlignCenter)
	})
	return detailCard(header, skillBody(m, pg, name))
}

func skillBody(m *model, pg *skillPage, name string) ggui.Widget {
	// Rebuild when switching between view/edit/builtin-diff modes, NOT while
	// typing into the editor, so the text field keeps focus and caret position.
	mode := ggui.Derived(func() string {
		for _, d := range pg.drafts.Get() {
			if d.Name == name {
				return fmt.Sprintf("%v:%v:%v", d.Editing, d.ShowBuiltin, d.Customized)
			}
		}
		return ""
	})

	contentBind := pg.field(name, func(d skillDraft) string { return d.Content }, func(d *skillDraft, v string) { d.Content = v })

	return ggui.Key(mode, func(string) ggui.Widget {
		d, ok := pg.draft(name)
		if !ok {
			return widget.Box()
		}
		var rows []ggui.Widget
		if d.Description != "" {
			rows = append(rows, widget.Text(d.Description).Size(13))
		}
		if path, err := skills.OverridePath(name); err == nil {
			rows = append(rows, widget.Text(prettyPath(path)).Size(11.5).Font(ggui.DefaultMonoFont()).Color(skin.ExtraMuted))
		}

		if !d.Editing {
			rows = append(rows,
				codeView(m, name+".md", d.Content),
				widget.Row(append(
					[]ggui.Widget{
						ui.Button(T("Edit"), func() { pg.update(name, func(x *skillDraft) { x.Editing = true }) }),
					},
					func() []ggui.Widget {
						if !d.Customized {
							return nil
						}
						return []ggui.Widget{
							ui.Button(T("Reset to built-in"), func() { pg.askReset(m, name) }).Outline(),
						}
					}()...,
				)...).Gap(8),
			)
			return widget.Column(rows...).Gap(10).Stretch()
		}

		editor := ui.TextField(contentBind).Lines(22).Name(T("Skill content"))
		errLine := ggui.Reactive(func() ggui.Widget {
			for _, it := range pg.drafts.Get() {
				if it.Name == name && it.Err != "" {
					return widget.Text(it.Err).Size(12).Color(skin.DotDown)
				}
			}
			return widget.Box()
		})
		dirtySig := ggui.Derived(func() bool {
			for _, it := range pg.drafts.Get() {
				if it.Name == name {
					return it.dirty()
				}
			}
			return false
		})
		actions := []ggui.Widget{
			ui.Button(T("Save"), func() { pg.save(m, name) }).BindDisabled(ggui.Map(dirtySig, func(dirty bool) bool { return !dirty })),
			ui.Button(T("Revert"), func() { pg.revert(name) }).Outline().BindDisabled(ggui.Map(dirtySig, func(dirty bool) bool { return !dirty })),
			ui.Button(pick(d.ShowBuiltin, T("Hide built-in"), T("View built-in")), func() {
				pg.update(name, func(x *skillDraft) { x.ShowBuiltin = !x.ShowBuiltin })
			}).Ghost(),
		}
		if d.Customized {
			actions = append(actions, widget.Spacer(), ui.Button(T("Reset to built-in"), func() { pg.askReset(m, name) }).Outline())
		}
		rows = append(rows,
			editor,
			errLine,
			widget.Row(actions...).Gap(8).Align(widget.AlignCenter),
		)
		if d.ShowBuiltin {
			rows = append(rows,
				hline(),
				muted(widget.Text(T("Built-in source")).Size(12)),
				codeView(m, name+"-builtin.md", d.Builtin),
			)
		}
		return widget.Column(rows...).Gap(10).Stretch()
	})
}
