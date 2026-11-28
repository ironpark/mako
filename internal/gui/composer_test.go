package gui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/widget"
	"github.com/ironpark/gostty"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/mdview"
	"github.com/ironpark/mako/internal/protocol"
)

// Enter sends what is written at once; Shift+Enter breaks the line, and
// the lines reach the agent as one paste, then Enter.
func TestComposerSendsSeveralLinesOnEnter(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	m.conns["s"] = conn
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	link.output(t, "\x1b[?2004h") // the agent takes bracketed pastes
	waitFor(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.vt.Terminal().ModeEnabled(gostty.ModeBracketedPaste)
	})

	draft := m.chatState("s").draft
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", m.chatState("s")) }, ggui.Sz(600, 300))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatalf("no composer field:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	p.Text("one")
	p.Type(ggui.Mods{Shift: true}, ggui.KeyEnter)
	p.Text("two")
	if got := ggui.Peek(draft); got != "one\ntwo" {
		t.Fatalf("draft %q, want two lines", got)
	}
	if got := link.typed(); got != "" {
		t.Fatalf("Shift+Enter sent %q", got)
	}
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	// With no node to send it, the message goes through the terminal.
	if got, want := sentThrough(p, link), "\x1b[200~one\ntwo\x1b[201~\r"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	if ggui.Peek(draft) != "" {
		t.Fatal("the draft stayed after sending")
	}

	// ↑ and ↓ over the empty box walk back through what was sent and
	// forward to the empty box again; over text being written, or a recalled
	// message edited, they are the editor's.
	p.Text("three")
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	_ = sentThrough(p, link)
	for _, step := range []struct {
		key  ggui.KeyboardKey
		want string
	}{
		{ggui.KeyArrowUp, "three"},
		{ggui.KeyArrowUp, "one\ntwo"},
		{ggui.KeyArrowUp, "one\ntwo"},
		{ggui.KeyArrowDown, "three"},
		{ggui.KeyArrowDown, ""},
		{ggui.KeyArrowDown, ""},
		{ggui.KeyArrowUp, "three"},
	} {
		p.Type(ggui.Mods{}, step.key)
		if got := ggui.Peek(draft); got != step.want {
			t.Fatalf("after %v the draft is %q, want %q", step.key, got, step.want)
		}
	}
	p.Text("!") // the caret came back at the end
	if got := ggui.Peek(draft); got != "three!" {
		t.Fatalf("typed after a recall: %q", got)
	}
	p.Type(ggui.Mods{}, ggui.KeyArrowUp)
	if got := ggui.Peek(draft); got != "three!" {
		t.Fatalf("↑ over an edited message recalled %q", got)
	}
	if got := link.typed(); got != "" {
		t.Fatalf("browsing the history sent %q", got)
	}

	// A draft that begins with a slash lists the agent's commands it
	// matches: ↑ and ↓ choose, Enter takes one, sending it when it takes no
	// arguments, Escape closes the list.
	menu := m.chatState("s").menu
	menu.read = time.Now() // as if read: the test has no daemon
	menu.commands.Set([]*protocol.Command{
		{Name: "compact", Args: "[instructions]"}, {Name: "context"}, {Name: "frontend:config"}, {Name: "clear"},
	})
	m.setComposerDraft("s", "")
	p.Text("/co")
	p.Frame()
	if names := matchNames(m.menuItems("s", m.chatState("s"))); names != "compact context frontend:config" {
		t.Fatalf("/co lists %q", names)
	}
	if _, ok := p.Semantics().Find(ggui.RoleText, "/context"); !ok {
		t.Fatalf("no /context in the list:\n%s", p.Semantics().String())
	}
	p.Type(ggui.Mods{}, ggui.KeyArrowDown)
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got, want := sentThrough(p, link), "\x1b[200~/context\x1b[201~\r"; got != want {
		t.Fatalf("taking /context sent %q, want %q", got, want)
	}
	p.Text("/com")
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got := ggui.Peek(draft); got != "/compact " {
		t.Fatalf("taking /compact left %q, want its arguments to follow", got)
	}
	if got := link.typed(); got != "" {
		t.Fatalf("taking a command with arguments sent %q", got)
	}
	m.setComposerDraft("s", "/cl")
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if len(m.menuItems("s", m.chatState("s"))) != 0 {
		t.Fatal("Escape left the list open")
	}
}

func matchNames(items []menuItem) string {
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = strings.TrimPrefix(it.name, "/")
	}
	return strings.Join(names, " ")
}

// A call turns a spinner while it is at work: unanswered while its agent
// works, or with a subagent it started still running; one left open by an
// agent that has stopped does not.
func TestToolsAtWorkAreTold(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	session := func(state, status string) {
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: state, Status: status}})
	}
	open := &protocol.ChatMessage{Role: "tool", Id: "t", Tool: "Bash", Pending: true}
	done := &protocol.ChatMessage{Role: "tool", Id: "d", Tool: "Agent"}
	running := &protocol.SubAgent{CallId: "d", Running: true}
	for _, c := range []struct {
		state, status string
		msg           *protocol.ChatMessage
		agent         *protocol.SubAgent
		want          bool
	}{
		{protocol.StateRunning, string(protocol.AgentWorking), open, nil, true},
		{protocol.StateRunning, string(protocol.AgentWaiting), open, nil, true},
		{protocol.StateRunning, string(protocol.AgentIdle), open, nil, false},
		{protocol.StateExited, string(protocol.AgentWorking), open, nil, false},
		{protocol.StateRunning, string(protocol.AgentWorking), done, nil, false},
		{protocol.StateRunning, string(protocol.AgentIdle), done, running, true},
		{protocol.StateExited, string(protocol.AgentIdle), done, running, false},
	} {
		session(c.state, c.status)
		if got := toolLive(m, "s", c.msg, c.agent); got != c.want {
			t.Errorf("%s/%s, pending %v, agent running %v: live %v, want %v", c.state, c.status, c.msg.GetPending(), c.agent.GetRunning(), got, c.want)
		}
	}
}

// The subagents at work show over the composer, each with what it was
// asked and how far it has got, and go once they finish.
func TestRunningSubagentsShowOverTheComposer(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", State: protocol.StateRunning}})
	state := m.chatState("s")
	agent := &protocol.SubAgent{Id: "a", CallId: "c", Kind: "Explore", Description: "Find the tests", Running: true, ToolCalls: 3, Tool: "Bash", Detail: "go test ./..."}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Agents: []*protocol.SubAgent{agent}}})
	p := ggui.ProbeBuilder(func() ggui.Widget { return runningAgents(m, "s", state) }, ggui.Sz(700, 200))
	defer p.Close()
	p.Frame()
	for _, want := range []string{"Find the tests", "Explore · " + Tf("%d tool calls", 3), "Bash  go test ./..."} {
		if _, ok := p.Semantics().Find(ggui.RoleText, want); !ok {
			t.Errorf("no %q:\n%s", want, p.Semantics().String())
		}
	}
	finished := &protocol.SubAgent{Id: "a", CallId: "c", Description: "Find the tests", ToolCalls: 4}
	state.result.Set(chatResult{Loaded: true, Chat: &protocol.Chat{Supported: true, Agents: []*protocol.SubAgent{finished}}})
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "Find the tests"); ok {
		t.Error("a finished subagent still shows as running")
	}
}

// An image pasted into the composer goes with the message as a file, not
// in its text; text pastes as text.
func TestComposerPastesAnImageAsAFile(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
	draft, attached := m.chatState("s").draft, m.chatState("s").attached
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", m.chatState("s")) }, ggui.Sz(600, 300))
	defer p.Close()
	m.post = p.Post
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var clip *ggruntime.MemoryClipboard
	p.OnFrame(func() { clip, _ = ggui.CurrentClipboard().(*ggruntime.MemoryClipboard) })
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok || clip == nil {
		t.Fatalf("no composer field or no memory clipboard:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
	paste := ggui.Mods{Meta: runtime.GOOS == "darwin", Ctrl: runtime.GOOS != "darwin"}

	clip.WriteImage(png)
	p.Type(paste, ggui.KeyV)
	probeUntil(t, p, "the image attached", func() bool { return len(ggui.Peek(attached)) > 0 })
	path := ggui.Peek(attached)[0]
	if filepath.Dir(path) != clipboardDir() || filepath.Ext(path) != ".png" || ggui.Peek(draft) != "" {
		t.Fatalf("attached %q, draft %q, want the pasted image attached", path, ggui.Peek(draft))
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(png) {
		t.Fatalf("pasted file holds %q, %v", data, err)
	}

	clip.Write("words")
	p.Type(paste, ggui.KeyV)
	if got := ggui.Peek(draft); got != "words" {
		t.Fatalf("draft %q after pasting text", got)
	}
}

// A paste of several lines, or of a line copied with its break, reaches
// the program: bracketed when it asked for that, plain when not, which is
// asked about first.
func TestATerminalTakesPastesOfSeveralLines(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()

	if conn.runsLines("ls") || !conn.runsLines("ls\n") {
		t.Fatal("asks about the wrong pastes where they run as typed")
	}
	conn.paste("ls\n")
	if got := link.typed(); got != "ls\r" {
		t.Fatalf("a line with its break pasted as %q", got)
	}
	link.output(t, "\x1b[?2004h") // the shell takes bracketed pastes
	waitFor(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.vt.Terminal().ModeEnabled(gostty.ModeBracketedPaste)
	})
	if conn.runsLines("cd /tmp\nls") {
		t.Fatal("asks about a bracketed paste")
	}
	conn.paste("cd /tmp\nls")
	if got, want := link.typed(), "\x1b[200~cd /tmp\nls\x1b[201~"; got != want {
		t.Fatalf("pasted %q, want %q", got, want)
	}
	conn.paste("a\x1b[201~rm -rf ~\n")
	if got := link.typed(); got != "" {
		t.Fatalf("a paste ending the bracket early was sent: %q", got)
	}
}

// An image attached shows over the text as a thumbnail as large as the
// image, its remove button in its corner, and the composer stays as tall
// as the two. The message sent names it after the text, and clears both.
func TestADraftImageIsAThumbnail(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: "n", Agent: "claude", Project: "/tmp"}})
	img := ggfx.NewImage(100, 50)
	shot := m.chatImages.add("n\x00/tmp/shot.png")
	m.chatImages.set("n\x00/tmp/shot.png", shot, mdview.Image{Img: img, Size: ggui.Sz(100, 50)})
	m.setComposerDraft("s", "look")
	m.attachTo("s", []string{"/tmp/shot.png"})
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return widget.Column(widget.Expanded(widget.Text("FEED")), composer(m, "s", m.chatState("s"))).Stretch()
	}, ggui.Sz(900, 700))
	defer p.Close()
	p.Frame()
	feed, ok := p.Semantics().Find(ggui.RoleText, "FEED")
	if !ok || feed.Rect.Size.H < 400 {
		t.Fatalf("the composer took the pane: the feed is %v\n%s", feed.Rect, p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("Remove %s", "shot.png")); !ok {
		t.Fatalf("no remove button:\n%s", p.Semantics().String())
	}
}

func TestAMessageNamesItsFilesAfterItsText(t *testing.T) {
	for _, c := range []struct {
		text     string
		attached []string
		want     string
	}{
		{"look", nil, "look"},
		{"look ", []string{"/w/p/shot.png"}, "look shot.png"},
		{"", []string{"/w/p/a b.txt", "/tmp/c.png"}, "'a b.txt' /tmp/c.png"},
		{"two\nlines\n", []string{"/tmp/c.png"}, "two\nlines\n/tmp/c.png"},
	} {
		if got := composed(c.text, c.attached, "/w/p"); got != c.want {
			t.Errorf("composed(%q, %q) = %q, want %q", c.text, c.attached, got, c.want)
		}
	}
}

// Once a composer's text exceeds composerMaxHeight, the input caps its
// viewport height, follows typing and caret movement, and scrolls with the
// mouse wheel — in both the Chat composer and the New Session prompt box.
func TestComposerScrollsPastMaxHeight(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())

	verifyScroll := func(t *testing.T, build func() ggui.Widget, fieldName string, binding ggui.Binding[string]) {
		t.Helper()
		p := ggui.ProbeBuilder(build, ggui.Sz(640, 480))
		defer p.Close()
		p.Frame()

		field, ok := p.Semantics().Find(ggui.RoleTextField, fieldName)
		if !ok {
			t.Fatalf("no field %q:\n%s", fieldName, p.Semantics().String())
		}
		shortH := field.Rect.Size.H
		if shortH >= composerMaxHeight {
			t.Fatalf("empty field height %v, want < %v", shortH, composerMaxHeight)
		}

		// Click to focus and type 20 lines with Shift+Enter.
		p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))
		const totalLines = 20
		for i := 1; i <= totalLines; i++ {
			if i > 1 {
				p.Type(ggui.Mods{Shift: true}, ggui.KeyEnter)
			}
			p.Text(strings.Repeat("a", 4) + string(rune('A'+i-1)))
		}
		p.Frame()

		field, ok = p.Semantics().Find(ggui.RoleTextField, fieldName)
		if !ok {
			t.Fatalf("no field %q after typing", fieldName)
		}
		if field.Rect.Size.H > composerMaxHeight+1 {
			t.Fatalf("tall field height %v exceeded composerMaxHeight %v", field.Rect.Size.H, composerMaxHeight)
		}
		if field.Rect.Size.H <= shortH+40 {
			t.Fatalf("tall field height %v did not grow from %v", field.Rect.Size.H, shortH)
		}

		// Caret followed typing to the bottom, so clicking near the top edge
		// of the viewport lands below line 1 (line 1 scrolled above view).
		topClick := field.Rect.Origin.Add(ggui.Pt(4, 12))
		p.Click(topClick)
		p.Text("!")
		lines := strings.Split(ggui.Peek(binding), "\n")
		if len(lines) != totalLines {
			t.Fatalf("got %d lines, want %d", len(lines), totalLines)
		}
		if strings.Contains(lines[0], "!") {
			t.Fatalf("line 1 got click at top without scrolling up (viewport did not follow typing to bottom): line 1 = %q", lines[0])
		}
		p.Type(ggui.Mods{}, ggui.KeyBackspace) // remove "!"

		// Mouse wheel scrolling up brings line 1 back to the top of the viewport.
		center := field.Rect.Center()
		p.Scroll(center, ggui.Pt(0, 40))
		p.Frame()
		p.Click(topClick)
		p.Text("*")
		lines = strings.Split(ggui.Peek(binding), "\n")
		if !strings.Contains(lines[0], "*") {
			t.Fatalf("after scrolling up, click at top did not hit line 1: line 1 = %q, all = %q", lines[0], ggui.Peek(binding))
		}
		p.Type(ggui.Mods{}, ggui.KeyBackspace) // remove "*"

		// Moving the caret down with ArrowDown scrolls the viewport back down
		// to keep the bottom line in view.
		for range totalLines {
			p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		}
		p.Frame()
		p.Click(topClick)
		p.Text("#")
		lines = strings.Split(ggui.Peek(binding), "\n")
		if strings.Contains(lines[0], "#") {
			t.Fatalf("after ArrowDown to bottom, line 1 was still at the top: line 1 = %q", lines[0])
		}
		p.Type(ggui.Mods{}, ggui.KeyBackspace) // remove "#"

		// Moving the caret back up to line 1 with ArrowUp scrolls the viewport
		// back up to the top.
		for range totalLines {
			p.Type(ggui.Mods{}, ggui.KeyArrowUp)
		}
		p.Frame()
		p.Click(topClick)
		p.Text("@")
		lines = strings.Split(ggui.Peek(binding), "\n")
		if !strings.Contains(lines[0], "@") {
			t.Fatalf("after ArrowUp to top, click at top did not hit line 1: line 1 = %q", lines[0])
		}
	}

	t.Run("chat composer", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
		st := m.chatState("s")
		verifyScroll(t, func() ggui.Widget { return composer(m, "s", st) }, T("Message"), st.draft)
	})

	t.Run("launch composer", func(t *testing.T) {
		m := newModel(config.Default(), "")
		id, l := m.newLaunch(newSession{Agent: "claude", Project: "/tmp"})
		prompt := l.Form.Field(func(f *newSession) *string { return &f.Prompt })
		verifyScroll(t, func() ggui.Widget { return launchBox(m, id, l) }, T("First prompt"), prompt)
	})
}

func TestComposerCmdDeleteLine(t *testing.T) {
	focusField := func(t *testing.T, p *ggui.Probe, name string) {
		t.Helper()
		field, ok := p.Semantics().Find(ggui.RoleTextField, name)
		if !ok {
			t.Fatalf("no field %q", name)
		}
		p.Click(field.Rect.Center())
	}

	t.Run("chat composer", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp"}})
		st := m.chatState("s")
		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", st) }, ggui.Sz(600, 400))
		focusField(t, p, T("Message"))

		// Single-line message (including Korean UTF-8): Cmd+Delete clears it completely.
		p.Text("hello 안녕하세요")
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := m.composerDraft("s"); got != "" {
			t.Fatalf("after single-line Cmd+Delete: got %q, want empty", got)
		}

		// Multiline message: Cmd+Delete removes only the current line, stops at line start,
		// and preserves undo.
		p.Text("first line")
		p.Type(ggui.Mods{Shift: true}, ggui.KeyEnter)
		p.Text("second line")
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := m.composerDraft("s"); got != "first line\n" {
			t.Fatalf("after multiline Cmd+Delete: got %q, want %q", got, "first line\n")
		}
		// Pressing Cmd+Delete again at column 0 does not cross the newline.
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := m.composerDraft("s"); got != "first line\n" {
			t.Fatalf("after column-0 Cmd+Delete: got %q, want %q", got, "first line\n")
		}
		// Mid-line Cmd+Delete removes to line start; Cmd+Fn+Delete removes to line end.
		p.Text("alpha beta")
		for range len("beta") {
			p.Type(ggui.Mods{}, ggui.KeyArrowLeft)
		}
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := m.composerDraft("s"); got != "first line\nbeta" {
			t.Fatalf("after mid-line Cmd+Delete: got %q, want %q", got, "first line\nbeta")
		}
		p.Type(ggui.Mods{Meta: true}, ggui.KeyDelete)
		if got := m.composerDraft("s"); got != "first line\n" {
			t.Fatalf("after Cmd+Fn+Delete: got %q, want %q", got, "first line\n")
		}

		// Active selection: Cmd+Delete deletes only the selection.
		p.Text("keep drop")
		for range len("drop") {
			p.Type(ggui.Mods{Shift: true}, ggui.KeyArrowLeft)
		}
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := m.composerDraft("s"); got != "first line\nkeep " {
			t.Fatalf("after selected Cmd+Delete: got %q, want %q", got, "first line\nkeep ")
		}
	})

	t.Run("launch composer", func(t *testing.T) {
		m := newModel(config.Default(), "")
		id, l := m.newLaunch(newSession{Agent: "claude", Project: "/tmp"})
		prompt := l.Form.Field(func(f *newSession) *string { return &f.Prompt })
		p := ggui.ProbeBuilder(func() ggui.Widget { return launchBox(m, id, l) }, ggui.Sz(600, 480))
		focusField(t, p, T("First prompt"))

		p.Text("start a session message")
		p.Type(ggui.Mods{Meta: true}, ggui.KeyBackspace)
		if got := ggui.Peek(prompt); got != "" {
			t.Fatalf("after launch Cmd+Delete: got %q, want empty", got)
		}
	})
}

// sentThrough is what reached the terminal of a message sent, up to the
// Enter that ends it; it waits through frames, in which a send that found
// no node goes through the terminal.
func sentThrough(p *ggui.Probe, link *fakeLink) string {
	var got strings.Builder
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.HasSuffix(got.String(), "\r"); {
		p.Frame()
		got.WriteString(link.typed())
		time.Sleep(5 * time.Millisecond)
	}
	return got.String()
}
