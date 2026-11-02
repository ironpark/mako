package protocol

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
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

// A reader takes the stream of a writer from before the window shrank,
// which refers as far back as 1 MiB.
func TestAStreamOfTheOlderWindowIsRead(t *testing.T) {
	p := NewPacker()
	p.enc, _ = zstd.NewWriter(&p.out, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(false),
		zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
	var u Unpacker
	first := bytes.Repeat([]byte("0123456789abcdef"), 40<<10) // 640 KiB, past the new window
	for i, piece := range [][]byte{first, first[:300<<10]} {
		body, err := p.pack(nil, piece)
		if err != nil {
			t.Fatal(err)
		}
		got, err := u.unpack(body)
		if err != nil || !bytes.Equal(got, piece) {
			t.Fatalf("piece %d: %d bytes, %v", i, len(got), err)
		}
	}
}
