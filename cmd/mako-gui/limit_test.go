package main

import "testing"

func TestTheLimitKeepsClearOfTheLiveHeap(t *testing.T) {
	if got := limitFor(60 << 20); got != memoryLimit {
		t.Errorf("a small live heap: limit %d MB; want %d", got>>20, memoryLimit>>20)
	}
	if got := limitFor(150 << 20); got != 289<<20 {
		t.Errorf("a live heap of 150 MB: limit %d MB; want 289", got>>20)
	}
}
