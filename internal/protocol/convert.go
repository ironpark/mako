package protocol

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The wire carries the messages of proto/sanbo/v2, and the rest of sanbo
// the Go types of this package. They convert by name: a Go field matches
// the message field named as its JSON name. A field of either with no match
// in the other is an error, so a field added to a type and not to the
// schema, or to the schema and not to the type, fails the first time it is
// sent, and the round-trip test before that.
//
// Go types map to protobuf ones by kind: strings (named ones included) to
// string, integers to any integer field, float64 to double, []byte to
// bytes, time.Time to google.protobuf.Timestamp, structs and pointers to
// structs to messages, slices to repeated fields, map[string]string to a
// map, and a pointer to a scalar to an optional field.

// fieldPlan is one Go field and the message field it matches.
type fieldPlan struct {
	index []int
	fd    protoreflect.FieldDescriptor
}

type planKey struct {
	t    reflect.Type
	name protoreflect.FullName
}

var plans sync.Map // planKey -> []fieldPlan

var (
	timeType      = reflect.TypeFor[time.Time]()
	timestampName = (&timestamppb.Timestamp{}).ProtoReflect().Descriptor().FullName()
)

// planFor matches a struct type's fields to a message's.
func planFor(t reflect.Type, md protoreflect.MessageDescriptor) ([]fieldPlan, error) {
	key := planKey{t, md.FullName()}
	if p, ok := plans.Load(key); ok {
		return p.([]fieldPlan), nil
	}
	var plan []fieldPlan
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || f.Anonymous {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("protocol: %s.%s has no JSON name to match %s by", t, f.Name, md.FullName())
		}
		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return nil, fmt.Errorf("protocol: %s has no field %q for %s.%s", md.FullName(), name, t, f.Name)
		}
		plan = append(plan, fieldPlan{index: f.Index, fd: fd})
	}
	if len(plan) != md.Fields().Len() {
		for i := range md.Fields().Len() {
			fd := md.Fields().Get(i)
			if !slices.ContainsFunc(plan, func(p fieldPlan) bool { return p.fd == fd }) {
				return nil, fmt.Errorf("protocol: %s has no field for %s", t, fd.FullName())
			}
		}
	}
	plans.Store(key, plan)
	return plan, nil
}

// toProto fills m from v, a struct or a pointer to one.
func toProto(v reflect.Value, m protoreflect.Message) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("protocol: %s is not a struct to send as %s", v.Type(), m.Descriptor().FullName())
	}
	plan, err := planFor(v.Type(), m.Descriptor())
	if err != nil {
		return err
	}
	for _, p := range plan {
		gv := v.FieldByIndex(p.index)
		if gv.IsZero() {
			continue // what an unset field reads back as
		}
		switch {
		case p.fd.IsList():
			list := m.Mutable(p.fd).List()
			for i := range gv.Len() {
				pv, err := valueToProto(gv.Index(i), p.fd, list, nil)
				if err != nil {
					return err
				}
				list.Append(pv)
			}
		case p.fd.IsMap():
			if gv.Kind() != reflect.Map || gv.Type().Key().Kind() != reflect.String || gv.Type().Elem().Kind() != reflect.String {
				return fmt.Errorf("protocol: %s is not a map of strings for %s", gv.Type(), p.fd.FullName())
			}
			mp := m.Mutable(p.fd).Map()
			for it := gv.MapRange(); it.Next(); {
				mp.Set(protoreflect.ValueOfString(it.Key().String()).MapKey(), protoreflect.ValueOfString(it.Value().String()))
			}
		default:
			pv, err := valueToProto(gv, p.fd, nil, m)
			if err != nil {
				return err
			}
			m.Set(p.fd, pv)
		}
	}
	return nil
}

// valueToProto converts one value for fd: an element of list, or else of
// m. A struct is put into a new message of list's or m's.
func valueToProto(gv reflect.Value, fd protoreflect.FieldDescriptor, list protoreflect.List, m protoreflect.Message) (protoreflect.Value, error) {
	if gv.Kind() == reflect.Pointer && fd.Kind() != protoreflect.MessageKind {
		gv = gv.Elem() // an optional scalar; nil was skipped as zero
	}
	switch fd.Kind() {
	case protoreflect.MessageKind:
		if fd.Message().FullName() == timestampName {
			if gv.Kind() == reflect.Pointer {
				gv = gv.Elem()
			}
			if gv.Type() != timeType {
				return protoreflect.Value{}, fmt.Errorf("protocol: %s is not a time for %s", gv.Type(), fd.FullName())
			}
			return protoreflect.ValueOfMessage(timestamppb.New(gv.Interface().(time.Time)).ProtoReflect()), nil
		}
		var nv protoreflect.Value
		if list != nil {
			nv = list.NewElement()
		} else {
			nv = m.NewField(fd)
		}
		if err := toProto(gv, nv.Message()); err != nil {
			return protoreflect.Value{}, err
		}
		return nv, nil
	case protoreflect.StringKind:
		if gv.Kind() == reflect.String {
			return protoreflect.ValueOfString(gv.String()), nil
		}
	case protoreflect.BytesKind:
		if gv.Kind() == reflect.Slice && gv.Type().Elem().Kind() == reflect.Uint8 {
			return protoreflect.ValueOfBytes(gv.Bytes()), nil
		}
	case protoreflect.BoolKind:
		if gv.Kind() == reflect.Bool {
			return protoreflect.ValueOfBool(gv.Bool()), nil
		}
	case protoreflect.DoubleKind:
		if gv.CanFloat() {
			return protoreflect.ValueOfFloat64(gv.Float()), nil
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if n, ok := intOf(gv); ok {
			return protoreflect.ValueOfInt32(int32(n)), nil
		}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		if n, ok := intOf(gv); ok {
			return protoreflect.ValueOfInt64(n), nil
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if n, ok := intOf(gv); ok {
			return protoreflect.ValueOfUint32(uint32(n)), nil
		}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if n, ok := intOf(gv); ok {
			return protoreflect.ValueOfUint64(uint64(n)), nil
		}
	}
	return protoreflect.Value{}, fmt.Errorf("protocol: cannot send %s as %s (%s)", gv.Type(), fd.FullName(), fd.Kind())
}

// intOf reads any Go integer.
func intOf(v reflect.Value) (int64, bool) {
	switch {
	case v.CanInt():
		return v.Int(), true
	case v.CanUint():
		return int64(v.Uint()), true
	}
	return 0, false
}

// fromProto fills v, a pointer to a struct, from m.
func fromProto(m protoreflect.Message, v reflect.Value) error {
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("protocol: cannot read %s into %s", m.Descriptor().FullName(), v.Type())
	}
	v = v.Elem()
	plan, err := planFor(v.Type(), m.Descriptor())
	if err != nil {
		return err
	}
	for _, p := range plan {
		if !m.Has(p.fd) {
			continue
		}
		gv := v.FieldByIndex(p.index)
		switch {
		case p.fd.IsList():
			list := m.Get(p.fd).List()
			s := reflect.MakeSlice(gv.Type(), list.Len(), list.Len())
			for i := range list.Len() {
				if err := scalarFromProto(list.Get(i), p.fd, s.Index(i)); err != nil {
					return err
				}
			}
			gv.Set(s)
		case p.fd.IsMap():
			mp := m.Get(p.fd).Map()
			out := make(map[string]string, mp.Len())
			mp.Range(func(k protoreflect.MapKey, val protoreflect.Value) bool {
				out[k.String()] = val.String()
				return true
			})
			gv.Set(reflect.ValueOf(out).Convert(gv.Type()))
		default:
			if err := scalarFromProto(m.Get(p.fd), p.fd, gv); err != nil {
				return err
			}
		}
	}
	return nil
}

// scalarFromProto sets gv, one value or one element of a slice, from pv.
func scalarFromProto(pv protoreflect.Value, fd protoreflect.FieldDescriptor, gv reflect.Value) error {
	if gv.Kind() == reflect.Pointer {
		gv.Set(reflect.New(gv.Type().Elem()))
		if fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() != timestampName {
			return fromProto(pv.Message(), gv)
		}
		gv = gv.Elem()
	}
	switch fd.Kind() {
	case protoreflect.MessageKind:
		if fd.Message().FullName() == timestampName {
			gv.Set(reflect.ValueOf(pv.Message().Interface().(*timestamppb.Timestamp).AsTime().Local()))
			return nil
		}
		return fromProto(pv.Message(), gv.Addr())
	case protoreflect.StringKind:
		gv.SetString(pv.String())
	case protoreflect.BytesKind:
		gv.SetBytes(pv.Bytes())
	case protoreflect.BoolKind:
		gv.SetBool(pv.Bool())
	case protoreflect.DoubleKind:
		gv.SetFloat(pv.Float())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		setInt(gv, pv.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		setInt(gv, int64(pv.Uint()))
	default:
		return fmt.Errorf("protocol: cannot read %s into %s", fd.FullName(), gv.Type())
	}
	return nil
}

func setInt(gv reflect.Value, n int64) {
	if gv.CanUint() {
		gv.SetUint(uint64(n))
		return
	}
	gv.SetInt(n)
}
