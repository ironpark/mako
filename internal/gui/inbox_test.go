package gui

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

type fakeInboxDaemon struct {
	sock string

	mu        sync.Mutex
	sends     []*protocol.Send
	approvals []*protocol.AnswerApproval
	dialogs   []*protocol.AnswerDialog
}

func startFakeInboxDaemon(t *testing.T) *fakeInboxDaemon {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fake daemon test runs on unix")
	}
	dir, err := os.MkdirTemp("", "mk-inbox")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	fd := &fakeInboxDaemon{sock: sock}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					env, err := protocol.ReadFrame(c)
					if err != nil {
						return
					}
					switch env.Type() {
					case protocol.TypeHello:
						if reply, err := protocol.Encode(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{
							Node: &protocol.Node{Id: "local", Name: "local", Revision: protocol.Revision},
						}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					case protocol.TypeSend:
						req := proto.CloneOf(env.GetSend())
						fd.mu.Lock()
						fd.sends = append(fd.sends, req)
						fd.mu.Unlock()
						if reply, err := protocol.Encode(protocol.TypeSessionInfo, env.GetId(), &protocol.SessionInfo{Id: req.GetSessionId(), State: protocol.StateRunning}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					case protocol.TypeAnswerApproval:
						req := proto.CloneOf(env.GetAnswerApproval())
						fd.mu.Lock()
						fd.approvals = append(fd.approvals, req)
						fd.mu.Unlock()
						if reply, err := protocol.Encode(protocol.TypeSessionInfo, env.GetId(), &protocol.SessionInfo{Id: req.GetSessionId(), State: protocol.StateRunning}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					case protocol.TypeAnswerDialog:
						req := proto.CloneOf(env.GetAnswerDialog())
						fd.mu.Lock()
						fd.dialogs = append(fd.dialogs, req)
						fd.mu.Unlock()
						if reply, err := protocol.Encode(protocol.TypeSessionInfo, env.GetId(), &protocol.SessionInfo{Id: req.GetSessionId(), State: protocol.StateRunning}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					case protocol.TypeSeeSession:
						req := env.GetSeeSession()
						if reply, err := protocol.Encode(protocol.TypeSessionInfo, env.GetId(), &protocol.SessionInfo{Id: req.GetSessionId(), State: protocol.StateRunning}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					}
				}
			}(conn)
		}
	}()
	return fd
}

func (fd *fakeInboxDaemon) gotSends() []*protocol.Send {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return slices.Clone(fd.sends)
}

func (fd *fakeInboxDaemon) gotApprovals() []*protocol.AnswerApproval {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return slices.Clone(fd.approvals)
}

func (fd *fakeInboxDaemon) gotDialogs() []*protocol.AnswerDialog {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return slices.Clone(fd.dialogs)
}

func TestCollectInboxItemsPriorityAndFiltering(t *testing.T) {
	now := time.Now()
	sessions := []*protocol.SessionInfo{
		{Id: "done-unread", State: protocol.StateRunning, Status: protocol.AgentDone, StatusAt: timestamppb.New(now)},
		{Id: "waiting-1", State: protocol.StateRunning, Status: protocol.AgentWaiting, StatusAt: timestamppb.New(now.Add(-2 * time.Minute))},
		{Id: "auth-err", State: protocol.StateRunning, AuthError: &protocol.AuthError{Summary: "expired"}, StatusAt: timestamppb.New(now.Add(-3 * time.Minute))},
		{Id: "approval-old", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a1", Tool: "Bash"}, StatusAt: timestamppb.New(now.Add(-5 * time.Minute))},
		{Id: "approval-new", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a2", Tool: "Edit"}, StatusAt: timestamppb.New(now.Add(-1 * time.Minute))},
		{Id: "working-quiet", State: protocol.StateRunning, Status: protocol.AgentWorking},
		{Id: "stopped-unread", State: protocol.StateStopped},
	}
	unread := map[string]bool{
		"done-unread":    true,
		"stopped-unread": true,
		"approval-new":   true, // already categorized as approval, must not duplicate
	}

	items := collectInboxItems(sessions, unread)
	wantOrder := []string{"approval-new", "approval-old", "auth-err", "waiting-1", "done-unread", "stopped-unread"}
	if len(items) != len(wantOrder) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(wantOrder), items)
	}
	for i, wantID := range wantOrder {
		if items[i].Session.GetId() != wantID {
			t.Errorf("items[%d] = %s (kind %d), want %s", i, items[i].Session.GetId(), items[i].Kind, wantID)
		}
	}

	if got := filterInboxItems(items, inboxFilterApprovals); len(got) != 2 {
		t.Errorf("approvals filter len = %d, want 2", len(got))
	}
	if got := filterInboxItems(items, inboxFilterWaiting); len(got) != 2 {
		t.Errorf("waiting filter len = %d, want 2", len(got))
	}
	if got := filterInboxItems(items, inboxFilterUnread); len(got) != 2 {
		t.Errorf("unread filter len = %d, want 2", len(got))
	}
}

func TestIsBatchSafeApproval(t *testing.T) {
	cases := []struct {
		name string
		a    *protocol.Approval
		want bool
	}{
		{"nil", nil, false},
		{"bash tool", &protocol.Approval{Id: "1", Tool: "Bash"}, true},
		{"question", &protocol.Approval{Id: "2", Questions: []*protocol.Question{{Question: "Pick?"}}}, false},
		{"plan", &protocol.Approval{Id: "3", Kind: protocol.ToolExitPlan}, false},
		{"layout", &protocol.Approval{Id: "4", Kind: protocol.ToolLayout}, false},
		{"template", &protocol.Approval{Id: "5", Kind: protocol.ToolTemplate}, false},
	}
	for _, tc := range cases {
		if got := isBatchSafeApproval(tc.a); got != tc.want {
			t.Errorf("%s: isBatchSafeApproval = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInboxCardRenderingFilteringAndAuthCommand(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(config.Default(), "")
	now := time.Now()
	longTitle := "Extremely long session title that should ellipsize cleanly without overflowing badges or open session buttons"
	m.Sessions.Set([]*protocol.SessionInfo{
		{
			Id: "s-app", Agent: "claude", Project: "/repo/app", State: protocol.StateRunning,
			Approval: &protocol.Approval{Id: "ap-1", Tool: "Bash", Detail: "go test ./..."},
			StatusAt: timestamppb.New(now),
		},
		{
			Id: "s-auth", Agent: "codex", Project: "/repo/auth", State: protocol.StateRunning,
			AuthError: &protocol.AuthError{
				Summary:  "Codex token expired",
				Guidance: "Sign in again on the host.",
				Command:  "codex login",
			},
			StatusAt: timestamppb.New(now.Add(-time.Minute)),
		},
		{
			Id: "s-wait", Agent: "claude", Project: "/repo/wait", State: protocol.StateRunning,
			Status:   protocol.AgentWaiting,
			StatusAt: timestamppb.New(now.Add(-2 * time.Minute)),
		},
		{
			Id: "s-unread", Agent: "claude", Project: "/repo/unread", State: protocol.StateRunning,
			Status:   protocol.AgentDone,
			StatusAt: timestamppb.New(now.Add(-3 * time.Minute)),
		},
	})
	m.Titles.Set(map[string]string{"s-app": longTitle, "s-auth": "Auth session", "s-wait": "Waiting session", "s-unread": "Unread session"})
	m.Unread.Set(map[string]bool{"s-unread": true})

	p := ggui.ProbeBuilder(func() ggui.Widget { return inboxView(m, nil) }, ggui.Sz(820, 760))
	defer p.Close()
	p.Frame()

	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "Allow Bash?"); !ok {
		t.Fatalf("missing approval card:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleText, "codex login"); !ok {
		t.Fatalf("missing auth error command pill:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleButton, "Copy command"); !ok {
		t.Fatalf("missing Copy command button on auth error card:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleTextField, "Quick reply"); !ok {
		t.Fatalf("missing Quick reply input for unattached waiting session:\n%s", sem.String())
	}
	// Long title must stay within card bounds and not push Open session off-screen.
	openBtn, ok := sem.Find(ggui.RoleButton, "Open session")
	if !ok || openBtn.Rect.Origin.X+openBtn.Rect.Size.W > 820 {
		t.Fatalf("Open session button overflowed or missing: ok=%v rect=%v", ok, openBtn.Rect)
	}

	// Filter to Unread: only unread card remains.
	unreadFilter, ok := sem.Find(ggui.RoleButton, "Unread (1)")
	if !ok {
		t.Fatalf("missing Unread filter button:\n%s", sem.String())
	}
	p.Click(unreadFilter.Rect.Center())
	p.Frame()
	sem = p.Semantics()
	if _, ok := sem.Find(ggui.RoleText, "Finished its turn"); !ok {
		t.Fatalf("expected unread card after Unread filter:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleText, "Allow Bash?"); ok {
		t.Fatalf("approval card should be hidden under Unread filter:\n%s", sem.String())
	}
}

func TestInboxDraftsPersistAcrossSessionPolls(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(config.Default(), "")
	now := time.Now()
	baseSessions := func(bump time.Duration) []*protocol.SessionInfo {
		return []*protocol.SessionInfo{
			{
				Id: "s-app", Agent: "claude", Project: "/repo/a", State: protocol.StateRunning,
				Approval: &protocol.Approval{Id: "ap-1", Tool: "Bash", Detail: "rm -rf build"},
				StatusAt: timestamppb.New(now.Add(bump)),
			},
			{
				Id: "s-wait", Agent: "claude", Project: "/repo/b", State: protocol.StateRunning,
				Status:   protocol.AgentWaiting,
				StatusAt: timestamppb.New(now.Add(bump)),
			},
		}
	}
	m.Sessions.Set(baseSessions(0))

	p := ggui.ProbeBuilder(func() ggui.Widget { return inboxView(m, nil) }, ggui.Sz(820, 700))
	defer p.Close()
	p.Frame()

	// Type into Deny-reason and Quick-reply fields.
	m.inboxDenyState("s-app", "ap-1").Set("use git clean instead")
	m.inboxReplyState("s-wait").Set("yes, proceed with option B")
	p.Frame()

	// Simulate several background session poll updates.
	for i := 1; i <= 3; i++ {
		next := baseSessions(time.Duration(i) * time.Second)
		m.noteStatus(next)
		m.Sessions.Set(next)
		p.Frame()
	}

	if got := ggui.Peek(m.inboxDenyState("s-app", "ap-1")); got != "use git clean instead" {
		t.Fatalf("deny draft wiped across polls: got %q", got)
	}
	if got := ggui.Peek(m.inboxReplyState("s-wait")); got != "yes, proceed with option B" {
		t.Fatalf("quick reply draft wiped across polls: got %q", got)
	}
	sem := p.Semantics()
	if f, ok := sem.Find(ggui.RoleTextField, "Why not"); !ok || f.Value != "use git clean instead" {
		t.Fatalf("Why not field value after polls: ok=%v value=%q", ok, f.Value)
	}
	if f, ok := sem.Find(ggui.RoleTextField, "Quick reply"); !ok || f.Value != "yes, proceed with option B" {
		t.Fatalf("Quick reply field value after polls: ok=%v value=%q", ok, f.Value)
	}
}

func TestInboxQuickReplyPreservesComposerAndWorksUnattached(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	fd := startFakeInboxDaemon(t)
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local", Name: "local"}})
	m.link.setDaemon(fd.sock)
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	// Unattached session (m.conns["s-wait"] is nil) with an unsaved composer draft & attachment!
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id: "s-wait", NodeId: "peer-9", Agent: "claude", Project: "/repo/wait",
		State: protocol.StateRunning, Status: protocol.AgentWaiting,
	}})
	m.setComposerDraft("s-wait", "unsaved composer message")
	m.attachTo("s-wait", []string{"/repo/wait/spec.md"})
	m.inboxReplyState("s-wait").Set("quick answer from inbox")

	p := ggui.ProbeBuilder(func() ggui.Widget { return inboxView(m, nil) }, ggui.Sz(820, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	sendBtn, ok := p.Semantics().Find(ggui.RoleButton, "Send")
	if !ok {
		t.Fatalf("missing Send button for unattached waiting session:\n%s", p.Semantics().String())
	}
	p.Click(sendBtn.Rect.Center())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.Frame()
		if len(fd.gotSends()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sends := fd.gotSends()
	if len(sends) != 1 {
		t.Fatalf("got %d Send RPCs, want 1", len(sends))
	}
	got := sends[0]
	if got.GetNodeId() != "peer-9" || got.GetSessionId() != "s-wait" || got.GetText() != "quick answer from inbox" || !bytes.Equal(got.GetKeys(), []byte{'\r'}) {
		t.Fatalf("Send RPC = %+v, want node=peer-9 session=s-wait text='quick answer from inbox' keys=\\r", got)
	}
	// Composer draft and attachments must remain completely untouched.
	if draft := m.composerDraft("s-wait"); draft != "unsaved composer message" {
		t.Fatalf("composer draft corrupted: got %q", draft)
	}
	if attached := ggui.Peek(m.chatState("s-wait").attached); !slices.Equal(attached, []string{"/repo/wait/spec.md"}) {
		t.Fatalf("composer attachments corrupted: got %v", attached)
	}
	// Quick reply input should be cleared after sending.
	if r := ggui.Peek(m.inboxReplyState("s-wait")); r != "" {
		t.Fatalf("quick reply draft not cleared: got %q", r)
	}
}

func TestInboxScreenDialogRendersAndAnswers(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	fd := startFakeInboxDaemon(t)
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local", Name: "local"}})
	m.link.setDaemon(fd.sock)
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	m.Sessions.Set([]*protocol.SessionInfo{{
		Id: "s-dlg", NodeId: "local", Agent: "claude", Project: "/repo/trust",
		State:  protocol.StateRunning,
		Status: protocol.AgentWaiting,
		Dialog: &protocol.ScreenDialog{
			Id:       "dlg-42",
			Text:     "Do you trust the files in this folder?",
			Options:  []string{"Yes, proceed", "No, exit"},
			Selected: 0,
		},
	}})

	p := ggui.ProbeBuilder(func() ggui.Widget { return inboxView(m, nil) }, ggui.Sz(820, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleTextField, "Quick reply"); ok {
		t.Fatalf("ScreenDialog item should not render free-text Quick reply:\n%s", sem.String())
	}
	if !strings.Contains(sem.String(), "Do you trust the files in this folder?") {
		t.Fatalf("ScreenDialog text missing:\n%s", sem.String())
	}
	optBtn, ok := sem.Find(ggui.RoleButton, "No, exit")
	if !ok {
		t.Fatalf("ScreenDialog option button missing:\n%s", sem.String())
	}
	p.Click(optBtn.Rect.Center())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.Frame()
		if len(fd.gotDialogs()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dialogs := fd.gotDialogs()
	if len(dialogs) != 1 {
		t.Fatalf("got %d AnswerDialog RPCs, want 1", len(dialogs))
	}
	if dialogs[0].GetSessionId() != "s-dlg" || dialogs[0].GetDialogId() != "dlg-42" || dialogs[0].GetOption() != 1 {
		t.Fatalf("AnswerDialog RPC = %+v, want session=s-dlg dialog=dlg-42 option=1", dialogs[0])
	}
}

func TestInboxBatchApproveIsDestructiveAndSkipsStaleApprovals(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	fd := startFakeInboxDaemon(t)
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local", Name: "local"}})
	m.link.setDaemon(fd.sock)
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	s1 := &protocol.SessionInfo{Id: "s1", NodeId: "local", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a1", Tool: "Bash", Detail: "go test"}}
	s2 := &protocol.SessionInfo{Id: "s2", NodeId: "local", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a2", Tool: "Edit", Detail: "main.go"}}
	s3 := &protocol.SessionInfo{Id: "s3", NodeId: "local", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a3", Tool: "Write", Detail: "out.txt"}}
	m.Sessions.Set([]*protocol.SessionInfo{s1, s2, s3})

	p := ggui.ProbeBuilder(func() ggui.Widget { return inboxView(m, nil) }, ggui.Sz(820, 600))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	m.askBatchApproveAll(collectInboxItems(ggui.Peek(m.Sessions), nil), nil)
	c := ggui.Peek(m.Confirmation)
	if c.Title == "" {
		t.Fatal("expected confirmation to be queued")
	}
	if c.Safe {
		t.Fatal("batch approval confirmation must have Safe=false")
	}

	// Before the user confirms: s1 is already answered (nil), s2 changed to a new approval ID,
	// and only s3 still matches its snapshot (a3).
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s1", NodeId: "local", State: protocol.StateRunning},
		{Id: "s2", NodeId: "local", State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a2-new", Tool: "Bash", Detail: "rm -rf /"}},
		s3,
	})

	c.Run()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.Frame()
		if len(fd.gotApprovals()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := fd.gotApprovals()
	if len(got) != 1 || got[0].GetSessionId() != "s3" || got[0].GetApprovalId() != "a3" {
		t.Fatalf("AnswerApproval RPCs = %+v, want only s3/a3 (stale s1 and changed s2 skipped)", got)
	}
}

func TestInViewFalseWhenCoveredByInbox(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Prefs.Update(func(p prefs) prefs { p.Notify = false; return p })

	at := time.Now()
	s := &protocol.SessionInfo{Id: "s1", State: protocol.StateRunning, Status: protocol.AgentWorking, StatusAt: timestamppb.New(at)}
	m.Sessions.Set([]*protocol.SessionInfo{s})
	m.editLayout(func(root *layoutNode) int {
		g := root.groups()[0]
		g.insert("s1", 0)
		g.Active = "s1"
		return g.ID
	})

	if !m.inView("s1") {
		t.Fatal("s1 should be inView when workspace is uncovered")
	}

	m.SidebarPage.Set(sidebarPageInbox)
	if m.inView("s1") {
		t.Fatal("s1 must NOT be inView while covered by builtin:inbox")
	}

	// Session finishing while covered by Needs You must become unread.
	done := proto.CloneOf(s)
	done.Status, done.StatusAt = protocol.AgentDone, timestamppb.New(at.Add(time.Second))
	m.noteStatus([]*protocol.SessionInfo{done})
	m.Sessions.Set([]*protocol.SessionInfo{done})
	if !ggui.Peek(m.Unread)["s1"] {
		t.Fatal("covered active session was auto-marked seen instead of unread while on builtin:inbox")
	}

	// Returning to workspace restores inView.
	m.SidebarPage.Set("")
	if !m.inView("s1") {
		t.Fatal("s1 should be inView again after clearing SidebarPage")
	}
}

func TestInboxInDetachedSpaceWindowNavigationAndConfirm(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.Spaces.Set(spaceList{
		Spaces: []space{
			{ID: "sp-work", Name: "Work", Color: "blue", Owners: []string{"github.com/acme"}},
			{ID: "sp-oss", Name: "OSS", Color: "teal", Owners: []string{"github.com/oss"}},
		},
	})
	m.Sessions.Set([]*protocol.SessionInfo{
		{
			Id: "s-work", NodeId: "n1", Agent: "claude", Project: "/work/app", Owner: "github.com/acme",
			State: protocol.StateRunning, Approval: &protocol.Approval{Id: "a-work", Tool: "Bash", Detail: "make build"},
		},
		{
			Id: "s-oss", NodeId: "n1", Agent: "codex", Project: "/oss/mako", Owner: "github.com/oss",
			State: protocol.StateRunning, Status: protocol.AgentDone,
		},
	})
	m.Titles.Set(map[string]string{"s-work": "Work agent", "s-oss": "OSS agent"})
	m.Unread.Set(map[string]bool{"s-oss": true})
	m.Prefs.Update(func(p prefs) prefs { p.ShownSpace = "sp-work"; return p })
	m.ws.layoutKey = layoutKey("sp-work", "")
	m.showTab("s-work")
	m.Page.Set("settings") // main window is in Settings

	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}

	spaceProbe := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowView(m, sw) }, ggui.Sz(1040, 720))
	defer spaceProbe.Close()
	mainProbe := ggui.ProbeBuilder(func() ggui.Widget { return window(m) }, ggui.Sz(1040, 720))
	defer mainProbe.Close()
	spaceProbe.Frame()
	mainProbe.Frame()

	// 1. Clicking Needs you in Space window opens inbox in sw without mutating m.Page.
	needsBtn, ok := spaceProbe.Semantics().Find(ggui.RoleButton, "Needs you")
	if !ok {
		t.Fatalf("missing Needs you sidebar row in Space window:\n%s", spaceProbe.Semantics().String())
	}
	spaceProbe.Click(needsBtn.Rect.Center())
	spaceProbe.Frame()
	if got := ggui.Peek(sw.sidebarPage); got != sidebarPageInbox {
		t.Fatalf("sw.sidebarPage = %q, want %q", got, sidebarPageInbox)
	}
	if got := ggui.Peek(m.Page); got != "settings" {
		t.Fatalf("main window m.Page mutated to %q from Space window click, want settings", got)
	}

	// 2. Allow all approvals in Space window opens confirm dialog ONLY in Space window, not main window.
	allowAllBtn, ok := spaceProbe.Semantics().Find(ggui.RoleButton, "Allow all approvals (1)")
	if !ok {
		t.Fatalf("missing Allow all approvals button in Space window inbox:\n%s", spaceProbe.Semantics().String())
	}
	spaceProbe.Click(allowAllBtn.Rect.Center())
	spaceProbe.Frame()
	mainProbe.Frame()

	if c := ggui.Peek(m.Confirmation); c.Owner != "sp-oss" || c.Safe {
		t.Fatalf("Confirmation = %+v, want Owner=sp-oss Safe=false", c)
	}
	if _, ok := spaceProbe.Semantics().Find(ggui.RoleButton, "Allow all"); !ok {
		t.Fatalf("Space window did not render confirmation dialog:\n%s", spaceProbe.Semantics().String())
	}
	if _, ok := mainProbe.Semantics().Find(ggui.RoleButton, "Allow all"); ok {
		t.Fatalf("main window duplicated Space window confirmation dialog:\n%s", mainProbe.Semantics().String())
	}
	// Dismiss confirmation in Space window.
	if cancelBtn, ok := spaceProbe.Semantics().Find(ggui.RoleButton, "Cancel"); ok {
		spaceProbe.Click(cancelBtn.Rect.Center())
		spaceProbe.Frame()
	}

	// 3. Opening an in-space session from Space window inbox clears sw.sidebarPage and activates it in sw.
	m.openInboxSession(sw, "s-oss", true)
	spaceProbe.Frame()
	if got := ggui.Peek(sw.sidebarPage); got != "" {
		t.Fatalf("sw.sidebarPage = %q after opening in-space session, want empty", got)
	}
	if got := m.spaceWindowActiveTab(sw); got != "s-oss" {
		t.Fatalf("spaceWindowActiveTab = %q, want s-oss", got)
	}
	if !m.chatOn("s-oss") {
		t.Fatal("Review in Chat did not enable chat mode for s-oss")
	}
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("main window selection disturbed: got %q, want s-work", got)
	}

	// 4. Opening a session from ANOTHER space clears sw.sidebarPage (never stuck on builtin:inbox)
	// and selects that session in the main window.
	sw.sidebarPage.Set(sidebarPageInbox)
	m.openInboxSession(sw, "s-work", false)
	if got := ggui.Peek(sw.sidebarPage); got != "" {
		t.Fatalf("sw.sidebarPage stuck at %q after opening out-of-space session", got)
	}
	if got := ggui.Peek(m.Selected); got != "s-work" {
		t.Fatalf("main window selection = %q, want s-work", got)
	}
}
