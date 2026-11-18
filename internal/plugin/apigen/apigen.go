// Package apigen generates what the plugin Host API is made of from its
// schema, proto/mako/plugin/v1, the one place it is defined: the daemon's
// dispatcher, which checks each call's permission before it reads the
// call (dispatch_gen.go); the JavaScript bindings a plugin's runtime
// loads (mako.js) and their types (mako.d.ts); and the WIT a wasm
// component runtime will import (mako.wit). It runs as a protoc plugin
// under buf (protoc-gen-makoplugin, `make proto`), and keeps the schema
// it read, comments and all, as hostapi.binpb, from which a test
// generates again and fails on any difference.
//
// The schema goes one way, proto to WIT, and the generator takes only
// what WIT can say as well, refusing the rest with the reason:
//
//   - scalars string, bool, int32, uint32, sint32, fixed32, sfixed32,
//     float, double and bytes; not 64-bit integers, whose JSON form is a
//     string;
//   - messages and enums of the package, declared at its top level, none
//     holding itself however far down (WIT types cannot recurse);
//   - repeated fields, maps with string keys (WIT has no maps: they become
//     lists of pairs), and proto3 optional fields;
//   - no oneofs, groups, extensions, or types from other packages, such as
//     google.protobuf.Any or Timestamp;
//   - unary methods only, each with a permission (options.proto), and
//     names that stay distinct as WIT's kebab case, a method's from the
//     types its interface uses too.
//
// The WIT written was checked once with wasm-tools 1.263 (component wit,
// and a component made of a dummy module of the world, validated), which
// found two things a schema could do that it refuses and this now does
// too: a name spelled as WIT's keyword map unescaped, and a method of the
// name of a type it uses.
package apigen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Package is the proto package the Host API is in.
const Package = "mako.plugin.v1"

// OutDir is where the generated files go, from the module's root.
const OutDir = "internal/plugin/hostapi/"

// The fields of options.proto's extensions of MethodOptions.
const (
	permissionField  = 51001
	unavailableField = 51002
)

// PermNone is the permission of a method every plugin may call.
const PermNone = "none"

// File is a generated file: its path from the module's root, and its
// content.
type File struct {
	Name    string
	Content []byte
}

// api is the schema as the generators read it.
type api struct {
	services []*service
	messages []*message
	enums    []*enum
	byName   map[string]*message // by full name, with the leading dot
	enumsBy  map[string]*enum
}

type service struct {
	name    string // LogService
	short   string // Log
	js      string // log
	doc     string
	methods []*method
}

type method struct {
	svc         *service
	name        string // GetItem
	js          string // getItem
	doc         string
	perm        string
	unavailable bool
	in, out     *message
}

// key is the method's name as the bindings call it: storage.getItem.
func (m *method) key() string { return m.svc.js + "." + m.js }

type message struct {
	name   string
	doc    string
	fields []*field
}

type enum struct {
	name   string
	doc    string
	values []string
}

type field struct {
	name     string // node_id
	json     string // nodeId
	doc      string
	scalar   string // the proto scalar's name, "" for a message or enum
	msg      *message
	enum     *enum
	repeated bool
	optional bool
	mapVal   *field // a map's value; keys are strings
}

// Generate makes the generated files from the schema's files, those of
// Package among files being the API's and the rest what they import.
func Generate(files []*descriptorpb.FileDescriptorProto) ([]File, error) {
	a, err := read(files)
	if err != nil {
		return nil, err
	}
	set := &descriptorpb.FileDescriptorSet{}
	for _, f := range files {
		if f.GetPackage() == Package {
			set.File = append(set.File, f)
		}
	}
	binpb, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
	if err != nil {
		return nil, err
	}
	return []File{
		{OutDir + "dispatch_gen.go", genGo(a)},
		{OutDir + "mako.js", genJS(a)},
		{OutDir + "mako.d.ts", genDTS(a)},
		{OutDir + "mako.wit", genWIT(a)},
		{OutDir + "hostapi.binpb", binpb},
	}, nil
}

// read takes the API's files apart, refusing what is outside the subset.
func read(files []*descriptorpb.FileDescriptorProto) (*api, error) {
	a := &api{byName: map[string]*message{}, enumsBy: map[string]*enum{}}
	var mine []*descriptorpb.FileDescriptorProto
	for _, f := range files {
		if f.GetPackage() == Package {
			mine = append(mine, f)
		}
	}
	if len(mine) == 0 {
		return nil, fmt.Errorf("no file of package %s", Package)
	}
	// Types first, so that fields and methods find them in any order.
	for _, f := range mine {
		if f.GetSyntax() != "proto3" {
			return nil, fmt.Errorf("%s: only proto3 is taken", f.GetName())
		}
		docs := comments(f)
		for i, m := range f.GetMessageType() {
			nested := len(m.GetEnumType()) > 0
			for _, n := range m.GetNestedType() {
				nested = nested || !n.GetOptions().GetMapEntry() // a map's entries are its own
			}
			if nested {
				return nil, fmt.Errorf("%s: declare types at the package's top level, not nested in %s", f.GetName(), m.GetName())
			}
			if len(m.GetExtension()) > 0 || len(m.GetExtensionRange()) > 0 {
				return nil, fmt.Errorf("%s: extensions are not taken", m.GetName())
			}
			msg := &message{name: m.GetName(), doc: docs[path(4, i)]}
			a.messages = append(a.messages, msg)
			a.byName["."+Package+"."+m.GetName()] = msg
		}
		for i, e := range f.GetEnumType() {
			en := &enum{name: e.GetName(), doc: docs[path(5, i)]}
			for _, v := range e.GetValue() {
				en.values = append(en.values, v.GetName())
			}
			a.enums = append(a.enums, en)
			a.enumsBy["."+Package+"."+e.GetName()] = en
		}
	}
	for _, f := range mine {
		docs := comments(f)
		for i, m := range f.GetMessageType() {
			msg := a.byName["."+Package+"."+m.GetName()]
			for j, fd := range m.GetField() {
				fl, err := a.readField(m, fd, docs[path(4, i, 2, j)])
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", m.GetName(), fd.GetName(), err)
				}
				msg.fields = append(msg.fields, fl)
			}
		}
		for i, s := range f.GetService() {
			svc, err := a.readService(s, docs, i)
			if err != nil {
				return nil, err
			}
			a.services = append(a.services, svc)
		}
		if len(f.GetExtension()) > 0 && !onlyMethodOptions(f) {
			return nil, fmt.Errorf("%s: extensions are taken only of MethodOptions, as options.proto declares", f.GetName())
		}
	}
	if err := a.check(); err != nil {
		return nil, err
	}
	return a, nil
}

func onlyMethodOptions(f *descriptorpb.FileDescriptorProto) bool {
	for _, x := range f.GetExtension() {
		if x.GetExtendee() != ".google.protobuf.MethodOptions" {
			return false
		}
	}
	return true
}

// scalars are the proto scalar types taken, by descriptor type.
var scalars = map[descriptorpb.FieldDescriptorProto_Type]string{
	descriptorpb.FieldDescriptorProto_TYPE_STRING:   "string",
	descriptorpb.FieldDescriptorProto_TYPE_BOOL:     "bool",
	descriptorpb.FieldDescriptorProto_TYPE_INT32:    "int32",
	descriptorpb.FieldDescriptorProto_TYPE_SINT32:   "sint32",
	descriptorpb.FieldDescriptorProto_TYPE_SFIXED32: "sfixed32",
	descriptorpb.FieldDescriptorProto_TYPE_UINT32:   "uint32",
	descriptorpb.FieldDescriptorProto_TYPE_FIXED32:  "fixed32",
	descriptorpb.FieldDescriptorProto_TYPE_FLOAT:    "float",
	descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:   "double",
	descriptorpb.FieldDescriptorProto_TYPE_BYTES:    "bytes",
}

func (a *api) readField(m *descriptorpb.DescriptorProto, fd *descriptorpb.FieldDescriptorProto, doc string) (*field, error) {
	fl := &field{name: fd.GetName(), json: fd.GetJsonName(), doc: doc}
	if fl.json == "" {
		fl.json = lowerCamel(snakeToCamel(fd.GetName()))
	}
	if fd.OneofIndex != nil && !fd.GetProto3Optional() {
		return nil, fmt.Errorf("oneofs are not taken; WIT would need a variant")
	}
	fl.optional = fd.GetProto3Optional()
	fl.repeated = fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	switch fd.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_INT64, descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64, descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64:
		return nil, fmt.Errorf("64-bit integers are not taken: their JSON form is a string; use int32, uint32 or double")
	case descriptorpb.FieldDescriptorProto_TYPE_GROUP:
		return nil, fmt.Errorf("groups are not taken")
	case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		e, ok := a.enumsBy[fd.GetTypeName()]
		if !ok {
			return nil, fmt.Errorf("type %s is not of package %s", fd.GetTypeName(), Package)
		}
		fl.enum = e
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:
		if entry := mapEntry(m, fd); entry != nil {
			k, v := entry.GetField()[0], entry.GetField()[1]
			if k.GetType() != descriptorpb.FieldDescriptorProto_TYPE_STRING {
				return nil, fmt.Errorf("a map's keys must be strings")
			}
			val, err := a.readField(entry, v, "")
			if err != nil {
				return nil, fmt.Errorf("map value: %w", err)
			}
			if val.mapVal != nil {
				return nil, fmt.Errorf("maps of maps are not taken")
			}
			fl.repeated, fl.mapVal = false, val
			return fl, nil
		}
		msg, ok := a.byName[fd.GetTypeName()]
		if !ok {
			return nil, fmt.Errorf("type %s is not of package %s (well-known types such as Any are not taken)", fd.GetTypeName(), Package)
		}
		fl.msg = msg
	default:
		s, ok := scalars[fd.GetType()]
		if !ok {
			return nil, fmt.Errorf("type %v is not taken", fd.GetType())
		}
		fl.scalar = s
	}
	return fl, nil
}

// mapEntry is the entry type of a map field, nil for any other.
func mapEntry(m *descriptorpb.DescriptorProto, fd *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
	if fd.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		return nil
	}
	for _, n := range m.GetNestedType() {
		if n.GetOptions().GetMapEntry() && strings.HasSuffix(fd.GetTypeName(), "."+n.GetName()) {
			return n
		}
	}
	return nil
}

func (a *api) readService(s *descriptorpb.ServiceDescriptorProto, docs map[string]string, i int) (*service, error) {
	if !strings.HasSuffix(s.GetName(), "Service") || s.GetName() == "Service" {
		return nil, fmt.Errorf("service %s: a service is named <Group>Service", s.GetName())
	}
	short := strings.TrimSuffix(s.GetName(), "Service")
	svc := &service{name: s.GetName(), short: short, js: lowerCamel(short), doc: docs[path(6, i)]}
	if len(s.GetMethod()) == 0 {
		return nil, fmt.Errorf("service %s has no methods", s.GetName())
	}
	for j, md := range s.GetMethod() {
		where := s.GetName() + "." + md.GetName()
		if md.GetClientStreaming() || md.GetServerStreaming() {
			return nil, fmt.Errorf("%s: streaming methods are not taken", where)
		}
		perm, unavailable := methodOptions(md.GetOptions())
		if perm == "" {
			return nil, fmt.Errorf("%s: every method declares the permission it takes: option (permission) = \"none\" for none", where)
		}
		if !validPerm(perm) {
			return nil, fmt.Errorf("%s: permission %q is not a word or words joined by colons", where, perm)
		}
		in, ok := a.byName[md.GetInputType()]
		if !ok {
			return nil, fmt.Errorf("%s: request %s is not of package %s", where, md.GetInputType(), Package)
		}
		out, ok := a.byName[md.GetOutputType()]
		if !ok {
			return nil, fmt.Errorf("%s: response %s is not of package %s", where, md.GetOutputType(), Package)
		}
		svc.methods = append(svc.methods, &method{svc: svc, name: md.GetName(), js: lowerCamel(md.GetName()),
			doc: docs[path(6, i, 2, j)], perm: perm, unavailable: unavailable, in: in, out: out})
	}
	return svc, nil
}

// methodOptions reads options.proto's options of a method by their field
// numbers, whether the options were read with the extensions known or
// left as unknown fields.
func methodOptions(o *descriptorpb.MethodOptions) (perm string, unavailable bool) {
	if o == nil {
		return "", false
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(o)
	if err != nil {
		return "", false
	}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return perm, unavailable
		}
		b = b[n:]
		switch {
		case num == permissionField && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return perm, unavailable
			}
			perm, n = string(v), m
		case num == unavailableField && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return perm, unavailable
			}
			unavailable, n = v != 0, m
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return perm, unavailable
			}
		}
		b = b[n:]
	}
	return perm, unavailable
}

// validPerm says a permission is none, or lowercase words joined by
// colons, as manifests name them.
func validPerm(p string) bool {
	if p == "" {
		return false
	}
	for _, w := range strings.Split(p, ":") {
		if w == "" {
			return false
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

// check refuses recursive types and names that collide as WIT spells
// them.
func (a *api) check() error {
	for _, m := range a.messages {
		if err := a.noCycle(m, nil); err != nil {
			return err
		}
		for _, f := range m.fields {
			t := f
			if f.mapVal != nil {
				t = f.mapVal
			}
			if t.msg != nil && len(t.msg.fields) == 0 {
				return fmt.Errorf("%s.%s: %s has no fields, and WIT has no empty record", m.name, f.name, t.msg.name)
			}
		}
	}
	seen := map[string]string{}
	claim := func(wit, what string) error {
		if other, ok := seen[wit]; ok {
			return fmt.Errorf("%s and %s are both %q in WIT", other, what, wit)
		}
		seen[wit] = what
		return nil
	}
	for _, m := range a.messages {
		if !validKebab(kebab(m.name)) {
			return fmt.Errorf("%s: not a WIT name as %q", m.name, kebab(m.name))
		}
		if err := claim(kebab(m.name), m.name); err != nil {
			return err
		}
		fields := map[string]string{}
		for _, f := range m.fields {
			k := snakeToKebab(f.name)
			if !validKebab(k) {
				return fmt.Errorf("%s.%s: not a WIT name as %q", m.name, f.name, k)
			}
			if other, ok := fields[k]; ok {
				return fmt.Errorf("%s: %s and %s are both %q in WIT", m.name, other, f.name, k)
			}
			fields[k] = f.name
		}
	}
	for _, e := range a.enums {
		if err := claim(kebab(e.name), e.name); err != nil {
			return err
		}
	}
	groups := map[string]string{}
	for _, s := range a.services {
		k := kebab(s.short)
		if other, ok := groups[k]; ok {
			return fmt.Errorf("services %s and %s are both %q in WIT", other, s.name, k)
		}
		groups[k] = s.name
		names := map[string]bool{}
		for _, m := range s.methods {
			if names[kebab(m.name)] {
				return fmt.Errorf("%s: two methods are %q in WIT", s.name, kebab(m.name))
			}
			names[kebab(m.name)] = true
		}
		// A function and a type its interface uses share one namespace:
		// wasm-tools refuses the two of a name.
		for _, u := range s.uses() {
			if names[u] {
				return fmt.Errorf("%s: a method and a type it uses are both %q in WIT", s.name, u)
			}
		}
	}
	return nil
}

// noCycle refuses a message that holds itself, through fields however
// deep.
func (a *api) noCycle(m *message, stack []string) error {
	if slices.Contains(stack, m.name) {
		return fmt.Errorf("%s holds itself (%s): WIT types cannot recurse", m.name, strings.Join(append(stack, m.name), " → "))
	}
	stack = append(stack, m.name)
	for _, f := range m.fields {
		t := f
		if f.mapVal != nil {
			t = f.mapVal
		}
		if t.msg != nil {
			if err := a.noCycle(t.msg, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

// permissions are the permissions the methods take, but for none, sorted.
func (a *api) permissions() []string {
	set := map[string]bool{}
	for _, s := range a.services {
		for _, m := range s.methods {
			if m.perm != PermNone {
				set[m.perm] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// comments are a file's leading comments, by the path of what they are
// on, as path makes it.
func comments(f *descriptorpb.FileDescriptorProto) map[string]string {
	out := map[string]string{}
	for _, loc := range f.GetSourceCodeInfo().GetLocation() {
		if c := loc.GetLeadingComments(); c != "" {
			ints := make([]int, len(loc.GetPath()))
			for i, p := range loc.GetPath() {
				ints[i] = int(p)
			}
			out[path(ints...)] = strings.TrimSpace(c)
		}
	}
	return out
}

func path(p ...int) string { return fmt.Sprint(p) }
