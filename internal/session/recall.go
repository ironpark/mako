package session

import (
	"bytes"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent may show a view over its own prompt, where text sent from
// mako would go to another agent or be lost: a subagent's, its transcript.
// Before text is sent, the agent is taken back to its prompt, a step at a
// time, each read off the screen as the one before it left it.

const (
	// recallSteps bounds the keys typed to get back.
	recallSteps = 8
	// recallWait is how long a step waits for the screen to show it.
	recallWait = 500 * time.Millisecond
	// recallPoll is how often the screen is read meanwhile.
	recallPoll = 15 * time.Millisecond
)

// recall takes the agent back to its own prompt from a view over it, as
// its driver reads the screen.
func (s *Session) recall() {
	r, ok := agents.As[agent.Recaller](s.spec.Agent)
	if !ok {
		return
	}
	for range recallSteps {
		screen, seen, ok := s.recallScreen()
		if !ok {
			return
		}
		keys := r.Recall(screen)
		if keys == nil {
			return
		}
		if _, err := s.Write(keys); err != nil {
			return
		}
		if !s.awaitFoot(screen, seen) {
			return
		}
	}
}

// awaitFoot waits, a while, until the screen's foot, where a step typed
// shows, is drawn anew from what screen, of seen outputs, showed; the
// screen is read again only once the program has printed. False once the
// agent has exited.
func (s *Session) awaitFoot(screen string, seen uint64) bool {
	was := foot(screen)
	for deadline := time.Now().Add(recallWait); time.Now().Before(deadline); {
		time.Sleep(recallPoll)
		if s.outputs.Load() == seen {
			continue
		}
		now, n, ok := s.recallScreen()
		if !ok {
			return false
		}
		if seen = n; foot(now) != was {
			break
		}
	}
	return true
}

// recallScreen is the screen shown, without the scrollback, and the
// outputs it shows, while the agent runs.
func (s *Session) recallScreen() (string, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != protocol.StateRunning {
		return "", 0, false
	}
	seen := s.outputs.Load()
	text, err := s.term.Terminal().PlainString()
	return text, seen, err == nil
}

// foot is the foot of screen, under the last rule drawn across it, as
// under a prompt box: where a step back shows, without the counts that
// tick there as the agent works.
func foot(screen string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return -1
		}
		return r
	}, detect.AfterRule(detect.Input{Screen: screen}))
}

// stepGap is how long a step typed waits for the one before it to show.
const stepGap = 150 * time.Millisecond

// Type types steps into the agent's terminal as they are, wherever its
// screen is, each's keys as its program asked for keys, a while apart.
func (s *Session) Type(steps []agent.Keystroke) error {
	for i, step := range steps {
		if i > 0 {
			time.Sleep(stepGap)
		}
		keys, err := s.encodeKeys(step.Keys)
		if err != nil {
			return err
		}
		if err := s.send(step.Text, keys); err != nil {
			return err
		}
	}
	return nil
}

// encodeKeys are keys pressed, as the program asked for keys.
func (s *Session) encodeKeys(keys []input.Key) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != protocol.StateRunning {
		return nil, errors.New("session: exited")
	}
	var buf bytes.Buffer
	for _, k := range keys {
		if err := input.EncodeKey(&buf, s.term.Terminal(), input.KeyEvent{Action: input.KeyActionPress, Key: k}, ""); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// Shows reports whether the screen shown, while the agent runs, is one
// shows passes.
func (s *Session) Shows(shows func(screen string) bool) bool {
	screen, _, ok := s.recallScreen()
	return ok && shows(screen)
}
