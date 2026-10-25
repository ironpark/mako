package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// An attached session stream switches to this compact framing once the Attach
// request has been read (Attach.Binary): PTY output is the hot path, and
// carrying it base64 inside JSON costs a third more bytes, an encode and a
// decode, and an allocation per frame. The request itself, and every other
// stream, keep the length-prefixed JSON envelope.
//
//	kind  1 byte
//	length 4 bytes, big-endian
//	payload
//
// The daemon-to-daemon leg stays JSON: the local daemon translates, so peers
// of either version can be attached through.
type FrameKind byte

const (
	// FrameSnapshot carries a Snapshot as JSON, with Text left out. The
	// screen itself follows as FrameOutput pieces, so a long scrollback
	// never has to fit in one frame.
	FrameSnapshot FrameKind = 1
	// FrameOutput carries PTY bytes toward the front-end.
	FrameOutput FrameKind = 2
	// FrameExited carries an Exited as JSON.
	FrameExited FrameKind = 3
	// FrameError carries an Error as JSON.
	FrameError FrameKind = 4
	// FrameInput carries raw bytes toward the session PTY.
	FrameInput FrameKind = 5
	// FrameResize carries the window size: cols then rows, big-endian uint16.
	FrameResize FrameKind = 6
)

// StreamEvent is one frame read from an attached session stream. Data is the
// payload: output or input bytes for those kinds, JSON for the rest. It is
// valid only until the next read into the same buffer.
type StreamEvent struct {
	Kind     FrameKind
	Data     []byte
	Snapshot Snapshot // FrameSnapshot
	ExitCode int      // FrameExited
	Err      string   // FrameExited's Err, or FrameError's message
}

// WriteStreamFrame writes one binary frame.
func WriteStreamFrame(w io.Writer, kind FrameKind, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(kind)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// WriteStreamJSON writes one frame whose payload is v as JSON.
func WriteStreamJSON(w io.Writer, kind FrameKind, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteStreamFrame(w, kind, body)
}

// ReadStreamFrame reads one binary frame into buf, growing it if needed, and
// returns the kind and the payload, which aliases buf.
func ReadStreamFrame(r io.Reader, buf []byte) (FrameKind, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[1:]))
	if n > MaxFrameSize {
		return 0, nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return FrameKind(hdr[0]), buf, nil
}

// DecodeStreamEvent fills in the fields a frame kind carries.
func DecodeStreamEvent(ev *StreamEvent) error {
	switch ev.Kind {
	case FrameSnapshot:
		var snap Snapshot
		if err := json.Unmarshal(ev.Data, &snap); err != nil {
			return err
		}
		ev.Snapshot = snap
	case FrameExited:
		var ex Exited
		if err := json.Unmarshal(ev.Data, &ex); err != nil {
			return err
		}
		ev.ExitCode, ev.Err = ex.ExitCode, ex.Err
	case FrameError:
		var e Error
		if err := json.Unmarshal(ev.Data, &e); err != nil {
			return err
		}
		ev.Err = e.Message
	}
	return nil
}

// WriteStreamResize writes a window size as a FrameResize.
func WriteStreamResize(w io.Writer, cols, rows uint16) error {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:], cols)
	binary.BigEndian.PutUint16(b[2:], rows)
	return WriteStreamFrame(w, FrameResize, b[:])
}

// DecodeResize reads a FrameResize payload.
func DecodeResize(p []byte) (cols, rows uint16, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(p[0:]), binary.BigEndian.Uint16(p[2:]), true
}
