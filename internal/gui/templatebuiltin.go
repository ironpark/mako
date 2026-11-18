package gui

import (
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// A built-in template on Settings ▸ Templates is read, not edited: its
// line marks it built in, and its page shows what it is for, what it
// takes and its file, with Copy to edit, which starts a template of the
// user's of its name to stand in for it (templatesettings.go).

// builtinItem is a built-in template's line in the list, as templateItem
// is a template's of the user's.
func builtinItem(m *model, pg *templatePage, shown ggui.Readable[int], l builtinLine, inline bool) ggui.Widget {
	id, t := l.ID, l.T
	summary := strings.TrimSpace(t.Description + "  " + t.Usage())
	label := []ggui.Widget{
		icon("package").Size(15).Color(skin.Muted),
		widget.Text("//" + t.Name).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
		builtinTag(),
		widget.Expanded(widget.Text(summary).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()),
	}
	if !inline {
		chosen := ggui.Map(shown, func(s int) bool { return s == id })
		return widget.Box(listItem(widget.Row(label...).Gap(10).Align(widget.AlignCenter), chosen,
			func() { pg.open.Set(id) }, Tf("%s template", t.Name))).Pad(4, 8)
	}
	isOpen := ggui.Map(pg.open, func(o int) bool { return o == id })
	head := ui.ButtonOf(widget.Row(append(label, chevron(isOpen, 13, skin.ExtraMuted))...).Gap(10).Align(widget.AlignCenter),
		func() { pg.open.Set(pick(ggui.Peek(pg.open) == id, 0, id)) }).
		Ghost().Pad(8, 10).Radius(7).Name(Tf("%s template", t.Name))
	return widget.Box(widget.Column(
		head,
		reveal(isOpen, func() ggui.Widget { return widget.Padding(builtinView(m, pg, t), 6, 10, 10, 35) }),
	).Stretch()).Pad(4, 8)
}

// builtinTag marks a template built in.
func builtinTag() ggui.Widget {
	return widget.Box(widget.Text(T("Built in")).Size(10.5).Color(skin.Muted).NoWrap()).
		Pad(1, 6).Border(1, skin.Border).Radius(4)
}

// builtinDetail is the built-in template shown beside the list.
func builtinDetail(m *model, pg *templatePage, t *prompttmpl.Template) ggui.Widget {
	return detailCard(widget.Row(
		icon("package").Size(18).Color(skin.Muted),
		widget.Text("//"+t.Name).Size(16).Weight(ggui.WeightSemibold).NoWrap(),
		builtinTag(),
	).Gap(10).Align(widget.AlignCenter), builtinView(m, pg, t))
}

// builtinView is what a built-in template is for, what it takes, its file,
// and Copy to edit.
func builtinView(m *model, pg *templatePage, t *prompttmpl.Template) ggui.Widget {
	rows := []ggui.Widget{}
	if t.Description != "" {
		rows = append(rows, widget.Text(t.Description).Size(13))
	}
	rows = append(rows,
		widget.Text("//"+t.Name+" "+t.Usage()).Size(12).Font(ggui.DefaultMonoFont()).Color(skin.ExtraMuted),
		muted(widget.Text(T("Built in to mako. Copy it to edit it: your copy, a template file of the same name, stands in for it until you delete it.")).Size(12)),
		codeView(m, t.Name+".md", string(prompttmpl.BuiltinSource(t.Name))),
		widget.Row(ui.Button(T("Copy to edit"), func() { pg.copyBuiltin(t) })).Gap(8),
	)
	return widget.Column(rows...).Gap(10).Stretch()
}
