package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
)

// A prompt template an agent wrote (save_template, as the built-in
// //template asks) comes as an approval on its session, holding the file.
// Templates are the window's, so this window saves it, in its own
// templates folder, once the user allows it here: Chat's card shows the
// template, or what it changes in one of the user's it replaces, and the
// bar over the terminal sends the user there to read it first. The daemon
// checked it; it is checked again here, as Settings ▸ Templates would,
// with the name kept in the folder, and a template the agent did not ask
// to replace is not.

// templateRequest is a template an agent asks to save, as this window
// reads it: the file, read; why it does not; and the user's own file of
// the name, nil for none.
type templateRequest struct {
	Name, Content string
	Replace       bool
	t             *prompttmpl.Template
	err           error
	old           []byte
}

// errTemplateTaken is what the agent is told of a name the user has a
// template of, which it did not ask to replace.
var errTemplateTaken = errors.New("the user already has a template of that name; choose another name, or call save_template again with replace set if they asked to change it")

// readTemplateRequest reads the template approval a holds.
func readTemplateRequest(a *protocol.Approval) templateRequest {
	var in struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Replace bool   `json:"replace"`
	}
	r := templateRequest{}
	if err := json.Unmarshal([]byte(a.GetInput()), &in); err != nil {
		r.err = fmt.Errorf("the request does not read: %w", err)
		return r
	}
	r.Name, r.Content, r.Replace = in.Name, in.Content, in.Replace
	if !prompttmpl.ValidName(r.Name) || filepath.Base(r.Name) != r.Name {
		r.err = fmt.Errorf("%q is not a template's name", r.Name)
		return r
	}
	if r.t, r.err = prompttmpl.Parse(r.Name, []byte(r.Content)); r.err != nil {
		return r
	}
	if dir, err := config.TemplatesDir(); err == nil {
		r.old, _ = os.ReadFile(filepath.Join(dir, r.Name+".md"))
	}
	return r
}

// taken reports whether saving r would replace a template, the user's or
// a built-in, that the agent did not ask to replace.
func (r templateRequest) taken() bool {
	return !r.Replace && (r.old != nil || prompttmpl.BuiltinTemplate(r.Name) != nil)
}

// save writes r into the templates folder, as it was read.
func (r templateRequest) save() error {
	switch {
	case r.err != nil:
		return r.err
	case r.taken():
		return errTemplateTaken
	}
	dir, err := config.TemplatesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.Write(filepath.Join(dir, r.Name+".md"), []byte(r.Content))
}

// saveTemplateApproval saves the template approval a of session id's
// agent asks for, and answers it as answerTemplate says. Saved, it is in
// // at once: every composer reads the templates again.
func (m *model) saveTemplateApproval(id string, a *protocol.Approval) {
	answer := m.answerTemplate(a)
	m.answerApproval(id, answer)
	if answer.GetOption() == protocol.TemplateSaved {
		for _, state := range m.chats {
			m.readTemplates(state, true)
		}
	}
}

// answerTemplate saves the template approval a asks for, and is the
// answer to it: allowed, as saved, once it is; declined, with why it
// could not be, to the agent, otherwise.
func (m *model) answerTemplate(a *protocol.Approval) *protocol.AnswerApproval {
	r := readTemplateRequest(a)
	if err := r.save(); err != nil {
		if r.err == nil && !errors.Is(err, errTemplateTaken) {
			m.toaster.Push(ui.Toast(T("Could not save the template"), err.Error()).Destructive())
		}
		return &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionDeny,
			Message: "the window could not save it: " + err.Error()}
	}
	return &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Option: protocol.TemplateSaved}
}

// templateApprovalCard is a template's approval at the foot of Chat: what
// it is called and takes, the file, or the change to the user's own, and
// the answers.
func templateApprovalCard(m *model, id string, a *protocol.Approval) ggui.Widget {
	r := readTemplateRequest(a)
	why := ggui.State("")
	deny := func() { denyWith(m, id, a, ggui.Peek(why)) }
	title := Tf("Save the template //%s?", r.Name)
	switch {
	case r.Replace && r.old != nil:
		title = Tf("Replace your template //%s?", r.Name)
	case r.Replace && prompttmpl.BuiltinTemplate(r.Name) != nil:
		title = Tf("Stand in for the built-in //%s?", r.Name)
	}
	rows := []ggui.Widget{widget.Row(
		icon("file-text").Size(16).Color(skin.DotWaiting),
		widget.Expanded(widget.Text(title).Size(13.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
	).Gap(8).Align(widget.AlignCenter)}
	if r.t != nil && r.t.Description != "" {
		rows = append(rows, muted(widget.Text(r.t.Description).Size(12.5)))
	}
	if a.GetDetail() != "" {
		rows = append(rows, widget.Text(a.GetDetail()).Size(12).Font(ggui.DefaultMonoFont()).Color(skin.ExtraMuted))
	}
	// The file is capped lower than a tool call's, as the generic card's
	// arguments are, so that the composer keeps its room under it.
	file := codeView(m, r.Name+".md", r.Content)
	if r.old != nil && r.Replace {
		file = scrolled(editDiff(string(r.old), r.Content))
	}
	rows = append(rows, widget.Box(file).MaxHeight(260))
	var problem string
	switch {
	case r.err != nil:
		problem = Tf("It does not read: %s", r.err.Error())
	case r.taken():
		problem = Tf("You have a template named //%s, and the agent did not ask to replace it.", r.Name)
		deny = func() {
			msg := ggui.Peek(why)
			if msg == "" {
				msg = errTemplateTaken.Error()
			}
			denyWith(m, id, a, msg)
		}
	}
	var buttons []ggui.Widget
	if problem != "" {
		rows = append(rows, widget.Text(problem).Size(12.5).Color(uitheme.Destructive))
	} else {
		save := ui.Button(T("Save template"), func() { m.saveTemplateApproval(id, a) })
		buttons = append(buttons, ui.Tooltip(save, chordLabel("cmd+enter")))
		rows = append(rows, muted(widget.Text(T("It is saved in this window's templates folder, and called in Chat as // and its name.")).Size(12)))
	}
	rows = append(rows,
		widget.Wrap(buttons...).Gap(8).RunGap(8).Align(widget.AlignCenter),
		widget.Row(
			widget.Expanded(ui.TextField(why).Placeholder(T("Tell the agent what to do instead")).Name(T("Why not")).OnSubmit(func(string) { deny() })),
			ui.Tooltip(ui.Button(T("Decline"), deny).Outline(), chordLabel("cmd+.")),
		).Gap(8).Align(widget.AlignCenter),
	)
	return approvalFrame(widget.Column(rows...).Gap(10).Stretch())
}
