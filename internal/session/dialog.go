package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ironpark/gostty/input"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/detect"
	"github.com/ironpark/mako/internal/protocol"
)

// A dialog in an agent's terminal that no hook holds, as a folder's trust,
// or a permission asked while no front-end watched, asks there to pick one
// of its options. While the screen shows one, the session says so, and a
// front-end picks for the user as the user would: the cursor moved to the
// option with the arrow keys, each move read off the screen, then Enter.

// dialogMoves bounds the moves made to bring the cursor to an option.
const dialogMoves = 4

// screenDialog is the dialog of options screen shows, cols wide, nil for
// none.
func screenDialog(screen string, cols int) *protocol.ScreenDialog {
	c, ok := detect.ParseChoice(screen, cols)
	if !ok {
		return nil
	}
	return &protocol.ScreenDialog{Id: dialogID(c), Text: c.Text, Options: c.Options, Selected: int32(c.Selected)}
}

// dialogID names a dialog by what it asks and offers, wherever its cursor.
func dialogID(c detect.Choice) string {
	sum := sha256.Sum256([]byte(c.Text + "\x00" + strings.Join(c.Options, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// readDialogLocked notes the dialog the screen shows while the agent waits
// on one, and reports whether that changed.
func (s *Session) readDialogLocked() bool {
	w := s.screen
	var d *protocol.ScreenDialog
	if w.state == detect.Blocked && w.visible && s.approval == nil {
		if text, err := s.term.Terminal().PlainString(); err == nil {
			d = screenDialog(text, int(s.spec.Cols))
		}
	}
	if proto.Equal(d, w.dialog) {
		return false
	}
	w.dialog = d
	return true
}

// dialogLocked is the dialog the agent waits on in its terminal, nil for
// none.
func (s *Session) dialogLocked() *protocol.ScreenDialog {
	if s.screen == nil || s.state != protocol.StateRunning {
		return nil
	}
	return s.screen.dialog
}

// AnswerDialog picks option of the dialog called id the agent's terminal
// shows: refused once it shows another, or none.
func (s *Session) AnswerDialog(id string, option int) error {
	s.mu.Lock()
	cols := int(s.spec.Cols)
	s.mu.Unlock()
	for range dialogMoves {
		screen, seen, ok := s.recallScreen()
		if !ok {
			return errors.New("session: exited")
		}
		d := screenDialog(screen, cols)
		if d == nil || d.GetId() != id {
			return errors.New("the agent no longer shows that dialog")
		}
		if option < 0 || option >= len(d.GetOptions()) {
			return errors.New("no such option")
		}
		at := int(d.GetSelected())
		if at == option {
			return s.Type([]agent.Keystroke{{Keys: []input.Key{input.KeyEnter}}})
		}
		key, n := input.KeyArrowDown, option-at
		if n < 0 {
			key, n = input.KeyArrowUp, -n
		}
		keys := make([]input.Key, n)
		for i := range keys {
			keys[i] = key
		}
		if err := s.Type([]agent.Keystroke{{Keys: keys}}); err != nil {
			return err
		}
		if !s.awaitFoot(screen, seen) {
			return errors.New("session: exited")
		}
	}
	return errors.New("the dialog's cursor did not reach the option")
}
