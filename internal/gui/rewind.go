package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
)

// rewindDialogState holds the state for the file-inclusive rewind modal,
// allowing the user to rewind conversation only, code only (from a captured
// git shadow checkpoint), or both together.
type rewindDialogState struct {
	Open          *ggui.StateValue[bool]
	SessionID     string
	Info          *protocol.SessionInfo
	MessageID     string
	UserTurnIndex int
	PromptText    string
	CanConv       bool
	Checkpoints   *ggui.StateValue[[]gitstat.CheckpointEntry]
	SelectedSHA   *ggui.StateValue[string]
	Loading       *ggui.StateValue[bool]
	Err           *ggui.StateValue[string]
}

func newRewindDialogState() *rewindDialogState {
	return &rewindDialogState{
		Open:        ggui.State(false),
		Checkpoints: ggui.State([]gitstat.CheckpointEntry(nil)),
		SelectedSHA: ggui.State(""),
		Loading:     ggui.State(false),
		Err:         ggui.State(""),
	}
}

func (m *model) openRewindDialog(id string, info *protocol.SessionInfo, msg *protocol.ChatMessage, userTurnIndex int, canConv bool) {
	st := m.Rewind
	st.SessionID = id
	st.Info = info
	st.MessageID = msg.GetId()
	st.UserTurnIndex = userTurnIndex
	st.PromptText = msg.GetText()
	st.CanConv = canConv
	st.Checkpoints.Set(nil)
	st.SelectedSHA.Set("")
	st.Err.Set("")
	st.Loading.Set(true)
	st.Open.Set(true)

	req := &protocol.GitRun{
		NodeId:  m.remoteID(info.GetNodeId()),
		Root:    info.GetProject(),
		Op:      protocol.GitOpListCheckpoints,
		Message: id,
	}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(res *protocol.GitResult, err error) {
		st.Loading.Set(false)
		if err != nil {
			// Not a git repo or older daemon: conversation-only rewind still works.
			return
		}
		var list []gitstat.CheckpointEntry
		if raw := strings.TrimSpace(res.GetOutput()); raw != "" {
			_ = json.Unmarshal([]byte(raw), &list)
		}
		st.Checkpoints.Set(list)
		if len(list) > 0 {
			st.SelectedSHA.Set(list[0].Commit)
		}
	})
}

func (m *model) captureSessionCheckpoint(id string) {
	info, ok := m.session(id)
	if !ok || info.GetProject() == "" {
		return
	}
	key := fmt.Sprintf("turn-%d", time.Now().UnixMilli())
	req := &protocol.GitRun{
		NodeId:  m.remoteID(info.GetNodeId()),
		Root:    info.GetProject(),
		Op:      protocol.GitOpCheckpoint,
		Message: id + ":" + key,
	}
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(_ *protocol.GitResult, _ error) {})
}

func (m *model) restoreCodeCheckpoint(info *protocol.SessionInfo, sha string, after func()) {
	req := &protocol.GitRun{
		NodeId: m.remoteID(info.GetNodeId()),
		Root:   info.GetProject(),
		Op:     protocol.GitOpRestoreCheckpoint,
		Commit: sha,
	}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.GitResult, error) {
		return c.GitRun(ctx, req)
	}, func(_ *protocol.GitResult, err error) {
		if err != nil {
			m.toaster.Push(ui.Toast(T("Could not restore code checkpoint"), nodeErr(err)).Destructive())
			return
		}
		m.toaster.Push(ui.Toast(T("Code restored to checkpoint"), shortHash(sha)))
		if after != nil {
			after()
		}
	})
}

func rewindDialog(m *model) ggui.Widget {
	st := m.Rewind
	body := ggui.Reactive(func() ggui.Widget {
		if !st.Open.Get() || st.Info == nil {
			return widget.Box()
		}
		loading := st.Loading.Get()
		checkpoints := st.Checkpoints.Get()
		selected := st.SelectedSHA.Get()

		rows := []ggui.Widget{
			muted(widget.Text(T("Choose what to rewind from right before this message. A safety snapshot is always saved before restoring files.")).Size(12.5)),
		}

		if loading {
			rows = append(rows, widget.Row(ui.Spinner().Size(13), muted(widget.Text(T("Loading code checkpoints…")).Size(12.5))).Gap(8).Align(widget.AlignCenter))
		} else if len(checkpoints) > 0 {
			rows = append(rows, widget.Text(T("Code checkpoint")).Size(12.5).Weight(ggui.WeightMedium))
			opts := make([]string, 0, len(checkpoints))
			labelBySHA := map[string]string{}
			for _, cp := range checkpoints {
				opts = append(opts, cp.Commit)
				when := ""
				if cp.CreatedAt > 0 {
					when = time.UnixMilli(cp.CreatedAt).Local().Format("15:04:05")
				}
				labelBySHA[cp.Commit] = strings.TrimSpace(fmt.Sprintf("%s · %s (%s)", when, cp.Key, shortHash(cp.Commit)))
			}
			rows = append(rows, ui.Select(st.SelectedSHA).Options(opts).Format(func(sha string) string {
				if l, ok := labelBySHA[sha]; ok {
					return l
				}
				return shortHash(sha)
			}).Name(T("Code checkpoint")))
		} else {
			rows = append(rows, muted(widget.Text(T("No code checkpoints captured yet for this session.")).Size(12)))
		}

		hasCP := selected != ""
		closeDlg := func() { st.Open.Set(false) }

		var buttons []ggui.Widget
		if st.CanConv && hasCP {
			buttons = append(buttons, ui.Button(T("Rewind conversation & code"), func() {
				sha := ggui.Peek(st.SelectedSHA)
				info := st.Info
				sid, msgID, turnIdx, prompt := st.SessionID, st.MessageID, st.UserTurnIndex, st.PromptText
				closeDlg()
				m.restoreCodeCheckpoint(info, sha, func() {
					m.rewindSession(sid, info, msgID, turnIdx, prompt)
				})
			}))
		}
		if st.CanConv {
			btn := ui.Button(T("Conversation only"), func() {
				info := st.Info
				sid, msgID, turnIdx, prompt := st.SessionID, st.MessageID, st.UserTurnIndex, st.PromptText
				closeDlg()
				m.rewindSession(sid, info, msgID, turnIdx, prompt)
			})
			if hasCP {
				btn = btn.Outline()
			}
			buttons = append(buttons, btn)
		}
		if hasCP {
			btn := ui.Button(T("Code only"), func() {
				sha := ggui.Peek(st.SelectedSHA)
				info := st.Info
				closeDlg()
				m.restoreCodeCheckpoint(info, sha, nil)
			})
			if st.CanConv {
				btn = btn.Outline()
			}
			buttons = append(buttons, btn)
		}
		buttons = append(buttons, ui.Button(T("Cancel"), closeDlg).Ghost())

		rows = append(rows, widget.Wrap(buttons...).Gap(8).RunGap(8).Align(widget.AlignCenter))
		return widget.Column(rows...).Gap(12).Stretch()
	})
	return keyedDialog(st.Open, body).Title(T("Rewind from here")).Width(480)
}
