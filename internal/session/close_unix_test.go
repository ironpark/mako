//go:build !windows

package session

import (
	"testing"
	"time"
)

// Close of a session whose program is exiting, or has just exited, ends the
// session without an error. The pump closes the terminal as the program
// exits, so Close must not report that terminal as closed twice.
func TestCloseAsTheProgramExitsIsNoError(t *testing.T) {
	for i := range 200 {
		s, err := Start(Spec{ID: "exits", Command: []string{"/bin/sh", "-c", "exit 0"}, Cols: 40, Rows: 5})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(i%20) * 100 * time.Microsecond)
		if err := s.Close(); err != nil {
			t.Fatalf("run %d: Close = %v", i, err)
		}
	}
}
