// Package session runs a terminal agent on a PTY and keeps its screen state.
//
// Every session owns a gostty terminal that consumes the same bytes the PTY
// produces, so a front-end attaching late can be handed the current screen
// instead of an empty window.
package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/ironpark/gostty"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/activity"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
)

// Spec describes a session to start.
type Spec struct {
	ID      string
	NodeID  string
	Agent   string
	Profile string
	Project string
	Command []string
	Env     map[string]string
	Cols    uint16
	Rows    uint16
	// Worktree is the git worktree made for the session, which Project is.
	Worktree *protocol.Worktree
}

// Session is a running agent process with its terminal state.
type Session struct {
	spec Spec

	cmd *exec.Cmd
	tty *os.File

	mu       sync.Mutex
	term     *gostty.Session
	state    protocol.SessionState
	exitCode *int64
	subs     map[int]*Subscription
	nextSub  int
	// final is the screen as the agent left it, plain and styled, kept
	// when the terminal is released so an exited session can still be
	// attached to and read.
	finalPlain, finalStyled *protocol.Snapshot

	conversationID string // guarded by mu
	transcriptPath string // guarded by mu
	name           string // guarded by mu
	title          string // latest terminal title, guarded by mu
	started        time.Time
	done           chan struct{}
	progress       activity.Progress // guarded by mu
	outputs        atomic.Uint64
	lastOutput     atomic.Int64 // unix nanoseconds

	// What the agent's hooks last reported, and the approval it waits
	// for; guarded by mu.
	status   protocol.AgentStatus
	statusAt time.Time
	approval *protocol.Approval
	// reported is what the agent's status line last said of its tokens:
	// the context window and the account's limits; guarded by mu.
	reported *protocol.Usage
}

// staleWork is how long an agent said to be working may print nothing
// before it is taken to have stopped: one at work redraws its spinner, and
// an interrupted turn reports no end.
const staleWork = time.Minute

// Start launches the agent on a PTY.
func Start(spec Spec) (*Session, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("session: empty command")
	}
	if spec.Cols == 0 {
		spec.Cols = 80
	}
	if spec.Rows == 0 {
		spec.Rows = 24
	}

	term, err := gostty.New(spec.Cols, spec.Rows)
	if err != nil {
		return nil, fmt.Errorf("session: terminal: %w", err)
	}

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.Project
	cmd.Env = append(terminalEnv(os.Environ()), "MAKO_SESSION="+spec.ID)
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: spec.Cols, Rows: spec.Rows})
	if err != nil {
		term.Close()
		return nil, fmt.Errorf("session: start %s: %w", spec.Command[0], err)
	}

	s := &Session{
		spec:    spec,
		cmd:     cmd,
		tty:     tty,
		term:    term,
		state:   protocol.StateRunning,
		subs:    map[int]*Subscription{},
		started: time.Now(),
		done:    make(chan struct{}),
	}
	go s.pump()
	return s, nil
}

// ID reports the session identifier.
func (s *Session) ID() string { return s.spec.ID }

// Agent is the agent the session runs, as its launch named it.
func (s *Session) Agent() string { return s.spec.Agent }

// Info snapshots the session metadata.
func (s *Session) Info() *protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &protocol.SessionInfo{
		Id:             s.spec.ID,
		NodeId:         s.spec.NodeID,
		Agent:          s.spec.Agent,
		Caps:           agents.Caps(s.spec.Agent),
		Profile:        s.spec.Profile,
		Name:           s.name,
		ConversationId: s.conversationID,
		Title:          s.title,
		Project:        s.spec.Project,
		Command:        s.spec.Command,
		State:          s.state,
		Cols:           uint32(s.spec.Cols),
		Rows:           uint32(s.spec.Rows),
		StartedAt:      protocol.Time(s.started),
		ExitCode:       s.exitCode,
		Busy:           s.state != protocol.StateExited && s.progress.Busy(time.Now()),
		Idle:           s.state == protocol.StateRunning && foregroundIsSelf(s.tty, s.cmd.Process.Pid),
		Status:         s.statusLocked(),
		StatusAt:       protocol.Time(s.statusAt),
		LastOutput:     protocol.Time(s.LastOutput()),
		Approval:       s.approval,
		Worktree:       s.spec.Worktree,
	}
}

// statusLocked is what the agent is doing, as far as its hooks said.
func (s *Session) statusLocked() protocol.AgentStatus {
	switch {
	case s.state == protocol.StateExited:
		return ""
	case s.approval != nil:
		return protocol.AgentWaiting
	case protocol.AtWork(s.status) && time.Since(s.LastOutput()) > staleWork && time.Since(s.statusAt) > staleWork:
		return protocol.AgentIdle
	}
	return s.status
}

// SetStatus records what the agent's hooks report it is doing.
func (s *Session) SetStatus(status protocol.AgentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != status {
		s.status, s.statusAt = status, time.Now()
	}
}

// Report records what the agent's status line says of its tokens. What a
// report leaves out, such as limits before the first reply, stays as the
// last one said.
func (s *Session) Report(u *protocol.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reported == nil {
		s.reported = &protocol.Usage{}
	}
	s.reported.Merge(u)
}

// Reported is a copy of what the agent's status line has said of its
// tokens, nil for nothing.
func (s *Session) Reported() *protocol.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(s.reported)
}

// SetApproval records the approval the agent waits for, nil for none.
func (s *Session) SetApproval(a *protocol.Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approval = a
	if a != nil {
		s.status, s.statusAt = protocol.AgentWaiting, time.Now()
	}
}

// LastOutput is when the program last printed; zero before it has.
func (s *Session) LastOutput() time.Time {
	if ns := s.lastOutput.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// SetName names the session, "" for none.
func (s *Session) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// Write sends input to the agent.
func (s *Session) Write(p []byte) (int, error) { return s.tty.Write(p) }

// Resize changes the PTY and terminal dimensions together.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("session: zero window size")
	}
	if err := pty.Setsize(s.tty, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited {
		return nil // its screen is kept as it was left
	}
	s.spec.Cols, s.spec.Rows = cols, rows
	return s.term.Terminal().Resize(cols, rows)
}

// Snapshot renders the current screen. Styled output replays colors and
// styles; plain output is the text only.
func (s *Session) Snapshot(styled bool) (*protocol.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited {
		if styled {
			return s.finalStyled, nil
		}
		return s.finalPlain, nil
	}
	return s.snapshotLocked(styled)
}

func (s *Session) snapshotLocked(styled bool) (*protocol.Snapshot, error) {
	opts := gostty.FormatOptions{Format: gostty.FormatterFormatPlain}
	if styled {
		opts.Format = gostty.FormatterFormatVt
		opts.Cursor = true
	}
	text, err := s.term.FormatString(opts)
	if err != nil {
		return nil, err
	}
	if styled {
		text = modesAround(s.term.Terminal(), text)
	}
	return &protocol.Snapshot{
		SessionId: s.spec.ID,
		Cols:      uint32(s.spec.Cols),
		Rows:      uint32(s.spec.Rows),
		Text:      text,
		Styled:    styled,
		Title:     s.title,
		Progress:  s.progress.Snapshot(time.Now()),
	}, nil
}

// replayedModes are the DEC private modes a program sets that a terminal
// fed its screen must have too: how its keys and the mouse are encoded,
// and what it is told of. A TUI that asked for the mouse otherwise gets
// none, and its wheel scrolls the attaching terminal's scrollback instead.
// Each is true where a new terminal has it on.
var replayedModes = []struct {
	mode gostty.Mode
	on   bool
}{
	{gostty.ModeCursorKeys, false},
	{gostty.ModeCursorVisible, true},
	{gostty.ModeMouseEventX10, false},
	{gostty.ModeMouseEventNormal, false},
	{gostty.ModeMouseEventButton, false},
	{gostty.ModeMouseEventAny, false},
	{gostty.ModeMouseFormatUTF8, false},
	{gostty.ModeMouseFormatSgr, false},
	{gostty.ModeMouseFormatUrxvt, false},
	{gostty.ModeMouseFormatSgrPixels, false},
	{gostty.ModeMouseAlternateScroll, true},
	{gostty.ModeFocusEvent, false},
	{gostty.ModeBracketedPaste, false},
	{gostty.ModeReportColorScheme, false},
	{gostty.ModeInBandSizeReports, false},
}

// modesAround is a screen's text as a terminal must be fed it to be in the
// program's modes: on the alternate screen, where the program is on it,
// and after it each mode the program changed, set as it left it.
func modesAround(term *gostty.Terminal, text string) string {
	var b strings.Builder
	if term.ActiveScreenKey() == gostty.ScreenKeyAlternate {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString(text)
	for _, m := range replayedModes {
		if on := term.ModeEnabled(m.mode); on != m.on {
			set := byte('l')
			if on {
				set = 'h'
			}
			fmt.Fprintf(&b, "\x1b[?%d%c", int(m.mode), set)
		}
	}
	return b.String()
}

// Subscribe returns a view of the session's live PTY output. Subscribing to
// an exited session yields a subscription that is already closed.
func (s *Session) Subscribe() *Subscription {
	sub := &Subscription{s: s, signal: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.state == protocol.StateExited {
		sub.closed = true
		s.mu.Unlock()
		return sub
	}
	sub.id = s.nextSub
	s.nextSub++
	s.subs[sub.id] = sub
	s.mu.Unlock()
	return sub
}

// Done is closed once the agent process has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close terminates the agent and releases the terminal.
func (s *Session) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	err := s.tty.Close()
	<-s.done
	return err
}

// pump copies PTY output into the terminal state and out to subscribers.
func (s *Session) pump() {
	defer close(s.done)

	buf := make([]byte, 32*1024)
	for {
		n, err := s.tty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.feed(chunk)
		}
		if err != nil {
			break
		}
	}

	exitCode := int64(0)
	if err := s.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = int64(exitErr.ExitCode())
		} else {
			exitCode = -1
		}
	}

	s.mu.Lock()
	s.state = protocol.StateExited
	s.exitCode = &exitCode
	s.progress = activity.Progress{}
	for id, sub := range s.subs {
		delete(s.subs, id)
		sub.closeLocked()
	}
	s.finalPlain, _ = s.snapshotLocked(false)
	s.finalStyled, _ = s.snapshotLocked(true)
	s.term.Close()
	s.mu.Unlock()
}

func (s *Session) feed(chunk []byte) {
	s.outputs.Add(1)
	s.lastOutput.Store(time.Now().UnixNano())
	s.mu.Lock()
	if _, err := s.term.Write(chunk); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		// A parse failure must not kill the session; the raw bytes still reach
		// attached front-ends, only the server-side screen falls behind.
		_ = err
	}
	// The daemon's terminal is the one that answers the program's queries
	// (device attributes, cursor position, size reports): it sees every byte
	// and exists whether or not anyone is attached, so each query is answered
	// exactly once. Keep titles and progress for metadata and replay;
	// draining the rest keeps the queue from growing.
	stream := s.term.Stream()
	_ = stream.WriteReplies(s.tty)
	now := time.Now()
	for ev, err := range stream.EventValues() {
		if err != nil {
			break
		}
		if ev.Kind == gostty.StreamEventTitleChanged {
			s.title = ev.Title
		}
		s.progress.Report(ev, now)
	}
	for id, sub := range s.subs {
		if !sub.push(chunk) {
			// Dropped for falling too far behind; the front-end resyncs
			// from a snapshot rather than stalling the PTY for everyone.
			delete(s.subs, id)
		}
	}
	s.mu.Unlock()
}

// terminalEnv is the daemon's environment as a session's program sees it:
// with the terminal described as the one it runs in, not whichever one the
// daemon was started from. That terminal is gostty, Ghostty's core, so it
// is named Ghostty, as of the release whose features it has; programs that
// look for a terminal by name, as claude does before it reports progress,
// then find what it supports.
func terminalEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+4)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TERM_SESSION_ID", "COLORTERM",
			"LC_TERMINAL", "LC_TERMINAL_VERSION", "ITERM_SESSION_ID", "ITERM_PROFILE",
			"GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR", "GHOSTTY_SHELL_FEATURES",
			"KITTY_WINDOW_ID", "WT_SESSION", "VSCODE_INJECTION":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor",
		"TERM_PROGRAM=ghostty", "TERM_PROGRAM_VERSION="+ghosttyVersion)
}

// ghosttyVersion is the Ghostty release gostty's core is at least.
const ghosttyVersion = "1.2.0"
