package session

import (
	"encoding/base64"
	"time"
)

// copyQuiet is how long the program must have printed nothing before a
// copy goes to the front-ends, and copyWait how long a copy waits for it
// at most: one sent amid a sequence the program has half written would
// break it in the front-ends' terminals.
const (
	copyQuiet = 30 * time.Millisecond
	copyWait  = time.Second
)

// Copy puts text on the clipboard of every front-end attached, as a
// program does with OSC 52 itself: the session's program put it on the
// clipboard of the session's node, which is not theirs. It goes to them
// alone, not to the session's own terminal, and is gone for one attached
// later.
func (s *Session) Copy(text string) {
	seq := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
	go func() {
		for deadline := time.Now().Add(copyWait); ; {
			wait := min(time.Until(s.LastOutput().Add(copyQuiet)), time.Until(deadline))
			if wait <= 0 {
				break
			}
			time.Sleep(wait)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pushLocked(seq)
	}()
}
