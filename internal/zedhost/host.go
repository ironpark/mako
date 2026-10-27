// Package zedhost runs the wasm of Zed extensions, the components their
// Rust is compiled to against Zed's extension API, so mako can ask an
// extension what Zed would: chiefly the command that starts each of its
// context servers (MCP servers).
//
// Components run under wasmtime, through wasmtime-pure-go: its C API
// called without cgo, from the shared library it embeds, so mako builds
// and cross-compiles with no C toolchain.
//
// The host imports are Zed's (crates/extension_api/wit): settings,
// downloads, Node.js and npm, GitHub releases, the platform, processes
// and HTTP, implemented as an extension expects them, in its own work
// directory. Imports a context server has no use for, such as the
// language server and debugger ones, trap if called.
package zedhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	wasmtime "github.com/ironpark/wasmtime-pure-go"

	"github.com/ironpark/mako/internal/zed"
)

// Resource types the host defines; any values do, being the host's own.
const (
	resWorktree uint32 = iota + 1
	resProject
	resKeyValueStore
)

// sharedEngine is the process's one engine, for compiling and running;
// it fails when wasmtime's library cannot be loaded.
var sharedEngine = sync.OnceValues(func() (*wasmtime.Engine, error) {
	if err := wasmtime.LoadError(); err != nil {
		return nil, err
	}
	cfg := wasmtime.NewConfig()
	cfg.SetWasmComponentModel(true)
	return wasmtime.NewEngineWithConfig(cfg), nil
})

// Host runs extensions. Its fields are read when an extension loads.
type Host struct {
	// WorkDir holds a directory for each extension to download and
	// install into, as Zed's extensions/work does.
	WorkDir string
	// Settings answers an extension's get-settings: the JSON of the
	// settings in category ("context_servers", "lsp", "language") for
	// key, such as a context server's ID. Nil, or "" with no error, is
	// the defaults.
	Settings func(category, key string) (string, error)
	Log      *slog.Logger
}

// ContextServerConfiguration is what an extension says about setting up
// one of its context servers.
type ContextServerConfiguration struct {
	InstallationInstructions string // Markdown
	SettingsSchema           string // JSON Schema of its settings
	DefaultSettings          string // JSON
}

// Command is a program to start, with arguments and environment.
type Command struct {
	Command string
	Args    []string
	Env     [][2]string
}

// Extension is an extension's component, instantiated. Calls into it are
// serialized; Close frees it.
type Extension struct {
	Manifest zed.Manifest
	api      apiVersion
	dir      string
	work     string
	host     *Host
	log      *slog.Logger

	mu     sync.Mutex
	ctx    context.Context // the call in progress, for host imports
	store  *wasmtime.Store
	linker *wasmtime.ComponentLinker
	comp   *wasmtime.Component
	inst   *wasmtime.ComponentInstance
	err    error // sticky: a trap leaves the guest's state unknown
	closed bool
}

// Load compiles and instantiates an installed extension's wasm and runs
// its init-extension.
func (h *Host) Load(ctx context.Context, ext zed.Extension) (*Extension, error) {
	bin, err := os.ReadFile(filepath.Join(ext.Dir, "extension.wasm"))
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	api, err := parseAPIVersion(ext.Lib.Version, bin)
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	work := filepath.Join(h.WorkDir, ext.ID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	work, err = filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	log := h.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Extension{
		Manifest: ext.Manifest, api: api, dir: ext.Dir, work: work, host: h,
		log: log.With("extension", ext.ID),
	}
	e.comp, err = compile(work, bin)
	if err != nil {
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	if err := e.instantiate(); err != nil {
		e.Close()
		return nil, fmt.Errorf("zedhost: %s: %w", ext.ID, err)
	}
	if _, err := e.call(ctx, "init-extension", 0); err != nil {
		e.Close()
		return nil, fmt.Errorf("zedhost: %s: init-extension: %w", ext.ID, err)
	}
	return e, nil
}

// compile compiles a component, or reads the machine code a compile
// before left in the work directory; wasmtime refuses code from another
// version of itself, which then compiles again.
func compile(work string, bin []byte) (*wasmtime.Component, error) {
	engine, err := sharedEngine()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bin)
	cache := filepath.Join(work, ".mako-"+hex.EncodeToString(sum[:8])+".cwasm")
	if code, ok := readCompiled(cache); ok {
		if comp, err := wasmtime.NewComponentDeserialize(engine, code); err == nil {
			return comp, nil
		}
	}
	comp, err := wasmtime.NewComponent(engine, bin)
	if err != nil {
		return nil, err
	}
	if code, err := comp.Serialize(); err == nil {
		old, _ := filepath.Glob(filepath.Join(work, ".mako-*.cwasm"))
		for _, o := range old {
			if o != cache {
				os.Remove(o)
			}
		}
		_ = writeCompiled(cache, code)
	}
	return comp, nil
}

// Wasmtime runs compiled code as it finds it, so a compiled component
// read back must be exactly what was written: one cut short or half
// written over by another process compiling the same extension would
// crash it. The cache is written whole, under a temporary name renamed
// into place, and ends with the SHA-256 of what comes before, which is
// checked before wasmtime sees it.

// readCompiled reads compiled code written by writeCompiled, reporting
// false for none or for any that is not whole.
func readCompiled(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= sha256.Size {
		return nil, false
	}
	code, sum := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	if got := sha256.Sum256(code); !bytes.Equal(got[:], sum) {
		return nil, false
	}
	return code, true
}

// writeCompiled writes compiled code with its checksum, replacing what is
// at path all at once.
func writeCompiled(path string, code []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".compiling-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	sum := sha256.Sum256(code)
	if _, err := tmp.Write(append(slices.Clip(code), sum[:]...)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (e *Extension) instantiate() error {
	engine, err := sharedEngine()
	if err != nil {
		return err
	}
	e.store = wasmtime.NewStore(engine)
	if err := e.setWASI(); err != nil {
		return err
	}
	e.linker = wasmtime.NewComponentLinker(engine)
	if err := e.linker.AddWasip2(); err != nil {
		return err
	}
	if err := e.defineImports(); err != nil {
		return err
	}
	e.inst, err = e.linker.Instantiate(e.store, e.comp)
	return err
}

// setWASI gives the guest its work directory, as "." and as PWD, which
// std::env::current_dir reads, as Zed does.
func (e *Extension) setWASI() error {
	wasi := wasmtime.NewWasiConfig()
	wasi.SetEnv([]string{"PWD", "RUST_BACKTRACE"}, []string{e.work, "full"})
	if err := wasi.PreopenDir(e.work, ".", true); err != nil {
		return fmt.Errorf("wasi: cannot open the extension's work directory: %w", err)
	}
	e.store.SetWasi(wasi)
	return nil
}

// Close frees the instance. It is safe to call more than once.
func (e *Extension) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.linker != nil {
		e.linker.Close()
	}
	if e.store != nil {
		e.store.Close()
	}
	if e.comp != nil {
		e.comp.Close()
	}
}

// HasExport reports whether the extension exports a function by name.
func (e *Extension) HasExport(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	return e.export(name) != nil
}

func (e *Extension) export(name string) *wasmtime.ComponentFunc {
	idx := e.inst.GetExportIndex(e.store, nil, name)
	if idx == nil {
		return nil
	}
	defer idx.Close()
	return e.inst.GetFunc(e.store, idx)
}

// errNoExport is a call of a function the extension does not export, as
// an extension built against an older API lacks the newer ones.
var errNoExport = errors.New("zedhost: the extension does not export it")

// call runs an export with args and nresults results. ctx bounds the host
// imports it calls (downloads, installs); the guest's own code runs on.
func (e *Extension) call(ctx context.Context, name string, nresults int, args ...Val) ([]Val, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("zedhost: extension closed")
	}
	if e.err != nil {
		return nil, e.err
	}
	f := e.export(name)
	if f == nil {
		return nil, errNoExport
	}
	e.ctx = ctx
	defer func() { e.ctx = nil }()
	out, err := f.Call(e.store, args, nresults)
	if err != nil {
		e.err = fmt.Errorf("zedhost: %s trapped in %s: %w", e.Manifest.ID, name, err)
		return nil, e.err
	}
	return out, nil
}

// result unwraps a result<T, string>.
func result(v []Val, err error) (*Val, error) {
	if err != nil {
		return nil, err
	}
	if len(v) != 1 || v[0].Kind != KResult {
		return nil, errors.New("zedhost: the extension answered with something other than a result")
	}
	if !v[0].Ok {
		msg := "failed"
		if v[0].Payload != nil {
			msg = v[0].Payload.Str
		}
		return nil, errors.New(msg)
	}
	return v[0].Payload, nil
}

// project is the one project the host hands extensions: mako resolves
// a context server for the node, not for one of its folders.
const projectRep = 1

// ContextServerCommand asks the extension for the command that starts
// the context server id. A relative program is taken in the extension's
// work directory, where it would have installed it.
func (e *Extension) ContextServerCommand(ctx context.Context, id string) (Command, error) {
	pl, err := result(e.call(ctx, "context-server-command", 1, String(id), BorrowHost(projectRep, resProject)))
	if err != nil {
		return Command{}, err
	}
	if pl == nil {
		return Command{}, errors.New("zedhost: no command")
	}
	cmd, _ := field(*pl, "command")
	args, _ := field(*pl, "args")
	env, _ := field(*pl, "env")
	c := Command{Command: cmd.Str, Args: strs(args), Env: pairsOf(env)}
	if c.Command == "" {
		return Command{}, errors.New("zedhost: an empty command")
	}
	if !filepath.IsAbs(c.Command) && strings.ContainsRune(c.Command, '/') {
		c.Command = filepath.Join(e.work, c.Command)
	}
	return c, nil
}

// ContextServerConfiguration asks the extension how to set up the
// context server id. It is nil when the extension has nothing to say,
// as one built against an API older than 0.5.0 never does.
func (e *Extension) ContextServerConfiguration(ctx context.Context, id string) (*ContextServerConfiguration, error) {
	pl, err := result(e.call(ctx, "context-server-configuration", 1, String(id), BorrowHost(projectRep, resProject)))
	if errors.Is(err, errNoExport) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pl == nil || pl.Payload == nil {
		return nil, nil
	}
	c := *pl.Payload
	get := func(name string) string { v, _ := field(c, name); return v.Str }
	return &ContextServerConfiguration{
		InstallationInstructions: get("installation-instructions"),
		SettingsSchema:           get("settings-schema"),
		DefaultSettings:          get("default-settings"),
	}, nil
}

// WorkDir is the extension's work directory.
func (e *Extension) WorkDir() string { return e.work }
