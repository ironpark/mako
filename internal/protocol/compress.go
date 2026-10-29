package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
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

// An attached session's output repeats itself: a spinner redrawn, a status
// line, the same colours and cursor moves. Each of those frames is too
// small to pack alone, but packed as pieces of one zstd stream, each
// costs only what is new since the frames before it. A frame so packed
// has streamedBit set in its length, and starts with its envelope's size
// as a uvarint; the rest is the stream's next piece, flushed so it can be
// read as it comes. Only a reader that asked for it gets one: see
// Attach.pack_stream.
const (
	streamedBit = 1 << 30
	// streamWindow is how far back a streamed frame may refer: some screens
	// of a large terminal, redrawn whole.
	streamWindow = 1 << 20
)

// A Packer packs the frames of one stream as one zstd stream. Its frames
// are written one at a time, as a stream's are.
type Packer struct {
	out bytes.Buffer
	enc *zstd.Encoder
}

// NewPacker starts a stream of packed frames.
func NewPacker() *Packer {
	p := &Packer{}
	p.enc, _ = zstd.NewWriter(&p.out, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(false),
		zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(streamWindow), zstd.WithLowerEncoderMem(true))
	return p
}

// pack appends b, packed as the stream's next piece, to dst.
func (p *Packer) pack(dst, b []byte) ([]byte, error) {
	p.out.Reset()
	if _, err := p.enc.Write(b); err != nil {
		return nil, err
	}
	if err := p.enc.Flush(); err != nil {
		return nil, err
	}
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, p.out.Bytes()...), nil
}

// An Unpacker reads the frames a Packer packed, in the order it packed
// them.
type Unpacker struct {
	in  bytes.Reader
	dec *zstd.Decoder
}

// unpack is the envelope a streamed frame's body b carries.
func (u *Unpacker) unpack(b []byte) ([]byte, error) {
	n, k := binary.Uvarint(b)
	if k <= 0 || n > MaxFrameSize {
		return nil, errors.New("a streamed frame's size is out of range")
	}
	if u.dec == nil {
		var err error
		u.dec, err = zstd.NewReader(&u.in, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(streamWindow),
			zstd.WithDecoderMaxMemory(MaxFrameSize), zstd.WithDecoderLowmem(true))
		if err != nil {
			return nil, err
		}
	}
	// The piece decodes to exactly the envelope, so reading that much
	// takes all of it and reads no further: the next piece is in the next
	// frame.
	u.in.Reset(b[k:])
	out := make([]byte, n)
	if _, err := io.ReadFull(u.dec, out); err != nil {
		return nil, err
	}
	if u.in.Len() != 0 {
		return nil, errors.New("a streamed frame holds more than its envelope")
	}
	return out, nil
}
