// Package hostapi is the plugin Host API: its messages (host.pb.go,
// later.pb.go), the dispatcher that checks a call's permission and hands
// it to a Handler (dispatch_gen.go), and the JavaScript bindings, types
// and WIT, all generated from proto/mako/plugin/v1 by `make proto` (see
// internal/plugin/apigen). This file is what the generated code leans on.
package hostapi

import (
	"context"
	_ "embed"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// JS is the bindings' module, which the runtime loads before a plugin:
// hostApi(call) builds mako.<group>.<method>.
//
//go:embed mako.js
var JS string

// Grants is what a plugin is allowed. Has reports whether it holds perm,
// one of the Perm constants; PermNet stands for any net:<host>, the host
// itself being the handler's to check.
type Grants interface {
	Has(perm string) bool
}

// Method is a method of the Host API: its name as the bindings call it,
// the permission it takes, and whether this version carries it out.
type Method struct {
	Name        string
	Permission  string
	Unavailable bool
}

// MaxRequest bounds a request's JSON, before it is read.
const MaxRequest = 1 << 20

// DeniedError is a call the plugin has no permission for.
type DeniedError struct{ Method, Permission string }

func (e *DeniedError) Error() string {
	if e.Permission == PermNet {
		return fmt.Sprintf("%s needs net:<host> for the host it names, which the plugin was not granted", e.Method)
	}
	return fmt.Sprintf("%s needs the permission %s, which the plugin was not granted", e.Method, e.Permission)
}

// UnavailableError is a call of a method this version does not carry out.
type UnavailableError struct{ Method string }

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("%s is not available in this version of mako", e.Method)
}

// UnknownError is a call of no method of the API.
type UnknownError struct{ Method string }

func (e *UnknownError) Error() string {
	return fmt.Sprintf("%q is no method of the Host API", e.Method)
}

// BadRequestError is a request that does not read as its method's.
type BadRequestError struct {
	Method string
	Err    error
}

func (e *BadRequestError) Error() string { return fmt.Sprintf("%s: bad request: %v", e.Method, e.Err) }

func (e *BadRequestError) Unwrap() error { return e.Err }

var (
	decode = protojson.UnmarshalOptions{}
	encode = protojson.MarshalOptions{EmitUnpopulated: true}
)

// call decodes data into in, strictly, has fn carry the call out, and
// encodes what it returns.
func call[Req, Resp proto.Message](ctx context.Context, data []byte, in Req, fn func(context.Context, Req) (Resp, error)) ([]byte, error) {
	name := string(in.ProtoReflect().Descriptor().Name())
	if len(data) > MaxRequest {
		return nil, &BadRequestError{Method: name, Err: fmt.Errorf("the request is over %d bytes", MaxRequest)}
	}
	if len(data) > 0 && string(data) != "null" {
		if err := decode.Unmarshal(data, in); err != nil {
			return nil, &BadRequestError{Method: name, Err: err}
		}
	}
	out, err := fn(ctx, in)
	if err != nil {
		return nil, err
	}
	return encode.Marshal(out)
}
