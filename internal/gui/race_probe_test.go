package gui

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// fakeRaceDaemon answers what a race's winner and its cleanup ask: the
// worktrees' changes, the git runs, which it records, and the losers'
// kills; and the sessions' usage, as their chats read it. Each request is
// answered on a goroutine of its own, so requests sent at once overlap
// here as they would on a daemon.
type fakeRaceDaemon struct {
	sock string

	mu       sync.Mutex
	usage    map[string]*protocol.Usage      // by session
	status   map[string]*protocol.GitChanges // by path
	runs     []*protocol.GitRun
	kills    []string
	removing int // remove_worktree runs under way
	overlap  bool
}

func startFakeRaceDaemon(t *testing.T) *fakeRaceDaemon {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fake daemon test runs on unix")
	}
	dir, err := os.MkdirTemp("", "mk-race")
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
	fd := &fakeRaceDaemon{sock: sock, status: map[string]*protocol.GitChanges{}, usage: map[string]*protocol.Usage{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fd.serve(conn)
		}
	}()
	return fd
}

func (fd *fakeRaceDaemon) serve(c net.Conn) {
	defer c.Close()
	var write sync.Mutex
	reply := func(typ protocol.Type, id string, msg proto.Message) {
		if env, err := protocol.Encode(typ, id, msg); err == nil {
			write.Lock()
			_ = protocol.WriteFrame(c, env, false)
			write.Unlock()
		}
	}
	for {
		env, err := protocol.ReadFrame(c)
		if err != nil {
			return
		}
		go func() {
			switch env.Type() {
			case protocol.TypeHello:
				reply(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{Node: &protocol.Node{Id: "local", Name: "local", Revision: protocol.Revision}})
			case protocol.TypeGitStatus:
				fd.mu.Lock()
				ch := proto.CloneOf(fd.status[env.GetGitStatus().GetPath()])
				fd.mu.Unlock()
				reply(protocol.TypeGitChanges, env.GetId(), cmpOrChanges(ch))
			case protocol.TypeGitRun:
				req := proto.CloneOf(env.GetGitRun())
				fd.mu.Lock()
				fd.runs = append(fd.runs, req)
				remove := req.GetOp() == protocol.GitOpRemoveWT
				if remove {
					fd.removing++
					fd.overlap = fd.overlap || fd.removing > 1
				}
				fd.mu.Unlock()
				if remove {
					time.Sleep(40 * time.Millisecond)
					fd.mu.Lock()
					fd.removing--
					fd.mu.Unlock()
				}
				reply(protocol.TypeGitResult, env.GetId(), &protocol.GitResult{})
			case protocol.TypeReadChat:
				fd.mu.Lock()
				u := proto.CloneOf(fd.usage[env.GetReadChat().GetSessionId()])
				fd.mu.Unlock()
				reply(protocol.TypeChat, env.GetId(), &protocol.Chat{Supported: true, Usage: u})
			case protocol.TypeCloseSess:
				fd.mu.Lock()
				fd.kills = append(fd.kills, env.GetCloseSession().GetSessionId())
				fd.mu.Unlock()
				reply(protocol.TypeExited, env.GetId(), &protocol.Exited{SessionId: env.GetCloseSession().GetSessionId()})
			}
		}()
	}
}

func cmpOrChanges(ch *protocol.GitChanges) *protocol.GitChanges {
	if ch == nil {
		return &protocol.GitChanges{}
	}
	return ch
}

func (fd *fakeRaceDaemon) setStatus(path string, files ...*protocol.GitFile) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.status[path] = &protocol.GitChanges{Root: path, Files: files}
}

// ops is the git runs so far, as op@root.
func (fd *fakeRaceDaemon) ops() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	out := make([]string, len(fd.runs))
	for i, r := range fd.runs {
		at := r.GetRoot()
		if r.GetOp() == protocol.GitOpRemoveWT {
			at = r.GetWorktree().GetPath()
		}
		out[i] = r.GetOp() + "@" + at
	}
	return out
}

// raceRig is a model with a three-agent race whose contestants are done,
// linked to a fake daemon: s1 is to win, with one file uncommitted.
func raceRig(t *testing.T) (*model, *fakeRaceDaemon) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)
	fd := startFakeRaceDaemon(t)
	m := newModel(&config.Config{NodeID: "local"}, "")
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "local", Name: "local"}})
	m.link.setDaemon(fd.sock)
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	var sessions []*protocol.SessionInfo
	var entries []raceEntry
	for i, agent := range []string{"claude", "codex", "agy"} {
		id := "s" + string(rune('1'+i))
		wt := &protocol.Worktree{Repo: "/repo", Path: "/repo-" + id, Branch: "race/" + agent + "-1", Base: "main"}
		sessions = append(sessions, &protocol.SessionInfo{
			Id: id, NodeId: "local", Agent: agent, Project: wt.GetPath(), Branch: wt.GetBranch(), Worktree: wt,
			State: protocol.StateRunning, Status: protocol.AgentIdle,
		})
		entries = append(entries, raceEntry{Target: agent, SessionID: id, NodeID: "local", Branch: wt.GetBranch(), Worktree: wt})
		fd.setStatus(wt.GetPath())
	}
	fd.setStatus("/repo-s1", &protocol.GitFile{Path: "a.go", Status: protocol.GitModified, Added: 3, Removed: 1})
	m.Sessions.Set(sessions)
	m.Races.Groups.Set([]raceGroup{{ID: "race-1", Prompt: "Fix the login bug", Project: "/repo", NodeID: "local", Entries: entries}})
	return m, fd
}

// pump frames p, and any other probes, until cond holds or the time is up.
func pump(t *testing.T, what string, cond func() bool, ps ...*ggui.Probe) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range ps {
			p.Frame()
		}
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func countButtons(p *ggui.Probe, label string) int {
	n := 0
	for _, f := range p.FindAll(ggui.RoleButton) {
		if f.Label == label {
			n++
		}
	}
	return n
}

// TestRaceWinnerConfirmSitsAboveTheRaceDialog picks a winner in the main
// window and clicks Merge winner in the confirmation the race dialog
// stays open under: the click reaches it, the winner is committed and
// merged, the losers' worktrees are deleted one after another, and their
// cards then say so, with no session left to open.
func TestRaceWinnerConfirmSitsAboveTheRaceDialog(t *testing.T) {
	m, fd := raceRig(t)
	p := ggui.ProbeBuilder(func() ggui.Widget { return window(m) }, ggui.Sz(1280, 800))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	m.openRaceDialog()
	pump(t, "the race's cards", func() bool { return countButtons(p, "Pick winner & merge") == 3 }, p)
	pump(t, "the winner's diff", func() bool { _, ok := p.FindRole(ggui.RoleButton, "a.go"); return ok }, p)

	pick := p.FindAll(ggui.RoleButton)
	var first ggui.Found
	for _, f := range pick {
		if f.Label == "Pick winner & merge" {
			first = f
			break
		}
	}
	p.Click(first.Rect.Center())
	var merge ggui.Found
	pump(t, "the winner's confirmation", func() bool {
		var ok bool
		merge, ok = p.FindRole(ggui.RoleButton, "Merge winner")
		return ok
	}, p)
	if !ggui.Peek(m.Races.Open) {
		t.Fatal("the race dialog closed under its confirmation")
	}
	p.Click(merge.Rect.Center())
	pump(t, "the merge and the losers' removal", func() bool { return len(fd.ops()) == 4 && ggui.Peek(m.Races.Busy) == "" }, p)

	want := []string{"commit@/repo-s1", "merge@/repo", "remove_worktree@/repo-s2", "remove_worktree@/repo-s3"}
	if got := fd.ops(); !slices.Equal(got, want) {
		t.Fatalf("git runs = %v, want %v", got, want)
	}
	fd.mu.Lock()
	overlap, kills, runs := fd.overlap, slices.Clone(fd.kills), slices.Clone(fd.runs)
	fd.mu.Unlock()
	if overlap {
		t.Fatal("two losers' worktrees were removed at once")
	}
	for _, r := range runs[2:] {
		if !r.GetForce() {
			t.Fatalf("loser removal %v not forced", r)
		}
	}
	slices.Sort(kills)
	if !slices.Equal(kills, []string{"s2", "s3"}) {
		t.Fatalf("killed %v, want the losers", kills)
	}

	g, _ := raceGroupByID(ggui.Peek(m.Races.Groups), "race-1")
	if g.WinnerID != "s1" || !g.Entries[1].Removed || !g.Entries[2].Removed || g.Entries[0].Removed {
		t.Fatalf("race after merge = %+v", g)
	}
	pump(t, "the losers' cards", func() bool { return countButtons(p, "Open session") == 1 }, p)
	if !hasProbeText(p, "Worktree removed") || !hasProbeText(p, "Its session stopped, and its worktree and branch were deleted.") {
		t.Fatalf("loser cards do not say they were removed:\n%s", p.Semantics().String())
	}
	if hasProbeText(p, "Starting") {
		t.Fatalf("a removed loser is shown as starting:\n%s", p.Semantics().String())
	}
}

// TestRaceWinnerChangedSinceAskedIsNotMerged answers the confirmation
// after the winner's worktree gained a file: nothing is committed or
// merged.
func TestRaceWinnerChangedSinceAskedIsNotMerged(t *testing.T) {
	m, fd := raceRig(t)
	p := ggui.ProbeBuilder(func() ggui.Widget { return window(m) }, ggui.Sz(1280, 800))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	g, _ := raceGroupByID(ggui.Peek(m.Races.Groups), "race-1")
	m.askPickRaceWinner(g.ID, "s1")
	pump(t, "the winner's confirmation", func() bool { return ggui.Peek(m.Confirmation).Title != "" }, p)
	fd.setStatus("/repo-s1",
		&protocol.GitFile{Path: "a.go", Status: protocol.GitModified, Added: 3, Removed: 1},
		&protocol.GitFile{Path: "secret.env", Status: protocol.GitAdded})
	merge, ok := p.FindRole(ggui.RoleButton, "Merge winner")
	if !ok {
		t.Fatalf("no Merge winner button:\n%s", p.Semantics().String())
	}
	p.Click(merge.Rect.Center())
	pump(t, "the check to end", func() bool { return ggui.Peek(m.Confirmation).Title == "" && ggui.Peek(m.Races.Busy) == "" }, p)
	time.Sleep(50 * time.Millisecond)
	p.Frame()
	if ops := fd.ops(); len(ops) != 0 {
		t.Fatalf("ran %v on a winner changed since the user was asked", ops)
	}
	if g, _ := raceGroupByID(ggui.Peek(m.Races.Groups), "race-1"); g.WinnerID != "" {
		t.Fatal("a winner was recorded")
	}
}

// TestRaceDialogOpensInTheSpaceWindowItIsOpenedFrom opens the race dialog
// from a Space window's header: it, and the winner's confirmation, show
// there and not in the main window.
func TestRaceDialogOpensInTheSpaceWindowItIsOpenedFrom(t *testing.T) {
	m, _ := raceRig(t)
	m.Spaces.Set(spaceList{Spaces: []space{{ID: "sp-oss", Name: "OSS", Color: "teal"}}})
	sw := m.openSpaceWindow("sp-oss")
	if sw == nil {
		t.Fatal("openSpaceWindow returned nil")
	}
	sp := ggui.ProbeBuilder(func() ggui.Widget { return spaceWindowView(m, sw) }, ggui.Sz(1280, 800))
	defer sp.Close()
	mp := ggui.ProbeBuilder(func() ggui.Widget { return window(m) }, ggui.Sz(1280, 800))
	defer mp.Close()
	m.post = sp.Post
	sp.Frame()
	mp.Frame()

	btn, ok := sp.FindRole(ggui.RoleButton, "Compare agent races")
	if !ok {
		t.Fatalf("no race button in the Space window:\n%s", sp.Semantics().String())
	}
	sp.Click(btn.Rect.Center())
	pump(t, "the race dialog in the Space window", func() bool { return countButtons(sp, "Pick winner & merge") == 3 }, sp, mp)
	if n := countButtons(mp, "Pick winner & merge"); n != 0 {
		t.Fatalf("the main window shows the Space window's race dialog too (%d cards)", n)
	}

	m.askPickRaceWinner("race-1", "s1")
	pump(t, "the confirmation in the Space window", func() bool { _, ok := sp.FindRole(ggui.RoleButton, "Merge winner"); return ok }, sp, mp)
	if c := ggui.Peek(m.Confirmation); c.Owner != "sp-oss" {
		t.Fatalf("confirmation owner = %q, want sp-oss", c.Owner)
	}
	if _, ok := mp.FindRole(ggui.RoleButton, "Merge winner"); ok {
		t.Fatal("the main window shows the Space window's confirmation")
	}
	cancel, _ := sp.FindRole(ggui.RoleButton, "Cancel")
	sp.Click(cancel.Rect.Center())
	sp.Frame()
	if !ggui.Peek(m.Races.Open) {
		t.Fatal("cancelling the confirmation closed the race dialog")
	}
}

// hasProbeText reports whether p shows text, as a text or anything else.
func hasProbeText(p *ggui.Probe, text string) bool {
	for _, n := range p.Semantics().All() {
		if n.Name == text {
			return true
		}
	}
	return false
}
