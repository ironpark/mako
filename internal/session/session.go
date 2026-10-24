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
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/ironpark/gostty"

	"github.com/ironpark/sanbo/internal/protocol"
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
}

// Session is a running agent process with its terminal state.
type Session struct {
	spec Spec

	cmd *exec.Cmd
	tty *os.File

	mu       sync.Mutex
	term     *gostty.Session
	state    protocol.SessionState
	exitCode *int
	subs     map[int]chan []byte
	nextSub  int

	started time.Time
	done    chan struct{}
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

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.Project
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "SANBO_SESSION="+spec.ID)
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
		subs:    map[int]chan []byte{},
		started: time.Now(),
		done:    make(chan struct{}),
	}
	go s.pump()
	return s, nil
}

// ID reports the session identifier.
func (s *Session) ID() string { return s.spec.ID }

// Info snapshots the session metadata.
func (s *Session) Info() protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return protocol.SessionInfo{
		ID:        s.spec.ID,
		NodeID:    s.spec.NodeID,
		Agent:     s.spec.Agent,
		Profile:   s.spec.Profile,
		Project:   s.spec.Project,
		Command:   s.spec.Command,
		State:     s.state,
		Cols:      s.spec.Cols,
		Rows:      s.spec.Rows,
		StartedAt: s.started,
		ExitCode:  s.exitCode,
	}
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
	s.spec.Cols, s.spec.Rows = cols, rows
	return s.term.Terminal().Resize(cols, rows)
}

// Snapshot renders the current screen. Styled output replays colors and
// styles; plain output is the text only.
func (s *Session) Snapshot(styled bool) (protocol.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	opts := gostty.FormatOptions{Format: gostty.FormatterFormatPlain}
	if styled {
		opts.Format = gostty.FormatterFormatVt
		opts.Cursor = true
	}
	text, err := s.term.FormatString(opts)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	return protocol.Snapshot{
		SessionID: s.spec.ID,
		Cols:      s.spec.Cols,
		Rows:      s.spec.Rows,
		Text:      text,
		Styled:    styled,
	}, nil
}

// Subscribe returns a channel of live PTY output and a function to stop it.
// A subscriber that falls behind is dropped (its channel closed while the
// session is still running) rather than stalling the PTY. Subscribing to an
// exited session yields an already-closed channel.
func (s *Session) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	s.mu.Lock()
	if s.state == protocol.StateExited {
		s.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if sub, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(sub)
		}
	}
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

	exitCode := 0
	if err := s.cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	s.mu.Lock()
	s.state = protocol.StateExited
	s.exitCode = &exitCode
	for id, sub := range s.subs {
		delete(s.subs, id)
		close(sub)
	}
	s.term.Close()
	s.mu.Unlock()
}

func (s *Session) feed(chunk []byte) {
	s.mu.Lock()
	if _, err := s.term.Write(chunk); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		// A parse failure must not kill the session; the raw bytes still reach
		// attached front-ends, only the server-side screen falls behind.
		_ = err
	}
	// The daemon's terminal is the one that answers the program's queries
	// (device attributes, cursor position, size reports): it sees every byte
	// and exists whether or not anyone is attached, so each query is answered
	// exactly once. Events are not acted on here; draining them keeps the
	// queue from growing for the life of the session.
	stream := s.term.Stream()
	_ = stream.WriteReplies(s.tty)
	for _, err := range stream.EventValues() {
		if err != nil {
			break
		}
	}
	for id, sub := range s.subs {
		select {
		case sub <- chunk:
		default:
			// Slow subscriber: drop it so one stalled front-end cannot block
			// the PTY reader for everyone else.
			delete(s.subs, id)
			close(sub)
		}
	}
	s.mu.Unlock()
}
