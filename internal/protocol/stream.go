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
	Data     []byte    // EventOutput
	Snapshot *Snapshot // EventSnapshot
	ExitCode int       // EventExited
	Err      string    // EventExited's Err, or EventError's message
}

// StreamEventOf reads an envelope from an attached session's stream,
// reporting false for one that is none of the events.
func StreamEventOf(env *Envelope) (*StreamEvent, bool) {
	switch b := env.GetBody().(type) {
	case *Envelope_Snapshot:
		return &StreamEvent{Kind: EventSnapshot, Snapshot: b.Snapshot}, true
	case *Envelope_Output:
		return &StreamEvent{Kind: EventOutput, Data: b.Output.GetData()}, true
	case *Envelope_Exited:
		return &StreamEvent{Kind: EventExited, ExitCode: int(b.Exited.GetExitCode()), Err: b.Exited.GetErr()}, true
	case *Envelope_Error:
		return &StreamEvent{Kind: EventError, Err: b.Error.GetMessage()}, true
	}
	return nil, false
}
