package protocol

// An attached session's stream carries envelopes as every stream does:
// Snapshot, Output and Exited toward the front-end, Input and Resize toward
// the daemon. Protobuf carries the bytes of Output and Input as they are,
// so the hot path needs no framing of its own.

// EventKind is what an attached session's stream delivered.
type EventKind byte

const (
	// EventSnapshot is a Snapshot with its Text left out. The screen
	// follows as EventOutput pieces, so a long scrollback never has to fit
	// in one frame.
	EventSnapshot EventKind = iota + 1
	// EventOutput is PTY bytes.
	EventOutput
	// EventExited is the program's end, with its exit code.
	EventExited
	// EventError is a failure that ended the stream.
	EventError
)

// StreamEvent is one thing an attached session's stream delivered.
type StreamEvent struct {
	Kind     EventKind
	Data     []byte   // EventOutput
	Snapshot Snapshot // EventSnapshot
	ExitCode int      // EventExited
	Err      string   // EventExited's Err, or EventError's message
}

// StreamEventOf reads an envelope from an attached session's stream into
// ev, reporting false for one that is none of the events.
func StreamEventOf(env *Envelope, ev *StreamEvent) (bool, error) {
	*ev = StreamEvent{}
	switch env.Type {
	case TypeSnapshot:
		ev.Kind = EventSnapshot
		return true, Decode(env, &ev.Snapshot)
	case TypeOutput:
		var out Output
		if err := Decode(env, &out); err != nil {
			return false, err
		}
		ev.Kind, ev.Data = EventOutput, out.Data
	case TypeExited:
		var ex Exited
		if err := Decode(env, &ex); err != nil {
			return false, err
		}
		ev.Kind, ev.ExitCode, ev.Err = EventExited, ex.ExitCode, ex.Err
	case TypeError:
		var e Error
		if err := Decode(env, &e); err != nil {
			return false, err
		}
		ev.Kind, ev.Err = EventError, e.Message
	default:
		return false, nil
	}
	return true, nil
}
