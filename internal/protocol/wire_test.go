package protocol

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// wireTypes is the Go type each message type carries.
var wireTypes = map[Type]any{
	TypeHello: Hello{}, TypeWelcome: Welcome{}, TypeError: Error{},
	TypeListNodes: struct{}{}, TypeNodes: Nodes{}, TypeGossip: Nodes{},
	TypeListSess: ListSessions{}, TypeSessions: Sessions{}, TypeOpenSession: OpenSession{},
	TypeSessionInfo: SessionInfo{}, TypeCloseSess: CloseSession{}, TypeRenameSess: RenameSession{},
	TypeAnswerApproval: AnswerApproval{},
	TypeAttach:         Attach{}, TypeInput: Input{}, TypeOutput: Output{}, TypeResize: Resize{},
	TypeSnapshot: Snapshot{}, TypeExited: Exited{},
	TypeListDir: ListDir{}, TypeDir: Dir{}, TypeReadFile: ReadFile{}, TypeFile: File{},
	TypeWriteFile: WriteFile{}, TypeFileOp: FileOp{}, TypeSearch: Search{}, TypeSearchResult: SearchResult{},
	TypeGitStatus: GitStatus{}, TypeGitChanges: GitChanges{}, TypeGitDiff: GitDiff{}, TypeDiff: Diff{},
	TypeGitLog: GitLog{}, TypeGitHistory: GitHistory{}, TypeGitShow: GitShow{},
	TypeGitCommitInfo: GitCommitInfo{}, TypeGitRun: GitRun{}, TypeGitResult: GitResult{},
	TypeReadChat: ReadChat{}, TypeChat: Chat{},
}

// The schema has a message for every type, and nothing else.
func TestEveryTypeHasAMessage(t *testing.T) {
	var missing, extra []string
	for typ := range wireTypes {
		if _, ok := bodyFields[typ]; !ok {
			missing = append(missing, string(typ))
		}
	}
	for typ := range bodyFields {
		if _, ok := wireTypes[typ]; !ok {
			extra = append(extra, string(typ))
		}
	}
	if len(missing)+len(extra) > 0 {
		t.Fatalf("types without a message %v; messages without a type here %v", missing, extra)
	}
}

// Every message crosses the wire with every field of its Go type intact: a
// field the schema lacks fails here.
func TestEveryMessageRoundTrips(t *testing.T) {
	for typ, zero := range wireTypes {
		v := reflect.New(reflect.TypeOf(zero))
		fill(v.Elem(), 1)
		var wire bytes.Buffer
		env, err := Encode(typ, "req-1", v.Interface())
		if err != nil {
			t.Errorf("%s: %v", typ, err)
			continue
		}
		if err := WriteFrame(&wire, env); err != nil {
			t.Errorf("%s: write: %v", typ, err)
			continue
		}
		got, err := ReadFrame(&wire)
		if err != nil {
			t.Errorf("%s: read: %v", typ, err)
			continue
		}
		if got.Type != typ || got.ID != "req-1" {
			t.Errorf("%s: read back as %q, id %q", typ, got.Type, got.ID)
			continue
		}
		out := reflect.New(reflect.TypeOf(zero))
		if err := Decode(got, out.Interface()); err != nil {
			t.Errorf("%s: decode: %v", typ, err)
			continue
		}
		if !reflect.DeepEqual(out.Elem().Interface(), v.Elem().Interface()) {
			t.Errorf("%s changed on the wire:\n sent %+v\n got  %+v", typ, v.Elem().Interface(), out.Elem().Interface())
		}
	}
}

// fill sets every field of v to a value that is not its zero, so a field
// the wire drops shows.
func fill(v reflect.Value, seed int) {
	switch {
	case v.Type() == timeType:
		v.Set(reflect.ValueOf(time.Unix(1_700_000_000+int64(seed), 123_456_789)))
		return
	case v.Type() == reflect.TypeFor[[]byte]():
		v.SetBytes([]byte{0, 1, 0xff, byte(seed)})
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), seed+i)
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), seed)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			fill(s.Index(i), seed+i+1)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, seed)
		fill(e, seed+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString("s" + strings.Repeat("x", seed%5) + "é")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(-seed - 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(seed + 1))
	case reflect.Float64:
		v.SetFloat(float64(seed) + .25)
	}
}

// A Go field the message lacks is an error, not a silent drop, and so is a
// message field the Go type lacks.
func TestAFieldTheSchemaLacksFails(t *testing.T) {
	type helloPlus struct {
		Name  string `json:"name"`
		Extra string `json:"extra"`
	}
	if _, err := Encode(TypeHello, "", helloPlus{Name: "a", Extra: "b"}); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("encode with an unknown field: %v", err)
	}
	type helloLess struct {
		Name string `json:"name"`
	}
	if _, err := Encode(TypeHello, "", helloLess{Name: "a"}); err == nil || !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("encode with a field missing: %v", err)
	}
	if _, err := Encode("no_such_type", "", nil); err == nil {
		t.Fatal("encoded a type with no message")
	}
}

// A frame from a newer peer, whose body this build does not know, reads
// back with no type for the daemon to answer as unsupported.
func TestAnUnknownBodyReadsAsNoType(t *testing.T) {
	body := protowire.AppendTag(nil, 999, protowire.BytesType)
	body = protowire.AppendBytes(body, []byte("future"))
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	env, err := ReadFrame(bytes.NewReader(append(frame, body...)))
	if err != nil || env.Type != "" {
		t.Fatalf("unknown body read as %q, %v", env.Type, err)
	}
}

// A frame longer than the limit is refused before it is read.
func TestAnOversizeFrameIsRefused(t *testing.T) {
	frame := binary.BigEndian.AppendUint32(nil, MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(frame)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize frame: %v", err)
	}
	env, _ := Encode(TypeOutput, "", Output{Data: make([]byte, MaxFrameSize)})
	if err := WriteFrame(&bytes.Buffer{}, env); err == nil {
		t.Fatal("wrote an oversize frame")
	}
}

// Output's bytes are carried as they are, without a JSON or base64 layer.
func TestOutputIsCarriedRaw(t *testing.T) {
	data := bytes.Repeat([]byte("\x1b[31mhello\r\n"), 1000)
	env, _ := Encode(TypeOutput, "", Output{SessionID: "s", Data: data})
	var wire bytes.Buffer
	if err := WriteFrame(&wire, env); err != nil {
		t.Fatal(err)
	}
	if overhead := wire.Len() - len(data); overhead > 32 {
		t.Fatalf("%d bytes of output took %d on the wire", len(data), wire.Len())
	}
	got, _ := ReadFrame(&wire)
	var ev StreamEvent
	if ok, err := StreamEventOf(got, &ev); !ok || err != nil || ev.Kind != EventOutput || !bytes.Equal(ev.Data, data) {
		t.Fatalf("output event %v %v %d", ok, err, len(ev.Data))
	}
	if !slices.Equal(ev.Data[:5], data[:5]) {
		t.Fatal("output changed")
	}
}
