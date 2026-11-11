// Package session runs a terminal agent on a PTY and keeps its screen state.
//
// Every session owns a gostty terminal that consumes the same bytes the PTY
// produces, so a front-end attaching late can be handed the current screen
// instead of an empty window.
package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/input"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/activity"
	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/kitty"
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
	// Shown is Command as Info reports it, nil for Command itself.
	Shown []string
	Env   map[string]string
	Cols  uint16
	Rows  uint16
	// CellWidth and CellHeight are a cell's size in pixels, as the
	// front-end last said, zero until one does.
	CellWidth, CellHeight uint16
	// Worktree is the git worktree made for the session, which Project is.
	Worktree *protocol.Worktree
	// OnChange, if set, is called as Changed's channel closes. It is
	// called with the session locked, so it must not block or call back.
	OnChange func()
}

// Session is a running agent process with its terminal state.
type Session struct {
	spec Spec

	proc  *proc    // the agent's process, on its terminal
	env   []string // the process's environment
	scope string   // scopeEnv's value, which every process it starts inherits

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

	conversationID string              // guarded by mu
	transcriptPath string              // guarded by mu
	name           string              // guarded by mu
	title          string              // latest terminal title, guarded by mu
	notice         *protocol.Notice    // the program's last notification, guarded by mu
	scheme         *gostty.ColorScheme // the front-end's, once one said; guarded by mu
	bells          uint64              // bells the program rang, guarded by mu
	started        time.Time
	done           chan struct{}
	progress       activity.Progress // guarded by mu
	outputs        atomic.Uint64
	lastOutput     atomic.Int64 // unix nanoseconds
	lastInput      atomic.Int64 // unix nanoseconds
	// replies are the terminal's answers to the program's queries, on
	// their way to writeReplies, which types them without mu held: a
	// program that stops reading its input fills the PTY, and a write
	// blocked on it must not hold the session, and every caller of Info
	// with it, until it reads again.
	replies chan []byte

	// What the agent's hooks last reported, and the approval it waits
	// for; guarded by mu.
	status        protocol.AgentStatus
	statusAt      time.Time
	turns         uint64 // turns ended: work that went idle or done
	changed       chan struct{}
	statusSeq     int64
	workEndedAt   int64 // explicit terminal inactivity, for late work hooks
	agentsWorking bool
	childHooks    map[string]childStatus
	childLogs     []*protocol.SubAgent
	tasks         []*protocol.BackgroundTask // its agent's in the background, as its log says
	approval      *protocol.Approval
	approvalDone  chan struct{}
	lastMessage   time.Time // when the conversation last said something, as its log has it
	// screen is what the agent's screen shows, read by its driver's
	// rules; nil for an agent whose screen is not read.
	screen *screenWatch
	// reported is what the agent's status line last said of its tokens:
	// the context window and the account's limits; guarded by mu.
	reported *protocol.Usage
}

// staleWork is how long an agent said to be working may print nothing
// before it is taken to have stopped: one at work redraws its spinner, and
// an interrupted turn reports no end.
const staleWork = time.Minute

type childStatus struct {
	running bool
	seq     int64
}

// live is a child its hooks say runs, unless they fell silent too long ago.
func (c childStatus) live() bool {
	return c.running && time.Since(time.Unix(0, c.seq)) < agent.StaleAgent
}

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

	var token [16]byte
	_, _ = rand.Read(token[:])
	scope := hex.EncodeToString(token[:])
	env := append(terminalEnv(os.Environ()), "MAKO_SESSION="+spec.ID, scopeEnv+"="+scope)
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	p, err := startProc(spec.Command, spec.Project, env, spec.Cols, spec.Rows)
	if err != nil {
		term.Close()
		return nil, fmt.Errorf("session: start %s: %w", spec.Command[0], err)
	}

	s := &Session{
		spec:    spec,
		proc:    p,
		env:     env,
		scope:   scope,
		term:    term,
		state:   protocol.StateRunning,
		subs:    map[int]*Subscription{},
		started: time.Now(),
		done:    make(chan struct{}),
		replies: make(chan []byte, replyQueue),
	}
	if d, ok := agents.As[agent.Screen](spec.Agent); ok {
		s.screen = &screenWatch{rules: d.ScreenRules()}
		go s.watchScreen()
	}
	go s.pump()
	go s.writeReplies()
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
	status, statusAt := s.statusLocked()
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
		Command:        s.shown(),
		State:          s.state,
		Cols:           uint32(s.spec.Cols),
		Rows:           uint32(s.spec.Rows),
		StartedAt:      protocol.Time(s.started),
		ExitCode:       s.exitCode,
		Busy:           s.state != protocol.StateExited && s.progress.Busy(time.Now()),
		Idle:           s.state == protocol.StateRunning && s.proc.idle(),
		Status:         status,
		StatusAt:       protocol.Time(statusAt),
		LastOutput:     protocol.Time(s.LastOutput()),
		LastMessage:    protocol.Time(s.lastMessage),
		Approval:       s.approval,
		Worktree:       s.spec.Worktree,
		Tasks:          s.tasksLocked(),
		Turns:          s.turns,
		Notice:         s.notice,
		Bells:          s.bells,
	}
}

// Changed is a channel closed the next time the agent's status, approval
// or state changes, or a turn ends. Take it before reading what changes:
// a change after Info closes the channel taken before it. Status worked
// out as it is read, as work gone stale, changes unannounced.
func (s *Session) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *Session) changedLocked() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
	if s.spec.OnChange != nil {
		s.spec.OnChange()
	}
}

// ReportTasks keeps the commands the agent of conversation id runs in the
// background, as its log says. One started before this process was is
// not running, whatever the log says: it was the last process's.
func (s *Session) ReportTasks(id string, tasks []*protocol.BackgroundTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || s.conversationID != id {
		return
	}
	for _, t := range tasks {
		if t.GetRunning() && t.GetStarted() < s.started.Unix() {
			t.Running, t.Status = false, protocol.TaskStopped
		}
	}
	s.tasks = tasks
}

// tasksLocked is the tasks Info reports: none once the session has ended.
func (s *Session) tasksLocked() []*protocol.BackgroundTask {
	if s.state == protocol.StateExited {
		return nil
	}
	return s.tasks
}

// shown is the command as Info reports it.
func (s *Session) shown() []string {
	if s.spec.Shown != nil {
		return s.spec.Shown
	}
	return s.spec.Command
}

// statusLocked is what the agent is doing, as far as its hooks said and
// its screen shows over them, and since when.
func (s *Session) statusLocked() (protocol.AgentStatus, time.Time) {
	s.updateChildrenLocked()
	if s.state == protocol.StateExited {
		return "", s.statusAt
	}
	if s.approval != nil {
		return protocol.AgentWaiting, s.statusAt
	}
	if status, at, ok := s.screenStatusLocked(); ok {
		return status, at
	}
	switch {
	case s.agentsWorking && s.status != protocol.AgentWaiting && !protocol.AtWork(s.status):
		return protocol.AgentWorking, s.statusAt
	case !s.agentsWorking && protocol.AtWork(s.status) && time.Since(s.LastOutput()) > staleWork && time.Since(s.statusAt) > staleWork:
		// Latch the fallback: an unrelated redraw must not revive a turn.
		s.endWorkLocked(time.Now())
	}
	return s.status, s.statusAt
}

// SetStatus records what the agent's hooks report it is doing.
func (s *Session) SetStatus(status protocol.AgentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The wall clock reads in microseconds on macOS: two statuses set in a
	// row can read the same, and the second would be taken as no newer
	// than the first. A status set here is the newest there is.
	seq := max(time.Now().UnixNano(), s.statusSeq+1, s.workEndedAt+1)
	s.reportStatusLocked(status, seq)
}

// ReportStatus orders hooks by when they ran, rather than when their
// connections happened to be served. Repeated reports renew the evidence.
func (s *Session) ReportStatus(status protocol.AgentStatus, seq int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reportStatusLocked(status, seq)
}

// ReportToolStatus cannot withdraw a request while another concurrent
// tool finishes. Turn boundaries still supersede requests via ReportStatus.
func (s *Session) ReportToolStatus(status protocol.AgentStatus, seq int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approval != nil {
		return false
	}
	return s.reportStatusLocked(status, seq)
}

func (s *Session) reportStatusLocked(status protocol.AgentStatus, seq int64) bool {
	if s.state == protocol.StateExited || seq <= s.statusSeq || (protocol.AtWork(status) && seq <= s.workEndedAt) {
		return false
	}
	s.clearApprovalLocked()
	// The log and Stop hook can report the same completion. Renew work
	// evidence, but keep one completion timestamp for attention and alerts.
	if s.status != status || status != protocol.AgentDone {
		s.statusAt = time.Now()
	}
	s.setStatusLocked(status)
	s.statusSeq = seq
	return true
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

// BeginApproval holds a request only while its hook is still current.
// The returned channel closes when another event supersedes it.
func (s *Session) BeginApproval(seq int64, a *protocol.Approval) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || s.statusSeq != seq {
		return nil, false
	}
	s.clearApprovalLocked()
	s.approval = a
	s.approvalDone = make(chan struct{})
	s.changedLocked()
	return s.approvalDone, true
}

// FinishApproval cannot clear a newer request or overwrite a newer turn.
func (s *Session) FinishApproval(seq int64, id string, status protocol.AgentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusSeq == seq && s.approval.GetId() == id {
		s.clearApprovalLocked()
		s.setStatusLocked(status)
		s.statusAt = time.Now()
	}
}

// endWorkLocked records that the turn ended at, and refuses work reported
// from before it.
func (s *Session) endWorkLocked(at time.Time) {
	s.workEndedAt = at.UnixNano()
	if protocol.AtWork(s.status) {
		s.setStatusLocked(protocol.AgentIdle)
		s.statusAt = at
	}
}

// setStatusLocked records the agent's status, counting the end of a turn
// it makes.
func (s *Session) setStatusLocked(next protocol.AgentStatus) {
	if next == s.status {
		return
	}
	if protocol.AtWork(s.status) && (next == protocol.AgentIdle || next == protocol.AgentDone) {
		s.turns++
	}
	s.status = next
	s.changedLocked()
}

// Turns is how many turns the agent has ended.
func (s *Session) Turns() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns
}

func (s *Session) clearApprovalLocked() {
	if s.approval != nil {
		s.approval = nil
		s.changedLocked()
	}
	if s.approvalDone != nil {
		close(s.approvalDone)
		s.approvalDone = nil
	}
}

// ReportActivity reconciles the bound log and its children independently
// of whether a client is reading chat. Historical turns never restart work.
// said is when its latest prompt or reply was written.
func (s *Session) ReportActivity(id string, status protocol.AgentStatus, at, said time.Time, children []*protocol.SubAgent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || s.conversationID != id {
		return
	}
	if status != "" && !at.Before(s.started) {
		s.reportStatusLocked(status, at.UnixNano())
	}
	s.lastMessage = said
	s.childLogs = children
	s.updateChildrenLocked()
}

// ReportSubagent is a child's own lifecycle, ordered independently of its
// siblings and parent. A child's Stop never ends the parent's turn.
func (s *Session) ReportSubagent(conversation, id string, running bool, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited || (conversation != "" && s.conversationID != "" && s.conversationID != conversation) || seq <= s.childHooks[id].seq {
		return
	}
	if s.childHooks == nil {
		s.childHooks = map[string]childStatus{}
	}
	s.childHooks[id] = childStatus{running, seq}
	s.updateChildrenLocked()
}

func (s *Session) updateChildrenLocked() {
	running := false
	for _, child := range s.childHooks {
		running = running || child.live()
	}
	for _, child := range s.childLogs {
		if hook, hooked := s.childHooks[child.GetId()]; !hooked || (hook.running && !hook.live()) {
			running = running || child.GetRunning()
		}
	}
	if s.agentsWorking && !running && s.status == protocol.AgentDone {
		s.statusAt = time.Now()
	}
	if s.agentsWorking != running {
		s.agentsWorking = running
		s.changedLocked()
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
func (s *Session) Write(p []byte) (int, error) {
	s.lastInput.Store(time.Now().UnixNano())
	return s.proc.Write(p)
}

// LastInput is when anything was last written to the agent; zero before.
func (s *Session) LastInput() time.Time {
	if ns := s.lastInput.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// AtPrompt reports whether the agent waits at its prompt, as its own hooks
// last said, and since when: its turn done or not yet begun, nothing asked
// of the user. Subagents it left working do not keep it from a prompt.
func (s *Session) AtPrompt() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusLocked() // latches the end of a turn gone quiet
	if s.state != protocol.StateRunning || s.approval != nil {
		return false, time.Time{}
	}
	// The screen, where it outweighs the hooks: a dialog it shows is not a
	// prompt, which typing would answer.
	status, at, ok := s.screenStatusLocked()
	if !ok {
		status, at = s.status, s.statusAt
	}
	return status == protocol.AgentIdle || status == protocol.AgentDone, at
}

// Prompt gives the agent text as the composer does: pasted, then Enter.
// Text of several lines needs a program that takes bracketed pastes, which
// would otherwise run each line as a prompt of its own.
func (s *Session) Prompt(text string) error {
	s.mu.Lock()
	if s.state == protocol.StateExited {
		s.mu.Unlock()
		return errors.New("session: exited")
	}
	bracketed := s.term.Terminal().ModeEnabled(gostty.ModeBracketedPaste)
	s.mu.Unlock()
	if !bracketed && strings.ContainsAny(text, "\r\n") {
		return errors.New("session: the program takes no bracketed paste for lines")
	}
	return s.Send(text, []byte{'\r'})
}

// pasteGap is how long a paste of n bytes stands before the keys sent
// after it, as Enter: a program that takes a burst of input as a paste
// would take them as part of it, and not submit. A Windows console hands
// a program a paste as a key event a character, which it may still be
// reading when the gap, counted from the write, has passed: Codex there
// tells a paste by its burst alone, and its Enter would be a new line. The
// gap there is longer, and grows with the paste.
func pasteGap(n int) time.Duration {
	if goruntime.GOOS != "windows" {
		return 200 * time.Millisecond
	}
	return min(500*time.Millisecond+time.Duration(n)*time.Millisecond/4, 2*time.Second)
}

// Send pastes text, if any, then types keys, if any, pasteGap after it.
// Text goes to the agent's own prompt, as a message: from a view over it,
// as a subagent's, the agent is first taken back there.
func (s *Session) Send(text string, keys []byte) error {
	if text != "" {
		s.recall()
	}
	return s.send(text, keys)
}

// send is Send as typed, wherever the screen is.
func (s *Session) send(text string, keys []byte) error {
	if text != "" {
		if err := s.Paste(text); err != nil {
			return err
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if text != "" {
		time.Sleep(pasteGap(len(text)))
	}
	_, err := s.Write(keys)
	return err
}

// Paste sends text to the agent as a paste: bracketed when its program
// enabled bracketed paste, plain otherwise, its line breaks meant. A
// paste's own end marker, which would let the rest run as if typed, is
// refused.
func (s *Session) Paste(text string) error {
	if strings.Contains(text, "\x1b[201~") {
		return errors.New("session: the text ends a paste")
	}
	var buf bytes.Buffer
	s.mu.Lock()
	if s.state == protocol.StateExited {
		s.mu.Unlock()
		return errors.New("session: exited")
	}
	err := input.EncodePaste(&buf, s.term.Terminal(), []byte(text))
	s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = s.Write(buf.Bytes())
	return err
}

// Resize changes the PTY and terminal dimensions together.
func (s *Session) Resize(cols, rows uint16) error { return s.ResizeCells(cols, rows, 0, 0) }

// ResizeCells changes the PTY and terminal dimensions together, with a
// cell's size in pixels, which zero keeps as it was: the PTY says the
// window's size in pixels from it, as programs that draw images ask, and
// the terminal sizes images placed in cells by it.
func (s *Session) ResizeCells(cols, rows, cellW, cellH uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("session: zero window size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cellW == 0 || cellH == 0 {
		cellW, cellH = s.spec.CellWidth, s.spec.CellHeight
	}
	if err := s.proc.resize(cols, rows, cellW, cellH); err != nil {
		return err
	}
	if s.state == protocol.StateExited {
		return nil // its screen is kept as it was left
	}
	if cols == s.spec.Cols && rows == s.spec.Rows && cellW == s.spec.CellWidth && cellH == s.spec.CellHeight {
		return nil // a size report for no change would be a lie
	}
	s.spec.Cols, s.spec.Rows = cols, rows
	s.spec.CellWidth, s.spec.CellHeight = cellW, cellH
	// Through the stream, not the terminal: a program that asked for
	// in-band size reports (mode 2048) is owed one, which only the stream
	// queues.
	stream := s.term.Stream()
	if err := stream.ResizeCells(cols, rows, uint32(cellW), uint32(cellH)); err != nil {
		return err
	}
	s.queueRepliesLocked(stream)
	return nil
}

// SetColors makes c the colours the session's terminal answers a program
// asking for them: its default foreground, background and ANSI palette,
// and whether it is dark, which a program that subscribed (mode 2031) is
// told when it changes.
func (s *Session) SetColors(c *protocol.TerminalColors) {
	if c == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == protocol.StateExited {
		return
	}
	term := s.term.Terminal()
	for i, rgb := range c.GetPalette() {
		if i < 16 {
			_ = term.SetDefaultPaletteColor(uint8(i), gostty.RGBFromUint32(rgb))
		}
	}
	term.SetDefaultForegroundColor(gostty.RGBFromUint32(c.GetForeground()))
	term.SetDefaultBackgroundColor(gostty.RGBFromUint32(c.GetBackground()))
	scheme := gostty.ColorSchemeLight
	if c.GetDark() {
		scheme = gostty.ColorSchemeDark
	}
	if s.scheme == nil || *s.scheme != scheme {
		s.scheme = &scheme
		stream := s.term.Stream()
		stream.ColorSchemeChanged(scheme)
		s.queueRepliesLocked(stream)
	}
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
	var text string
	var err error
	if styled {
		if text, err = s.term.FormatString(gostty.FormatOptions{Format: gostty.FormatterFormatVt, Cursor: true}); err == nil {
			text = modesAround(s.term.Terminal(), text)
		}
	} else {
		text, err = s.plainTextLocked()
	}
	if err != nil {
		return nil, err
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

// plainTextLocked is the text the screen shows, without its styles.
func (s *Session) plainTextLocked() (string, error) {
	return s.term.FormatString(gostty.FormatOptions{Format: gostty.FormatterFormatPlain})
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
// and after it the images the program placed on it, the Kitty keyboard
// flags it pushed and each mode it changed, set as it left them.
func modesAround(term *gostty.Terminal, text string) string {
	var b strings.Builder
	if term.ActiveScreenKey() == gostty.ScreenKeyAlternate {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString(text)
	b.WriteString(kitty.Replay(term))
	b.WriteString(kitty.Flags(term).Restore())
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

// scopeEnv names a variable of a session's own in its program's
// environment, which every process it starts inherits, so that what it
// left running is found when the session is closed.
const scopeEnv = "MAKO_SCOPE"

// Close terminates the agent, and every process it started that runs
// still, and releases the terminal.
func (s *Session) Close() error {
	s.proc.kill()
	err := s.proc.Close()
	<-s.done
	return err
}

// outputGrace is how long the terminal's output is read once the agent has
// exited: long enough for what it printed last, and no longer, as a
// process it left holding the terminal would otherwise keep the session
// from ever ending.
const outputGrace = time.Second

// pump copies PTY output into the terminal state and out to subscribers.
func (s *Session) pump() {
	defer close(s.done)

	// The agent is waited for as it exits, not once its terminal's output
	// ends, which a process it left in the background may hold off
	// forever. What it started is swept then, while its leader's id still
	// names its terminal's session: by the time the session is closed, that
	// id may be another process's, and its session another's too.
	exited := make(chan int64, 1)
	go func() {
		code := s.proc.wait()
		sweep(s.scope, s.proc.pid())
		s.proc.endOutput(outputGrace)
		exited <- code
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.feed(chunk)
		}
		if err != nil {
			break
		}
	}

	exitCode := <-exited

	s.mu.Lock()
	s.state = protocol.StateExited
	s.clearApprovalLocked()
	s.exitCode = &exitCode
	s.progress = activity.Progress{}
	for id, sub := range s.subs {
		delete(s.subs, id)
		sub.closeLocked()
	}
	s.finalPlain, _ = s.snapshotLocked(false)
	s.finalStyled, _ = s.snapshotLocked(true)
	s.term.Close()
	s.changedLocked()
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
	s.queueRepliesLocked(stream)
	now := time.Now()
	for ev, err := range stream.EventValues() {
		if err != nil {
			break
		}
		switch ev.Kind {
		case gostty.StreamEventTitleChanged:
			s.title = ev.Title
		case gostty.StreamEventBell:
			s.bells++
			s.changedLocked()
		case gostty.StreamEventDesktopNotification:
			s.notice = &protocol.Notice{Title: ev.Title, Body: ev.Body, Seq: s.notice.GetSeq() + 1}
			s.changedLocked()
		}
		s.progress.Report(ev, now)
		// Interruptions need not run a Stop hook. An explicit removal is
		// evidence that the terminal's turn ended; silence alone is not.
		if ev.Kind == gostty.StreamEventProgressReport && ev.ProgressState == gostty.ProgressStateRemove {
			s.endWorkLocked(now)
		}
	}
	s.pushLocked(chunk)
	s.mu.Unlock()
}

// replyQueue is how many batches of replies may wait for a program that
// reads none of its input; those after it are dropped, as a program that
// reads nothing would not have seen them anyway.
const replyQueue = 64

// queueRepliesLocked takes what the terminal has answered and queues it
// for writeReplies.
func (s *Session) queueRepliesLocked(stream *gostty.Stream) {
	if !stream.HasReplies() {
		return
	}
	var b bytes.Buffer
	if stream.WriteReplies(&b) != nil || b.Len() == 0 {
		return
	}
	select {
	case s.replies <- b.Bytes():
	default:
	}
}

// writeReplies types the terminal's replies to the program, in the order
// they were made, until the program exits.
func (s *Session) writeReplies() {
	for {
		select {
		case b := <-s.replies:
			_, _ = s.proc.Write(b)
		case <-s.done:
			return
		}
	}
}

// pushLocked sends chunk to every front-end attached.
func (s *Session) pushLocked(chunk []byte) {
	for id, sub := range s.subs {
		if !sub.push(chunk) {
			// Dropped for falling too far behind; the front-end resyncs
			// from a snapshot rather than stalling the PTY for everyone.
			delete(s.subs, id)
		}
	}
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
