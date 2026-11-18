package plugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ironpark/qngg"

	"github.com/ironpark/mako/internal/plugin/hostapi"
)

// A plugin runs in a QuickJS runtime of its own (qngg, in its own
// WebAssembly instance): no files, no network, no environment, nothing
// printed, its heap and stack bounded, every call into it bounded in time.
// Its modules are its folder's .js files, read by the daemon and handed
// to the runtime by name, so an import reaches nothing else. It reaches
// the host through one function, which the generated dispatcher answers.

//go:embed prelude.js
var preludeJS string

// The modules the runtime loads before a plugin's, under names no file of
// a plugin can have (their paths start with a letter, digit or _).
const (
	hostModule    = "@mako/host.js"
	preludeModule = "@mako/prelude.js"
)

// bridgeFunc carries out a call of the Host API: the method's name and
// its request as JSON, to the response as JSON.
type bridgeFunc func(ctx context.Context, method string, req []byte) ([]byte, error)

// jsRuntime is a plugin's runtime: one version of its code, started. It
// is used from the plugin's goroutine alone.
type jsRuntime struct {
	rt *qngg.Runtime
	r  *qngg.Value // the prelude's object, which the daemon calls
}

// limitError is a call that went over a limit: the runtime is not to be
// used again.
type limitError struct{ msg string }

func (e *limitError) Error() string { return e.msg }

// startRuntime makes a runtime for a version of a plugin and loads its
// modules, without running activate.
func startRuntime(ctx context.Context, v *version, lim Limits, bridge bridgeFunc) (_ *jsRuntime, err error) {
	defer trapped(&err)
	rt, err := qngg.New(qngg.Config{MemoryLimit: lim.Memory, MaxStackSize: lim.Stack})
	if err != nil {
		return nil, fmt.Errorf("JavaScript: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			closeQuietly(rt)
		}
	}()
	declared := map[string][]string{"blocks": {}, "commands": {}}
	for _, b := range v.man.Blocks {
		declared["blocks"] = append(declared["blocks"], b.ID)
	}
	for _, c := range v.man.Commands {
		declared["commands"] = append(declared["commands"], c.ID)
	}
	decl, _ := json.Marshal(declared)
	if err := rt.Set("__makoDeclared", string(decl)); err != nil {
		return nil, err
	}
	if err := rt.SetFunc("__makoCall", func(call *qngg.FunctionCall) (*qngg.Value, error) {
		method, req := call.Arg(0).String(), call.Arg(1).String()
		ctx := call.Context()
		// The host's time is not the plugin's.
		if b, ok := ctx.(*budget); ok {
			b.pause()
			defer b.resume()
		}
		resp, err := bridge(ctx, method, []byte(req))
		if err != nil {
			return nil, err
		}
		return call.Runtime().NewString(string(resp)), nil
	}); err != nil {
		return nil, err
	}
	if err := rt.LoadModule(ctx, hostModule, hostapi.JS); err != nil {
		return nil, fmt.Errorf("the Host API's bindings: %w", err)
	}
	if err := rt.LoadModule(ctx, preludeModule, preludeJS); err != nil {
		return nil, fmt.Errorf("the prelude: %w", err)
	}
	for _, name := range v.order {
		if err := rt.LoadModule(ctx, name, v.src[name]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, jsError(err))
		}
	}
	entry, _ := json.Marshal(v.man.Entry)
	r, err := rt.Eval(ctx, "@mako/boot.js",
		fmt.Sprintf("import R from %q;\nimport plugin from %s;\nexport default R.boot(plugin);\n", preludeModule, entry),
		qngg.AsModule())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.man.Entry, jsError(err))
	}
	ok = true
	return &jsRuntime{rt: rt, r: r}, nil
}

func (j *jsRuntime) close() {
	if j != nil && j.rt != nil {
		closeQuietly(j.rt)
		j.rt = nil
	}
}

// QuickJS built for WASI does not check its native stack, so JavaScript
// recursing deep enough exhausts the wasm stack, and the trap comes back
// as a panic of qngg's. It is taken as the plugin going over a limit: the
// runtime is closed, and the daemon goes on.

// trapped turns a panic of the runtime into a limitError.
func trapped(err *error) {
	if r := recover(); r != nil {
		*err = &limitError{msg: fmt.Sprintf("the plugin's runtime failed, its calls nested too deep or worse: %v", firstLine(r))}
	}
}

// closeQuietly closes a runtime that may have trapped, whose freeing may
// trap again.
func closeQuietly(rt *qngg.Runtime) {
	defer func() { _ = recover() }()
	rt.Close()
}

func firstLine(v any) string {
	s := fmt.Sprint(v)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// invoke calls the prelude's method with args, awaiting a promise it
// returns, and gives back what it came to as text: a JSON string the
// prelude made, or "".
func (j *jsRuntime) invoke(ctx context.Context, method string, args ...any) (_ string, err error) {
	defer trapped(&err)
	v, err := j.r.Invoke(ctx, method, args...)
	if err != nil {
		return "", err
	}
	defer v.Free()
	if v.IsPromise() {
		done, err := v.Await(ctx)
		if err != nil {
			return "", err
		}
		defer done.Free()
		v = done
	}
	if v.IsString() {
		return v.String(), nil
	}
	return "", nil
}

// classify says what a call's error was: a limit gone over, which ends
// the runtime, or the plugin's own error.
func classify(b *budget, err error) error {
	if err == nil {
		return nil
	}
	if over := b.exceeded(); over != nil || errors.Is(err, qngg.ErrInterrupted) {
		if over == nil {
			over = errCPU
		}
		return &limitError{msg: over.Error()}
	}
	var jsErr *qngg.Error
	if errors.As(err, &jsErr) {
		msg := jsErr.Message
		if jsErr.Name == "InternalError" && strings.Contains(msg, "out of memory") {
			return &limitError{msg: "the plugin used more memory than it may"}
		}
		if (jsErr.Name == "InternalError" || jsErr.Name == "RangeError") && strings.Contains(msg, "stack overflow") {
			return &limitError{msg: "the plugin's calls went deeper than they may"}
		}
	}
	return jsError(err)
}

// jsError is a JavaScript exception as a line: its name and message,
// without the stack.
func jsError(err error) error {
	var jsErr *qngg.Error
	if errors.As(err, &jsErr) {
		if jsErr.Name == "" {
			return errors.New(jsErr.Message)
		}
		return fmt.Errorf("%s: %s", jsErr.Name, jsErr.Message)
	}
	return err
}
