package gui

import (
	"reflect"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/sanbo/internal/protocol"
)

var messageType = reflect.TypeFor[proto.Message]()

// deepEqual is reflect.DeepEqual for values that hold messages, which it
// compares with proto.Equal: a message keeps state of its own that
// DeepEqual sees, so two equal ones read apart.
func deepEqual[T any](a, b T) bool {
	return equalValues(reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem())
}

func equalValues(a, b reflect.Value) bool {
	if a.Type().Implements(messageType) && a.CanInterface() {
		return protocol.Same(a.Interface().(proto.Message), b.Interface().(proto.Message))
	}
	switch a.Kind() {
	case reflect.Pointer:
		return a.Pointer() == b.Pointer() || !a.IsNil() && !b.IsNil() && equalValues(a.Elem(), b.Elem())
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return a.Elem().Type() == b.Elem().Type() && equalValues(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := range a.NumField() {
			if !equalValues(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if a.IsNil() != b.IsNil() {
			return false
		}
		fallthrough
	case reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !equalValues(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		for it := a.MapRange(); it.Next(); {
			v := b.MapIndex(it.Key())
			if !v.IsValid() || !equalValues(it.Value(), v) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	case reflect.Float32, reflect.Float64:
		return a.Float() == b.Float()
	case reflect.String:
		return a.String() == b.String()
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.Pointer() == b.Pointer()
	}
	return false
}
