package apigen

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// methodOpts are a method's options with permission perm, set as the
// unknown fields an unregistered extension leaves.
func methodOpts(perm string, unavailable bool) *descriptorpb.MethodOptions {
	o := &descriptorpb.MethodOptions{}
	var raw []byte
	if perm != "" {
		raw = protowire.AppendTag(raw, permissionField, protowire.BytesType)
		raw = protowire.AppendString(raw, perm)
	}
	if unavailable {
		raw = protowire.AppendTag(raw, unavailableField, protowire.VarintType)
		raw = protowire.AppendVarint(raw, 1)
	}
	o.ProtoReflect().SetUnknown(raw)
	return o
}

func scalarField(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(num), Type: typ.Enum(),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
}

func msgField(name string, num int32, typeName string) *descriptorpb.FieldDescriptorProto {
	f := scalarField(name, num, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	f.TypeName = proto.String(typeName)
	return f
}

// sample is a schema the generator takes: one service of one method.
func sample() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String("mako/plugin/v1/t.proto"), Package: proto.String(Package), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("PingRequest"), Field: []*descriptorpb.FieldDescriptorProto{scalarField("text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("PingResponse")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("PingService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name: proto.String("Ping"), InputType: proto.String(".mako.plugin.v1.PingRequest"),
				OutputType: proto.String(".mako.plugin.v1.PingResponse"), Options: methodOpts("none", false),
			}},
		}},
	}
}

func TestSampleGenerates(t *testing.T) {
	files, err := Generate([]*descriptorpb.FileDescriptorProto{sample()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if len(f.Content) == 0 {
			t.Errorf("%s is empty", f.Name)
		}
	}
}

// TestRefusesWhatWITCannotSay changes the sample in each way the subset
// leaves out, and expects the generator to refuse it, saying why.
func TestRefusesWhatWITCannotSay(t *testing.T) {
	req := func(f *descriptorpb.FileDescriptorProto) *descriptorpb.DescriptorProto { return f.MessageType[0] }
	meth := func(f *descriptorpb.FileDescriptorProto) *descriptorpb.MethodDescriptorProto {
		return f.Service[0].Method[0]
	}
	cases := []struct {
		name, want string
		change     func(*descriptorpb.FileDescriptorProto)
	}{
		{"no permission", "declares the permission", func(f *descriptorpb.FileDescriptorProto) { meth(f).Options = nil }},
		{"bad permission", "not a word", func(f *descriptorpb.FileDescriptorProto) { meth(f).Options = methodOpts("Net!", false) }},
		{"server streaming", "streaming", func(f *descriptorpb.FileDescriptorProto) { meth(f).ServerStreaming = proto.Bool(true) }},
		{"client streaming", "streaming", func(f *descriptorpb.FileDescriptorProto) { meth(f).ClientStreaming = proto.Bool(true) }},
		{"Any", "not of package", func(f *descriptorpb.FileDescriptorProto) {
			req(f).Field = append(req(f).Field, msgField("any", 2, ".google.protobuf.Any"))
		}},
		{"int64", "64-bit", func(f *descriptorpb.FileDescriptorProto) {
			req(f).Field = append(req(f).Field, scalarField("n", 2, descriptorpb.FieldDescriptorProto_TYPE_INT64))
		}},
		{"oneof", "oneofs", func(f *descriptorpb.FileDescriptorProto) {
			req(f).OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("o")}}
			req(f).Field[0].OneofIndex = proto.Int32(0)
		}},
		{"recursion", "holds itself", func(f *descriptorpb.FileDescriptorProto) {
			req(f).Field = append(req(f).Field, msgField("next", 2, ".mako.plugin.v1.PingRequest"))
		}},
		{"indirect recursion", "holds itself", func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType = append(f.MessageType, &descriptorpb.DescriptorProto{Name: proto.String("Node"),
				Field: []*descriptorpb.FieldDescriptorProto{msgField("back", 1, ".mako.plugin.v1.PingRequest")}})
			req(f).Field = append(req(f).Field, msgField("node", 2, ".mako.plugin.v1.Node"))
		}},
		{"nested type", "top level", func(f *descriptorpb.FileDescriptorProto) {
			req(f).NestedType = []*descriptorpb.DescriptorProto{{Name: proto.String("Inner")}}
		}},
		{"map of int keys", "keys must be strings", func(f *descriptorpb.FileDescriptorProto) {
			req(f).NestedType = []*descriptorpb.DescriptorProto{{Name: proto.String("MEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{scalarField("key", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32), scalarField("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}}}
			m := msgField("m", 2, ".mako.plugin.v1.PingRequest.MEntry")
			m.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			req(f).Field = append(req(f).Field, m)
		}},
		{"field of an empty message", "no empty record", func(f *descriptorpb.FileDescriptorProto) {
			req(f).Field = append(req(f).Field, msgField("e", 2, ".mako.plugin.v1.PingResponse"))
		}},
		{"service name", "<Group>Service", func(f *descriptorpb.FileDescriptorProto) { f.Service[0].Name = proto.String("Ping") }},
		{"not a WIT name", "not a WIT name", func(f *descriptorpb.FileDescriptorProto) {
			req(f).Field = append(req(f).Field, scalarField("text_", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING))
		}},
		{"proto2", "proto3", func(f *descriptorpb.FileDescriptorProto) { f.Syntax = proto.String("proto2") }},
		{"method of a type's name", "a method and a type it uses", func(f *descriptorpb.FileDescriptorProto) {
			meth(f).Name = proto.String("PingRequest")
		}},
		{"WIT name collision", "both", func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType = append(f.MessageType, &descriptorpb.DescriptorProto{Name: proto.String("PingREQUEST"),
				Field: []*descriptorpb.FieldDescriptorProto{scalarField("x", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := sample()
			c.change(f)
			_, err := Generate([]*descriptorpb.FileDescriptorProto{f})
			if err == nil {
				t.Fatalf("taken; want it refused for %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refused with %q; want %q", err, c.want)
			}
		})
	}
}

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{
		"GetItemRequest": "get-item-request", "Http": "http", "HTTPServer": "http-server", "Run2Fast": "run2-fast",
	} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q; want %q", in, got, want)
		}
	}
}

// A field spelled as one of WIT's keywords is escaped, as wasm-tools
// takes it: map among them, WIT's map type.
func TestKeywordsAreEscaped(t *testing.T) {
	f := sample()
	f.MessageType[0].Field = append(f.MessageType[0].Field, scalarField("map", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
		scalarField("type", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING))
	files, err := Generate([]*descriptorpb.FileDescriptorProto{f})
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range files {
		if strings.HasSuffix(out.Name, ".wit") {
			wit := string(out.Content)
			if !strings.Contains(wit, "%map: string") || !strings.Contains(wit, "%type: string") {
				t.Fatalf("keywords not escaped:\n%s", wit)
			}
		}
	}
}
