package daemon

import (
	"testing"
	"time"
)

// A link that fails waits longer each time it fails again, up to a cap.
func TestALinkFailingWaitsLongerEachTime(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := relinkWait(i + 1); got != w {
			t.Errorf("after %d failures: %v, want %v", i+1, got, w)
		}
	}
}
