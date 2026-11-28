package gui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

// The rewind dialog rewinds a session from right before one of the user's
// messages: its conversation, forked into a new session, its code, from the
// checkpoint the daemon captured as that turn started, or both. A restore's
// safety snapshot is listed with the checkpoints, to undo it with.

// rewindDialogState is the rewind dialog's. The fields that are not states
// are set as it opens, before Open.
type rewindDialogState struct {
	Open *ggui.StateValue[bool]
	// gen counts the openings: a listing answered for an earlier one is
	// not this one's.
	gen           int
	SessionID     string
	Info          *protocol.SessionInfo
	MessageID     string
	UserTurnIndex int
	PromptText    string
	CanConv       bool
	// Code says whether the session's code can be rewound; CodeNote, when
	// it is in a repository and cannot, why.
	Code     bool
	CodeNote string
	// Turn is the message's, to find its checkpoint by; Texts are the
	// loaded user messages, by their PromptKey, to name checkpoints by.
	Turn  turnPrompt
	Texts map[string]string

	Checkpoints *ggui.StateValue[[]gitstat.CheckpointEntry]
	Selected    *ggui.StateValue[string] // the Key of the checkpoint chosen, "" for none
	Own         *ggui.StateValue[string] // the Key of the message's own checkpoint, "" for none
	Loading     *ggui.StateValue[bool]
	Err         *ggui.StateValue[string]
}

func newRewindDialogState() *rewindDialogState {
	return &rewindDialogState{
		Open:        ggui.State(false),
		Checkpoints: ggui.State([]gitstat.CheckpointEntry(nil)),
		Selected:    ggui.State(""),
		Own:         ggui.State(""),
		Loading:     ggui.State(false),
		Err:         ggui.State(""),
	}
}

// turnPrompt is a user message as its turn's checkpoint knows it: the
// PromptKey of its words, and how many later messages have the same.
type turnPrompt struct {
	Key   string
	Later int
}

// userTurnPrompt is the turnPrompt of the user message at userTurnIndex
// among messages, which run to the newest, then those sent and not logged
// yet; ok is false when there is no such message.
func userTurnPrompt(messages []*protocol.ChatMessage, sent []string, userTurnIndex int) (turnPrompt, bool) {
	var keys []string
	for _, msg := range messages {
		if msg.GetRole() == "user" {
			keys = append(keys, gitstat.PromptKey(msg.GetText()))
		}
	}
	if userTurnIndex < 0 || userTurnIndex >= len(keys) || keys[userTurnIndex] == "" {
		return turnPrompt{}, false
	}
	for _, text := range sent {
		keys = append(keys, gitstat.PromptKey(text))
	}
	t := turnPrompt{Key: keys[userTurnIndex]}
	for _, k := range keys[userTurnIndex+1:] {
		if k == t.Key {
			t.Later++
		}
	}
	return t, true
}

// turnCheckpoint is the Key of the checkpoint of turn t's message among
// list, newest first, "" for none: the one of its prompt, as many back as
// later messages have the same words. A turn whose checkpoint was not
// captured has none rather than another's.
func turnCheckpoint(list []gitstat.CheckpointEntry, t turnPrompt) string {
	if t.Key == "" {
		return ""
	}
	seen := 0
	for _, cp := range list {
		if cp.Kind != gitstat.CheckpointTurn || cp.Prompt != t.Key {
			continue
		}
		if seen == t.Later {
			return cp.Key
		}
		seen++
	}
	return ""
}

// directSSH reports whether node is an SSH host reached without makod,
// whose git operations are a few the daemon runs over SSH.
func (m *model) directSSH(node string) bool {
	n, ok := nodeIn(ggui.Peek(m.Nodes), node)
	return ok && n.HasFeature(sshremote.FeatureSSH) && !n.HasFeature(sshremote.FeatureSSHDaemon)
}

// codeRewind says whether a session's code can be rewound, and when it is
// in a repository and cannot, why.
func (m *model) codeRewind(info *protocol.SessionInfo) (bool, string) {
	switch {
	case info.GetBranch() == "" || info.GetProject() == "":
		return false, ""
	case m.directSSH(info.GetNodeId()):
		return false, T("Code checkpoints need makod on this SSH host, so only the conversation can be rewound here.")
	}
	return true, ""
}

func (m *model) openRewindDialog(id string, info *protocol.SessionInfo, msg *protocol.ChatMessage, userTurnIndex int, canConv bool) {
	st := m.Rewind
	st.gen++
	gen := st.gen
	st.SessionID = id
	st.Info = info
	st.MessageID = msg.GetId()
	st.UserTurnIndex = userTurnIndex
	st.PromptText = msg.GetText()
	st.CanConv = canConv
	code, note := m.codeRewind(info)
	st.Code, st.CodeNote = code, note
	st.Turn, st.Texts = turnPrompt{}, map[string]string{}
	state := m.chatState(id)
	messages := chatMessages(ggui.Peek(state.result), ggui.Peek(state.older))
	var sent []string
	for _, p := range ggui.Peek(state.pending) {
		sent = append(sent, p.Text)
	}
	st.Turn, _ = userTurnPrompt(messages, sent, userTurnIndex)
	for _, msg := range messages {
		if msg.GetRole() == "user" {
			st.Texts[gitstat.PromptKey(msg.GetText())] = msg.GetText()
		}
	}
	st.Checkpoints.Set(nil)
	st.Selected.Set("")
	st.Own.Set("")
	st.Err.Set("")
	st.Loading.Set(code)
	st.Open.Set(true)
	if !code {
		return
	}
	req := &protocol.GitRun{
		NodeId:  m.remoteID(info.GetNodeId()),
		Root:    info.GetProject(),
		Op:      protocol.GitOpListCheckpoints,
		Message: id,
	}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(res *protocol.GitResult, err error) { m.rewindListed(gen, res, err) })
}

// rewindListed takes the listing of the checkpoints the dialog's opening
// gen asked for, choosing the message's own if there is one.
func (m *model) rewindListed(gen int, res *protocol.GitResult, err error) {
	st := m.Rewind
	if st.gen != gen {
		return // the dialog was closed, or opened for another message
	}
	st.Loading.Set(false)
	var list []gitstat.CheckpointEntry
	if err == nil {
		if raw := strings.TrimSpace(res.GetOutput()); raw != "" {
			err = json.Unmarshal([]byte(raw), &list)
		}
	}
	if err != nil {
		st.Err.Set(nodeErr(err))
		return
	}
	own := turnCheckpoint(list, st.Turn)
	st.Own.Set(own)
	st.Selected.Set(own)
	st.Checkpoints.Set(list)
}

// closeRewindDialog closes the dialog, and leaves a listing still on its
// way unheard.
func (m *model) closeRewindDialog() {
	m.Rewind.gen++
	m.Rewind.Open.Set(false)
}

// restoreCodeCheckpoint puts the session's files back as checkpoint cp has
// them, reads the open diffs again, and runs after once it has.
func (m *model) restoreCodeCheckpoint(info *protocol.SessionInfo, cp gitstat.CheckpointEntry, after func()) {
	req := &protocol.GitRun{
		NodeId: m.remoteID(info.GetNodeId()),
		Root:   info.GetProject(),
		Op:     protocol.GitOpRestoreCheckpoint,
		Commit: cp.Commit,
	}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(res *protocol.GitResult, err error) {
		// A restore that fails part way has changed files too.
		m.rereadDiffChanges(info.GetNodeId())
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not restore code checkpoint"), nodeErr(err)).Destructive())
			return
		}
		detail := shortHash(cp.Commit)
		var safety gitstat.CheckpointEntry
		if json.Unmarshal([]byte(res.GetOutput()), &safety) == nil && safety.Commit != "" {
			detail = Tf("The files as they were are kept in safety snapshot %s.", shortHash(safety.Commit))
		}
		m.toaster.Push(ui.Toast(T("Code restored to checkpoint"), detail))
		if after != nil {
			after()
		}
	})
}

// checkpointLabel is how the dialog names a checkpoint: when it was taken,
// and of what, with the words of its turn's message when they are loaded.
func checkpointLabel(cp gitstat.CheckpointEntry, texts map[string]string) string {
	when := ""
	if cp.CreatedAt > 0 {
		t := time.UnixMilli(cp.CreatedAt).Local()
		when = t.Format("15:04:05")
		if y, mo, d := t.Date(); y != time.Now().Year() || mo != time.Now().Month() || d != time.Now().Day() {
			when = t.Format("Jan 2 15:04")
		}
	}
	what := Tf("Turn (%s)", shortHash(cp.Commit))
	switch {
	case cp.Kind == gitstat.CheckpointPreRestore:
		what = Tf("Before a restore (%s)", shortHash(cp.Commit))
	case texts[cp.Prompt] != "" && cp.Prompt != "":
		line, _, _ := strings.Cut(strings.TrimSpace(texts[cp.Prompt]), "\n")
		if r := []rune(line); len(r) > 48 {
			line = string(r[:47]) + "…"
		}
		what = "“" + line + "”"
	}
	if when == "" {
		return what
	}
	return when + " · " + what
}

func rewindDialog(m *model) ggui.Widget {
	st := m.Rewind
	body := ggui.Reactive(func() ggui.Widget {
		if !st.Open.Get() || st.Info == nil {
			return widget.Box()
		}
		loading := st.Loading.Get()
		checkpoints := st.Checkpoints.Get()
		selectedKey := st.Selected.Get()
		own := st.Own.Get()
		errText := st.Err.Get()

		rows := []ggui.Widget{
			muted(widget.Text(T("Choose what to rewind from right before this message. A safety snapshot is always saved before restoring files.")).Size(12.5)),
		}
		var selected gitstat.CheckpointEntry
		for _, cp := range checkpoints {
			if cp.Key == selectedKey {
				selected = cp
			}
		}
		switch {
		case !st.Code && st.CodeNote == "":
		case !st.Code:
			rows = append(rows, muted(widget.Text(st.CodeNote).Size(12)))
		case loading:
			rows = append(rows, widget.Row(ui.Spinner().Size(13), muted(widget.Text(T("Loading code checkpoints…")).Size(12.5))).Gap(8).Align(widget.AlignCenter))
		case errText != "":
			rows = append(rows, widget.Row(
				icon("info").Size(14).Color(skin.DotDown),
				widget.Expanded(widget.Text(Tf("Could not list code checkpoints: %s", errText)).Size(12).Color(skin.DotDown).Selectable()),
			).Gap(8))
		case len(checkpoints) > 0:
			opts := make([]string, len(checkpoints))
			byKey := make(map[string]gitstat.CheckpointEntry, len(checkpoints))
			// The turns first, then the restores, each newest first.
			i := 0
			for _, kind := range []string{gitstat.CheckpointTurn, gitstat.CheckpointPreRestore} {
				for _, cp := range checkpoints {
					if cp.Kind == kind {
						opts[i], byKey[cp.Key] = cp.Key, cp
						i++
					}
				}
			}
			opts = opts[:i]
			rows = append(rows,
				widget.Text(T("Code checkpoint")).Size(12.5).Weight(ggui.WeightMedium),
				ui.Select(st.Selected).Options(opts).Placeholder(T("Choose a checkpoint")).
					Format(func(key string) string { return checkpointLabel(byKey[key], st.Texts) }).
					GroupBy(func(key string) string {
						return pick(byKey[key].Kind == gitstat.CheckpointPreRestore, T("Before a restore"), T("Turns"))
					}).
					Name(T("Code checkpoint")),
			)
			switch {
			case selectedKey == "":
				rows = append(rows, muted(widget.Text(T("No checkpoint was captured for this message. Choose one to restore its code.")).Size(12)))
			case selectedKey != own:
				rows = append(rows, muted(widget.Text(T("This checkpoint is not the one captured for this message.")).Size(12)))
			}
		default:
			rows = append(rows, muted(widget.Text(T("No code checkpoints captured yet for this session.")).Size(12)))
		}

		hasCP := selected.Commit != ""
		info, sid, msgID, turnIdx, prompt := st.Info, st.SessionID, st.MessageID, st.UserTurnIndex, st.PromptText
		var buttons []ggui.Widget
		if st.CanConv && hasCP {
			buttons = append(buttons, ui.Button(T("Rewind conversation & code"), func() {
				m.closeRewindDialog()
				m.restoreCodeCheckpoint(info, selected, func() {
					m.rewindSession(sid, info, msgID, turnIdx, prompt)
				})
			}))
		}
		if st.CanConv {
			btn := ui.Button(T("Conversation only"), func() {
				m.closeRewindDialog()
				m.rewindSession(sid, info, msgID, turnIdx, prompt)
			})
			if hasCP {
				btn = btn.Outline()
			}
			buttons = append(buttons, btn)
		}
		if hasCP {
			btn := ui.Button(T("Code only"), func() {
				m.closeRewindDialog()
				m.restoreCodeCheckpoint(info, selected, nil)
			})
			if st.CanConv {
				btn = btn.Outline()
			}
			buttons = append(buttons, btn)
		}
		buttons = append(buttons, ui.Button(T("Cancel"), m.closeRewindDialog).Ghost())

		rows = append(rows, widget.Wrap(buttons...).Gap(8).RunGap(8).Align(widget.AlignCenter))
		return widget.Column(rows...).Gap(12).Stretch()
	})
	return keyedDialog(st.Open, body).Title(T("Rewind from here")).Width(480)
}
