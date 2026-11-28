package gui

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

func userMsgs(texts ...string) []*protocol.ChatMessage {
	var out []*protocol.ChatMessage
	for _, t := range texts {
		out = append(out, &protocol.ChatMessage{Role: "user", Text: t}, &protocol.ChatMessage{Role: "assistant", Text: "ok"})
	}
	return out
}

func turnCP(key, prompt string) gitstat.CheckpointEntry {
	return gitstat.CheckpointEntry{Key: key, Kind: gitstat.CheckpointTurn, Commit: key + "c", Prompt: gitstat.PromptKey(prompt)}
}

// A message's checkpoint is the one its words were captured with, counted
// back past the later messages with the same words; never another turn's.
func TestTurnCheckpointIsTheMessagesOwn(t *testing.T) {
	msgs := userMsgs("fix it", "test it", "fix it", "ship it")
	// Newest first; "test it" was never captured, and a restore sits
	// among them.
	list := []gitstat.CheckpointEntry{
		turnCP("k4", "ship it"),
		{Key: "r1", Kind: gitstat.CheckpointPreRestore, Commit: "r1c"},
		turnCP("k3", "fix it"),
		turnCP("k1", "fix it"),
	}
	for i, want := range []string{"k1", "", "k3", "k4"} {
		tp, ok := userTurnPrompt(msgs, nil, i)
		if !ok {
			t.Fatalf("turn %d not found", i)
		}
		if got := turnCheckpoint(list, tp); got != want {
			t.Fatalf("turn %d (%q): checkpoint %q, want %q", i, msgs[2*i].GetText(), got, want)
		}
	}
	// A message sent and not logged yet is later than every logged one.
	tp, _ := userTurnPrompt(msgs, []string{"ship it"}, 3)
	list = append([]gitstat.CheckpointEntry{turnCP("k5", "ship it")}, list...)
	if got := turnCheckpoint(list, tp); got != "k4" {
		t.Fatalf("with a message pending: %q", got)
	}
	if _, ok := userTurnPrompt(msgs, nil, 4); ok {
		t.Fatal("a turn past the messages")
	}
}

func rewindModel(t *testing.T) (*model, *protocol.SessionInfo) {
	t.Helper()
	m := newModel(config.Default(), "")
	info := &protocol.SessionInfo{Id: "s1", Agent: "claude", ConversationId: "c1", NodeId: "self", Project: "/repo", Branch: "main"}
	m.Sessions.Set([]*protocol.SessionInfo{info})
	m.chatState("s1").result.Set(chatResult{Chat: &protocol.Chat{Messages: userMsgs("one", "two")}, Loaded: true})
	return m, info
}

func listing(t *testing.T, list ...gitstat.CheckpointEntry) *protocol.GitResult {
	t.Helper()
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return &protocol.GitResult{Output: string(raw)}
}

// A listing for an earlier opening of the dialog does not land in a later
// one, and one that fails says why.
func TestRewindDialogTakesItsOwnListing(t *testing.T) {
	m, info := rewindModel(t)
	st := m.Rewind
	msgs := ggui.Peek(m.chatState("s1").result).Chat.GetMessages()

	m.openRewindDialog("s1", info, msgs[0], 0, true)
	first := st.gen
	m.openRewindDialog("s1", info, msgs[2], 1, true)
	m.rewindListed(first, listing(t, turnCP("k1", "one")), nil)
	if !ggui.Peek(st.Loading) || len(ggui.Peek(st.Checkpoints)) != 0 {
		t.Fatal("an earlier opening's listing landed")
	}
	m.rewindListed(st.gen, listing(t, turnCP("k2", "two"), turnCP("k1", "one")), nil)
	if got := ggui.Peek(st.Selected); got != "k2" || ggui.Peek(st.Own) != "k2" || ggui.Peek(st.Loading) {
		t.Fatalf("selected %q own %q", got, ggui.Peek(st.Own))
	}

	m.openRewindDialog("s1", info, msgs[0], 0, true)
	m.closeRewindDialog()
	m.rewindListed(st.gen-1, listing(t, turnCP("k1", "one")), nil)
	if len(ggui.Peek(st.Checkpoints)) != 0 {
		t.Fatal("a closed dialog's listing landed")
	}

	m.openRewindDialog("s1", info, msgs[0], 0, true)
	m.rewindListed(st.gen, nil, errors.New("disk on fire"))
	if ggui.Peek(st.Err) == "" || ggui.Peek(st.Loading) {
		t.Fatalf("a failed listing: err %q", ggui.Peek(st.Err))
	}
}

// On an SSH host without makod, the code is not rewound, and the dialog
// says why; a session that cannot rewind its conversation there has no
// rewind at all.
func TestRewindOfCodeNeedsMakodOverSSH(t *testing.T) {
	m, info := rewindModel(t)
	if code, why := m.codeRewind(info); !code || why != "" {
		t.Fatalf("local code rewind: %v %q", code, why)
	}
	ssh := &protocol.SessionInfo{Id: "s2", Agent: "opencode", NodeId: "ssh:box", Project: "/srv/repo", Branch: "main"}
	m.Nodes.Set([]*protocol.Node{{Id: "ssh:box", Features: []string{sshremote.FeatureSSH, sshremote.FeatureSSHDirect}}})
	code, why := m.codeRewind(ssh)
	if code || why == "" {
		t.Fatalf("direct SSH code rewind: %v %q", code, why)
	}
	msg := &protocol.ChatMessage{Role: "user", Text: "x"}
	if canRewindTurn(ssh, msg, 0, true, code) {
		t.Fatal("rewind offered with nothing to rewind")
	}
	m.Nodes.Set([]*protocol.Node{{Id: "ssh:box", Features: []string{sshremote.FeatureSSH, sshremote.FeatureSSHDaemon}}})
	if code, _ := m.codeRewind(ssh); !code {
		t.Fatal("makod over SSH rewinds code")
	}
	m.openRewindDialog("s2", ssh, msg, 0, false)
	if !m.Rewind.Code || !ggui.Peek(m.Rewind.Loading) {
		t.Fatal("the dialog does not list a makod host's checkpoints")
	}
}

// The conversation is rewound only as far as the loaded chat knows it: a
// Codex message of a partial history counts its turn from where the
// loading starts, so only its code is offered.
func TestRewindPassesTheHistoryItKnows(t *testing.T) {
	m, _ := rewindModel(t)
	codex := &protocol.SessionInfo{Id: "s1", Agent: "codex", ConversationId: "th", NodeId: "self", Project: "/repo", Branch: "main"}
	msg := &protocol.ChatMessage{Role: "user", Text: "two"}
	m.askRewindSession("s1", codex, msg, turnRewind{UserTurnIndex: 1, FullHistory: false})
	if m.Rewind.CanConv {
		t.Fatal("a partial Codex history rewinds its conversation")
	}
	m.askRewindSession("s1", codex, msg, turnRewind{UserTurnIndex: 1, FullHistory: true})
	if !m.Rewind.CanConv {
		t.Fatal("a whole Codex history does not rewind its conversation")
	}
}
