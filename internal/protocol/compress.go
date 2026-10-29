package protocol

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

// A frame between daemons is compressed with zstd when that makes it
// smaller by enough: a file, a diff, a conversation, a screen. Its length
// then has packedBit set, and is the compressed length. A reader takes
// either. See docs/protocol.md.
const (
	packedBit = 1 << 31
	// packMin is the smallest envelope worth compressing: below it, the
	// work outweighs what it saves.
	packMin = 1 << 10
)

var (
	encoder = sync.OnceValue(func() *zstd.Encoder {
		e, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(false), zstd.WithLowerEncoderMem(true))
		return e
	})
	decoder = sync.OnceValue(func() *zstd.Decoder {
		d, _ := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(MaxFrameSize))
		return d
	})
)

// Compress is b compressed with zstd.
func Compress(b []byte) []byte {
	return encoder().EncodeAll(b, make([]byte, 0, len(b)/2))
}

// Decompress is what Compress compressed, refused past MaxFrameSize.
func Decompress(b []byte) ([]byte, error) {
	return decoder().DecodeAll(b, nil)
}

// packed is b compressed, or false when that does not save an eighth of it.
func packed(b []byte) ([]byte, bool) {
	if len(b) < packMin {
		return nil, false
	}
	out := Compress(b)
	if len(out) > len(b)-len(b)/8 {
		return nil, false
	}
	return out, true
}
