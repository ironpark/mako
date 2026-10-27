package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Every Type constant names a body field, and every body field has one.
func TestEveryTypeHasABody(t *testing.T) {
	types := []Type{
		TypeHello, TypeWelcome, TypeError, TypeListNodes, TypeNodes, TypeGossip, TypeWatch, TypeChanged,
		TypeListSess, TypeSessions, TypeOpenSession, TypeSessionInfo, TypeCloseSess, TypeRenameSess, TypeAnswerApproval,
		TypeAttach, TypeInput, TypeOutput, TypeResize, TypeSnapshot, TypeExited,
		TypeListDir, TypeDir, TypeReadFile, TypeFile, TypeWriteFile, TypeFileOp, TypeSearch, TypeSearchResult, TypeStageFile, TypeStaged,
		TypeGitStatus, TypeGitChanges, TypeGitDiff, TypeDiff, TypeGitLog, TypeGitHistory, TypeGitShow,
		TypeGitCommitInfo, TypeGitRun, TypeGitResult, TypeReadChat, TypeChat,
	}
	for _, typ := range types {
		if _, ok := bodyFields[typ]; !ok {
			t.Errorf("no body field for %q", typ)
		}
	}
	if len(bodyFields) != len(types) {
		t.Errorf("%d body fields for %d types", len(bodyFields), len(types))
	}
}

// A message crosses the wire whole, and comes back as the type's message.
func TestAMessageRoundTrips(t *testing.T) {
	code := int64(3)
	sent := &SessionInfo{
		Id: "s", NodeId: "n", Agent: "claude", Command: []string{"claude", "-c"}, State: StateExited,
		Cols: 80, Rows: 24, StartedAt: timestamppb.Now(), ExitCode: &code,
		Approval: &Approval{Id: "a", Tool: "Bash", Since: timestamppb.Now()},
		Worktree: &Worktree{Repo: "/r", Branch: "b"},
	}
	env, err := Encode(TypeSessionInfo, "req-1", sent)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := WriteFrame(&wire, env); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != TypeSessionInfo || got.GetId() != "req-1" {
		t.Fatalf("read back as %q, id %q", got.Type(), got.GetId())
	}
	info, err := Body[*SessionInfo](got)
	if err != nil || !proto.Equal(info, sent) {
		t.Fatalf("got %v, %v; want %v", info, err, sent)
	}
	if _, err := Body[*Node](got); err == nil {
		t.Fatal("a session's info read as a node")
	}
	// Gossip and Nodes share a message, and keep their types apart.
	env, _ = Encode(TypeGossip, "", &Nodes{Nodes: []*Node{{Id: "x"}}})
	if env.Type() != TypeGossip {
		t.Fatalf("gossip encoded as %q", env.Type())
	}
}

// A message that is not the type's is refused, and nil sends it empty.
func TestEncodeChecksTheMessage(t *testing.T) {
	if _, err := Encode(TypeHello, "", &Welcome{}); err == nil || !strings.Contains(err.Error(), "Hello") {
		t.Fatalf("encode a welcome as a hello: %v", err)
	}
	if _, err := Encode("no_such_type", "", nil); err == nil {
		t.Fatal("encoded a type with no message")
	}
	env, err := Encode(TypeListNodes, "", nil)
	if err != nil || env.Type() != TypeListNodes {
		t.Fatalf("empty list_nodes: %v %q", err, env.Type())
	}
}

// A frame from a newer peer, whose body this build does not know, reads
// back with no type for the daemon to answer as unsupported.
func TestAnUnknownBodyReadsAsNoType(t *testing.T) {
	body := protowire.AppendTag(nil, 999, protowire.BytesType)
	body = protowire.AppendBytes(body, []byte("future"))
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	env, err := ReadFrame(bytes.NewReader(append(frame, body...)))
	if err != nil || env.Type() != "" {
		t.Fatalf("unknown body read as %q, %v", env.Type(), err)
	}
}

// A frame longer than the limit is refused before it is read.
func TestAnOversizeFrameIsRefused(t *testing.T) {
	frame := binary.BigEndian.AppendUint32(nil, MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(frame)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize frame: %v", err)
	}
	env, _ := Encode(TypeOutput, "", &Output{Data: make([]byte, MaxFrameSize)})
	if err := WriteFrame(&bytes.Buffer{}, env); err == nil {
		t.Fatal("wrote an oversize frame")
	}
}

// Output's bytes are carried as they are, without a JSON or base64 layer.
func TestOutputIsCarriedRaw(t *testing.T) {
	data := bytes.Repeat([]byte("\x1b[31mhello\r\n"), 1000)
	env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: data})
	var wire bytes.Buffer
	if err := WriteFrame(&wire, env); err != nil {
		t.Fatal(err)
	}
	if overhead := wire.Len() - len(data); overhead > 32 {
		t.Fatalf("%d bytes of output took %d on the wire", len(data), wire.Len())
	}
	got, _ := ReadFrame(&wire)
	ev, ok := StreamEventOf(got)
	if !ok || ev.Kind != EventOutput || !bytes.Equal(ev.Data, data) {
		t.Fatalf("output event %v %v", ok, ev)
	}
}
