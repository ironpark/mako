package main

import "testing"

func TestTheLimitKeepsClearOfTheLiveHeap(t *testing.T) {
	if got := limitFor(60<<20, 0); got != memoryLimit {
		t.Errorf("a small live heap: limit %d MB; want %d", got>>20, memoryLimit>>20)
	}
	if got := limitFor(150<<20, 0); got != 289<<20 {
		t.Errorf("a live heap of 150 MB: limit %d MB; want 289", got>>20)
	}
}

// After the live heap fell from 1150 MB to 515, the runtime still held
// 1145 MB it could not return; the limit must keep above that, or the
// collector runs back to back.
func TestTheLimitKeepsClearOfWhatTheRuntimeCannotReturn(t *testing.T) {
	const mb = 1 << 20
	total, released, free, objects := uint64(2043*mb), uint64(898*mb), uint64(169*mb), uint64(590*mb)
	other := besidesHeap(total, released, free, objects)
	if other != 386*mb {
		t.Fatalf("held besides the heap: %d MB; want 386", other/mb)
	}
	held := int64(total - released)
	if got := limitFor(590*mb, other); got <= held {
		t.Errorf("limit %d MB is under the %d MB the runtime holds", got/mb, held/mb)
	}
}

func TestBesidesHeapNeverUnderflows(t *testing.T) {
	if got := besidesHeap(100, 50, 40, 30); got != 0 {
		t.Errorf("got %d; want 0", got)
	}
}
