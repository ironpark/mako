package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/botavatar"
	"github.com/ironpark/mako/internal/protocol"
)

func TestSessionAvatarStateMapping(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	cases := []struct {
		name string
		s    *protocol.SessionInfo
		want botavatar.State
	}{
		{"idle", &protocol.SessionInfo{Id: "s1", Agent: "claude", State: protocol.StateRunning}, botavatar.StateIdle},
		{"working", &protocol.SessionInfo{Id: "s2", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentWorking}, botavatar.StateWorking},
		{"compacting", &protocol.SessionInfo{Id: "s3", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentCompacting}, botavatar.StateThinking},
		{"waiting", &protocol.SessionInfo{Id: "s4", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentWaiting}, botavatar.StateWaiting},
		{"approval", &protocol.SessionInfo{Id: "s5", Agent: "claude", State: protocol.StateRunning, Approval: &protocol.Approval{Tool: "bash"}}, botavatar.StateBlocked},
		{"done", &protocol.SessionInfo{Id: "s6", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentDone}, botavatar.StateDone},
	}
	for _, tc := range cases {
		if got := m.sessionAvatarState(tc.s); got != tc.want {
			t.Errorf("%s: sessionAvatarState = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBotAvatarBlockAndSettingsRender(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s1", NodeId: "n1", Agent: "claude", Project: "/work/mako", State: protocol.StateRunning, Status: protocol.AgentWorking},
	})
	m.Titles.Set(map[string]string{"s1": "Build feature"})
	m.showTab("s1")

	// Render the built-in mako.avatar block.
	blockProbe := ggui.ProbeBuilder(func() ggui.Widget { return avatarBlockPanel(m) }, ggui.Sz(320, 320))
	defer blockProbe.Close()
	sem := blockProbe.Semantics().String()
	if !strings.Contains(sem, "Brain") {
		t.Fatalf("expected Brain bot avatar SVG in mako.avatar semantics, got:\n%s", sem)
	}
	if !strings.Contains(sem, "Build feature") {
		t.Fatalf("expected Build feature session title in mako.avatar semantics, got:\n%s", sem)
	}
	if _, ok := blockProbe.Find(T("Avatar")); !ok {
		t.Fatal("expected Avatar picker menu in mako.avatar block")
	}
	if _, ok := blockProbe.Find(T("Neutral")); !ok {
		t.Fatal("expected Neutral mood toggle button in mako.avatar block")
	}
	blockProbe.Tap(T("Neutral"))
	if got := ggui.Peek(m.Prefs).BotAvatar.Mood; got != string(botavatar.MoodHappy) {
		t.Fatalf("tapping mood toggle gave %q, want happy", got)
	}

	// Render Appearance settings including Bot avatar gallery & preview.
	settingsProbe := ggui.ProbeBuilder(func() ggui.Widget { return botAvatarSettings(m) }, ggui.Sz(900, 700))
	defer settingsProbe.Close()
	if _, ok := settingsProbe.Find(T("Show bot avatars")); !ok {
		t.Fatal("expected Show bot avatars switch in settings")
	}
	if _, ok := settingsProbe.Find("Mug"); !ok {
		t.Fatal("expected Mug avatar button in settings gallery")
	}
}
