package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Every Type constant names a body field, and every body field has one.
func TestEveryTypeHasABody(t *testing.T) {
	types := []Type{
		TypeHello, TypeWelcome, TypeError, TypeListNodes, TypeNodes, TypeWatch, TypeChanged, TypeAccess, TypeAccessInfo, TypeGetWorkspace, TypeWorkspace,
		TypeListSess, TypeSessions, TypeOpenSession, TypeSessionInfo, TypeCloseSess, TypeRenameSess, TypeAnswerApproval, TypeSend,
		TypeAttach, TypeInput, TypeOutput, TypeResize, TypeSnapshot, TypeExited,
		TypeListDir, TypeDir, TypeReadFile, TypeFile, TypeWriteFile, TypeFileOp, TypeSearch, TypeSearchResult, TypeStageFile, TypeStaged, TypeTunnel, TypeTunneled,
		TypeGitStatus, TypeGitChanges, TypeGitDiff, TypeDiff, TypeGitLog, TypeGitHistory, TypeGitShow,
		TypeGitCommitInfo, TypeGitRun, TypeGitResult, TypeReadChat, TypeChat,
		TypeForgeList, TypeForgeItems, TypeForgeView, TypeForgeDetail, TypeForgeRun,
		TypeListDevices, TypeDevices, TypeBorrow, TypeLease, TypeGiveBack, TypeListLeases, TypeLeases, TypeClaim, TypeClaimed, TypeDeviceConn, TypeDeviceListen, TypeDeviceAccept,
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
	if err := WriteFrame(&wire, env, false); err != nil {
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
	if err := WriteFrame(&bytes.Buffer{}, env, false); err == nil {
		t.Fatal("wrote an oversize frame")
	}
}

// Output's bytes are carried as they are, without a JSON or base64 layer.
func TestOutputIsCarriedRaw(t *testing.T) {
	data := bytes.Repeat([]byte("\x1b[31mhello\r\n"), 1000)
	env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: data})
	var wire bytes.Buffer
	if err := WriteFrame(&wire, env, false); err != nil {
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

// A frame between daemons that compresses well goes compressed, and reads
// back the same; one that does not, and a small one, go as they are.
func TestAFrameGoesPackedWhenThatSaves(t *testing.T) {
	text := []byte(strings.Repeat("func main() { fmt.Println(\"hello\") }\n", 400))
	noise := make([]byte, 16<<10)
	rand.Read(noise)
	for _, c := range []struct {
		name   string
		data   []byte
		packed bool
	}{
		{"text", text, true},
		{"small", []byte("hi"), false},
		{"noise", noise, false},
	} {
		env, _ := Encode(TypeFile, "", &File{Path: "/f", Data: c.data})
		var wire bytes.Buffer
		if err := WriteFrame(&wire, env, true); err != nil {
			t.Fatal(err)
		}
		if packed := binary.BigEndian.Uint32(wire.Bytes())&packedBit != 0; packed != c.packed {
			t.Errorf("%s: packed %v, want %v", c.name, packed, c.packed)
		}
		got, err := ReadFrame(&wire)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if f := got.GetFile(); !bytes.Equal(f.GetData(), c.data) {
			t.Errorf("%s: read back %d bytes, want %d", c.name, len(f.GetData()), len(c.data))
		}
	}
}

// A compressed frame that would grow past the limit is refused.
func TestAPackedFrameIsHeldToTheLimit(t *testing.T) {
	big := Compress(make([]byte, MaxFrameSize+1))
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(big))|packedBit)
	if _, err := ReadFrame(bytes.NewReader(append(frame, big...))); err == nil {
		t.Fatal("a frame unpacking past the limit was read")
	}
}

// Frames copied for a front-end come plain, whichever form they came in.
func TestUnpackedFramesComePlain(t *testing.T) {
	text := []byte(strings.Repeat("output of a session\r\n", 500))
	var wire bytes.Buffer
	for _, pack := range []bool{true, false, true} {
		env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: text})
		if err := WriteFrame(&wire, env, pack); err != nil {
			t.Fatal(err)
		}
	}
	var plain bytes.Buffer
	if err := UnpackFrames(&plain, &wire); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if binary.BigEndian.Uint32(plain.Bytes())&packedBit != 0 {
			t.Fatalf("frame %d came packed", i)
		}
		env, err := ReadFrame(&plain)
		if err != nil || !bytes.Equal(env.GetOutput().GetData(), text) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if plain.Len() != 0 {
		t.Fatalf("%d bytes left over", plain.Len())
	}
}

// Frames streamed to a daemon that asked for them come plain to its
// front-end, between frames that came packed alone or plain, the large
// and the empty as well as the small.
func TestStreamedFramesComePlain(t *testing.T) {
	large := make([]byte, 600<<10) // spans several of zstd's blocks
	rand.Read(large[:len(large)/2])
	outputs := [][]byte{[]byte("a"), large, nil, []byte(strings.Repeat("\x1b[2K\rworking\r\n", 300)), []byte("b")}
	p := NewPacker()
	var wire bytes.Buffer
	for i, data := range outputs {
		env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: data})
		var err error
		if i == 2 {
			err = WriteFrame(&wire, env, true) // a plain frame between
		} else {
			err = WriteStreamed(&wire, env, p)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadFrame(bytes.NewReader(wire.Bytes())); err == nil {
		t.Fatal("a streamed frame was read without its stream")
	}
	var plain bytes.Buffer
	if err := UnpackFrames(&plain, &wire); err != nil {
		t.Fatal(err)
	}
	for i, data := range outputs {
		env, err := ReadFrame(&plain)
		if err != nil || !bytes.Equal(env.GetOutput().GetData(), data) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if plain.Len() != 0 {
		t.Fatalf("%d bytes left over", plain.Len())
	}
}

// A streamed frame that says it holds more than it does, or holds more
// than it says, is refused.
func TestAStreamedFrameHoldsItsEnvelope(t *testing.T) {
	env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: []byte("hello")})
	plain, _ := proto.Marshal(env)
	short, _ := NewPacker().pack(nil, plain)
	_, k := binary.Uvarint(short)
	short = append(binary.AppendUvarint(nil, uint64(len(plain)+1)), short[k:]...)
	p := NewPacker()
	long, _ := p.pack(nil, plain)
	more, _ := p.pack(nil, plain)
	_, k = binary.Uvarint(more)
	long = append(long, more[k:]...)
	for name, body := range map[string][]byte{"short": short, "long": long} {
		frame := binary.BigEndian.AppendUint32(nil, uint32(len(body))|streamedBit)
		if err := UnpackFrames(&bytes.Buffer{}, bytes.NewReader(append(frame, body...))); err == nil {
			t.Errorf("a %s frame was read", name)
		}
	}
}

// spinner is output as an agent's terminal prints it while it works: the
// lines below its reply redrawn every tick, a line of reply now and then.
func spinner(ticks int) [][]byte {
	glyphs := []string{"·", "✢", "✳", "✶", "✻", "✽"}
	var out [][]byte
	for i := range ticks {
		var b strings.Builder
		b.WriteString("\x1b[?2026h\x1b[4A\r")
		if i%25 == 0 {
			fmt.Fprintf(&b, "\x1b[2K\x1b[38;2;215;119;87m⏺\x1b[39m Reading internal/session/subscription.go, line %d of the pump\r\n", i)
		}
		fmt.Fprintf(&b, "\x1b[2K\x1b[38;2;215;119;87m%s\x1b[39m \x1b[38;2;235;159;127mPondering…\x1b[39m \x1b[2m(%ds · ↓ %.1fk tokens · esc to interrupt)\x1b[22m\r\n",
			glyphs[i%len(glyphs)], i/10, float64(i)*0.3)
		b.WriteString("\x1b[2K\r\n\x1b[2K\x1b[38;2;136;136;136m╭──────────────────────────────────────────────────────────────╮\x1b[39m\r\n")
		b.WriteString("\x1b[2K\x1b[38;2;136;136;136m│\x1b[39m > \x1b[7m \x1b[27m                                                          \x1b[38;2;136;136;136m│\x1b[39m\r\n")
		b.WriteString("\x1b[?2026l")
		out = append(out, []byte(b.String()))
	}
	return out
}

// Streamed, an agent's redrawn screen costs a fraction of what it costs in
// frames packed alone.
func TestStreamingShrinksTerminalOutput(t *testing.T) {
	var raw, alone, streamed bytes.Buffer
	p := NewPacker()
	for _, data := range spinner(500) {
		env, _ := Encode(TypeOutput, "", &Output{SessionId: "0f8c2a51-6f4e-4d0e-9a8b-2a9f3c1d7e66", Data: data})
		_ = WriteFrame(&raw, env, false)
		_ = WriteFrame(&alone, env, true)
		_ = WriteStreamed(&streamed, env, p)
	}
	t.Logf("500 ticks: %d bytes plain, %d packed alone, %d streamed", raw.Len(), alone.Len(), streamed.Len())
	if streamed.Len()*4 > alone.Len() {
		t.Fatalf("streamed %d bytes, packed alone %d", streamed.Len(), alone.Len())
	}
}

func BenchmarkWriteStreamed(b *testing.B) {
	ticks := spinner(500)
	p := NewPacker()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		env, _ := Encode(TypeOutput, "", &Output{SessionId: "s", Data: ticks[i%len(ticks)]})
		_ = WriteStreamed(io.Discard, env, p)
	}
}
