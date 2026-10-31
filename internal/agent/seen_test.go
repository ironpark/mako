package agent

import (
	"fmt"
	"testing"
)

func TestSeenTellsEachIDOnce(t *testing.T) {
	var s Seen
	for i := range 10000 {
		if !s.Add(fmt.Sprintf("2f0c5d3e-%04d-4b6a-9c1e-7d8f9a0b1c2d", i)) {
			t.Fatalf("id %d was taken as read", i)
		}
	}
	if s.Add("2f0c5d3e-0042-4b6a-9c1e-7d8f9a0b1c2d") {
		t.Fatal("an id read again was taken as new")
	}
}
