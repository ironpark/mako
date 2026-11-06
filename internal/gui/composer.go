package gui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
)

// The message composer's state: drafts, attachments, sending and recall.

// composerDraft is a session's message draft, kept with its chat state so
// closing the tab drops it.
func (m *model) composerDraft(id string) string { return ggui.Peek(m.chatState(id).draft) }

func (m *model) setComposerDraft(id, text string) { m.chatState(id).draft.Set(text) }

// chatTarget is the session a message would go to: the focused one while it
// shows Chat, else a pane that does.
func (m *model) chatTarget() string {
	if id := m.Selected.Get(); isSessionTab(id) && m.ChatModes.Get()[id] {
		return id
	}
	for _, g := range m.Layout.Get().groups() {
		if id := g.Active; isSessionTab(id) && m.ChatModes.Get()[id] {
			return id
		}
	}
	return ""
}

// attachTo adds the files at paths, on session id's node, to what its draft
// goes with, each once.
func (m *model) attachTo(id string, paths []string) {
	m.chatState(id).attached.Update(func(v []string) []string {
		v = slices.Clone(v)
		for _, p := range paths {
			if !slices.Contains(v, p) {
				v = append(v, p)
			}
		}
		return v
	})
}

// detachFrom takes the file at path off what session id's draft goes with.
func (m *model) detachFrom(id, path string) {
	m.chatState(id).attached.Update(func(v []string) []string {
		return slices.DeleteFunc(slices.Clone(v), func(p string) bool { return p == path })
	})
}

// sendable reports whether the composer has a message to send: text, or
// files. Reactive.
func (s *chatState) sendable() bool {
	return strings.TrimSpace(s.draft.Get()) != "" || len(s.attached.Get()) > 0
}

// composed is the message a draft and the files it goes with make: the
// text, then each file's path, from the project when it is in it, as the
// agent's own terminal types a file dropped on it.
func composed(text string, attached []string, project string) string {
	if len(attached) == 0 {
		return text
	}
	paths := make([]string, len(attached))
	for i, p := range attached {
		paths[i] = cmp.Or(subfolder(p, project), p)
	}
	text = strings.TrimRight(text, " \t")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += " "
	}
	return text + strings.TrimSuffix(shellPaths(paths), " ")
}

// sendComposer types a session's draft into its terminal, with the files
// it goes with, followed by Enter, and clears both.
func (m *model) sendComposer(id string) {
	conn := m.conns[id]
	if conn == nil {
		return
	}
	state := m.chatState(id)
	s, _ := m.session(id)
	text := composed(m.composerDraft(id), ggui.Peek(state.attached), s.GetProject())
	m.submit(id, conn, text)
	m.notePending(id, text)
	if strings.TrimSpace(text) != "" && (len(state.sent) == 0 || state.sent[len(state.sent)-1] != text) {
		state.sent = append(state.sent, text)
		if len(state.sent) > composerHistory {
			state.sent = slices.Delete(state.sent, 0, len(state.sent)-composerHistory)
		}
	}
	m.setComposerDraft(id, "")
	state.attached.Set(nil)
}

// composerHistory is how many sent messages a session's composer keeps.
const composerHistory = 100

// recallComposer brings back the message sent before the one shown, or
// after it for a step of 1, and past the latest an empty box again. It
// does so only over an empty draft or one recalled and left unedited, so
// the arrows still move through text being written, and reports whether
// it did.
func (m *model) recallComposer(id string, step int) bool {
	state := m.chatState(id)
	draft := m.composerDraft(id)
	browsing := state.recall < len(state.sent) && draft == state.sent[state.recall]
	if !browsing {
		if draft != "" || step > 0 {
			return false
		}
		state.recall = len(state.sent)
	}
	next := state.recall + step
	if next < 0 || next > len(state.sent) {
		return browsing // at the oldest, the arrow stays with the history
	}
	state.recall = next
	if next == len(state.sent) {
		m.setComposerDraft(id, "")
	} else {
		m.setComposerDraft(id, state.sent[next])
	}
	return true
}
