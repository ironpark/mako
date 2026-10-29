package protocol

import (
	"bytes"
	"runtime"
	"testing"
)

func TestCompressingALargeFrameKeepsLittleAfter(t *testing.T) {
	// A conversation of half a megabyte: more than a block, so the encoder
	// keeps a history between them.
	frame := bytes.Repeat([]byte("the agent read a file and wrote a reply\n"), 512<<10/40)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runtime.GOMAXPROCS(0) {
		out := Compress(frame)
		if got, err := Decompress(out); err != nil || !bytes.Equal(got, frame) {
			t.Fatalf("Decompress(Compress(frame)) = %d bytes, %v; want the frame back", len(got), err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if kept := int64(after.HeapAlloc) - int64(before.HeapAlloc); kept > 16<<20 {
		t.Errorf("compressing a 512 KiB frame keeps %d KiB after; want at most 16384", kept>>10)
	}
}
