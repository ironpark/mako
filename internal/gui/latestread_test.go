package gui

import "testing"

// One read is under way at a time; of those asked for meanwhile, only the
// latest is read after, and none when it is what was being read.
func TestALatestReadReadsTheLatestAskedForAfter(t *testing.T) {
	var r latestRead[string]
	if !r.ask("a") {
		t.Fatal("a first read did not start")
	}
	if r.ask("b") || r.ask("c") {
		t.Fatal("a read started while one was under way")
	}
	if next, again := r.done(); !again || next != "c" {
		t.Fatalf("read next %q, %v", next, again)
	}
	r.ask("a")
	r.ask("b")
	r.ask("a") // back to what is being read
	if _, again := r.done(); again {
		t.Fatal("what was being read is read again")
	}
	if !r.ask("d") {
		t.Fatal("a read after the last did not start")
	}
}
