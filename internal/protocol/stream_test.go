package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestStreamFramesRoundTrip(t *testing.T) {
	frames := []struct {
		kind FrameKind
		data []byte
	}{
		{FrameOutput, []byte("hello\x1b[31m\r\n")},
		{FrameInput, []byte("ls\r")},
		{FrameOutput, nil},
	}
	var wire bytes.Buffer
	for _, f := range frames {
		if err := WriteStreamFrame(&wire, f.kind, f.data); err != nil {
			t.Fatalf("write %d: %v", f.kind, err)
		}
	}
	var buf []byte
	for i, f := range frames {
		kind, payload, err := ReadStreamFrame(&wire, buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		buf = payload
		if kind != f.kind {
			t.Fatalf("frame %d kind %d, want %d", i, kind, f.kind)
		}
		if !bytes.Equal(payload, f.data) {
			t.Fatalf("frame %d payload %q, want %q", i, payload, f.data)
		}
	}
}

func TestStreamJSONEventsDecode(t *testing.T) {
	var wire bytes.Buffer
	snap := Snapshot{SessionID: "s", Cols: 80, Rows: 24, Title: "title", Progress: &ProgressReport{State: 3, Value: -1, RemainingMS: 500}}
	if err := WriteStreamJSON(&wire, FrameSnapshot, snap); err != nil {
		t.Fatal(err)
	}
	if err := WriteStreamJSON(&wire, FrameExited, Exited{SessionID: "s", ExitCode: 7}); err != nil {
		t.Fatal(err)
	}
	if err := WriteStreamJSON(&wire, FrameError, Error{Message: "gone"}); err != nil {
		t.Fatal(err)
	}

	var buf []byte
	kind, payload, err := ReadStreamFrame(&wire, buf)
	if err != nil {
		t.Fatal(err)
	}
	buf = payload
	ev := StreamEvent{Kind: kind, Data: payload}
	if err := DecodeStreamEvent(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Snapshot.Title != "title" || ev.Snapshot.Progress == nil || ev.Snapshot.Progress.State != 3 {
		t.Fatalf("snapshot %+v", ev.Snapshot)
	}

	kind, payload, err = ReadStreamFrame(&wire, buf)
	if err != nil {
		t.Fatal(err)
	}
	buf = payload
	ev = StreamEvent{Kind: kind, Data: payload}
	if err := DecodeStreamEvent(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Kind != FrameExited || ev.ExitCode != 7 {
		t.Fatalf("exited %+v", ev)
	}

	kind, payload, err = ReadStreamFrame(&wire, buf)
	if err != nil {
		t.Fatal(err)
	}
	ev = StreamEvent{Kind: kind, Data: payload}
	if err := DecodeStreamEvent(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Kind != FrameError || ev.Err != "gone" {
		t.Fatalf("error %+v", ev)
	}
}

func TestStreamResizeRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteStreamResize(&wire, 132, 43); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := ReadStreamFrame(&wire, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameResize {
		t.Fatalf("kind %d, want resize", kind)
	}
	cols, rows, ok := DecodeResize(payload)
	if !ok || cols != 132 || rows != 43 {
		t.Fatalf("resize %d x %d, ok=%v", cols, rows, ok)
	}
}

func TestStreamFrameRefusesOversize(t *testing.T) {
	big := make([]byte, MaxFrameSize+1)
	if err := WriteStreamFrame(io.Discard, FrameOutput, big); err == nil {
		t.Fatal("oversized frame written")
	}
}
