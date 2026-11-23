package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

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
	t.Setenv("MAKO_HOME", t.TempDir())
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

func TestAgentRowRendersProfileBotAvatar(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1"}, "")
	sess := &protocol.SessionInfo{
		Id:      "b293a229",
		NodeId:  "n1",
		Agent:   "agy",
		Project: "/work/mako",
		State:   protocol.StateRunning,
	}
	tag := agentTag{Host: "studio.local", OS: "darwin", Here: true}

	// Detailed profile avatar renders a 28px bot SVG inside a 36×36 badge.
	detailedAvatar := ggui.ProbeBuilder(func() ggui.Widget {
		return sessionProfileAvatar(m, sess, false)
	}, ggui.Sz(80, 80))
	defer detailedAvatar.Close()
	img, ok := detailedAvatar.Semantics().Find(ggui.RoleImage, "Cloud")
	if !ok {
		t.Fatalf("expected Cloud bot avatar image in detailed sessionProfileAvatar:\n%s", detailedAvatar.Semantics().String())
	}
	if img.Rect.Size.W < 26 || img.Rect.Size.H < 26 {
		t.Fatalf("expected profile-sized bot avatar (>=26px), got %.1f×%.1f", img.Rect.Size.W, img.Rect.Size.H)
	}

	// Compact profile avatar renders a smaller single-line avatar.
	compactAvatar := ggui.ProbeBuilder(func() ggui.Widget {
		return sessionProfileAvatar(m, sess, true)
	}, ggui.Sz(80, 80))
	defer compactAvatar.Close()
	cimg, ok := compactAvatar.Semantics().Find(ggui.RoleImage, "Cloud")
	if !ok {
		t.Fatalf("expected Cloud bot avatar image in compact sessionProfileAvatar:\n%s", compactAvatar.Semantics().String())
	}
	if cimg.Rect.Size.W >= img.Rect.Size.W {
		t.Fatalf("expected compact avatar (%.1f) smaller than detailed avatar (%.1f)", cimg.Rect.Size.W, img.Rect.Size.W)
	}

	// Full agentRow renders cleanly with the profile avatar beside the 2-line column.
	detailedRow := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Column(agentRow(m, sess, false, "agy session", tag, false)).Stretch()
	}, ggui.Sz(300, 120))
	defer detailedRow.Close()
	dBtn, ok := detailedRow.Semantics().Find(ggui.RoleButton, "agy session")
	if !ok {
		t.Fatalf("expected agy session row button:\n%s", detailedRow.Semantics().String())
	}
	compactRow := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Column(agentRow(m, sess, false, "agy session", tag, true)).Stretch()
	}, ggui.Sz(300, 120))
	defer compactRow.Close()
	cBtn, ok := compactRow.Semantics().Find(ggui.RoleButton, "agy session")
	if !ok {
		t.Fatalf("expected compact agy session row button:\n%s", compactRow.Semantics().String())
	}
	if dBtn.Rect.Size.H <= cBtn.Rect.Size.H {
		t.Fatalf("expected detailed row height (%.1f) > compact row height (%.1f)", dBtn.Rect.Size.H, cBtn.Rect.Size.H)
	}
}
