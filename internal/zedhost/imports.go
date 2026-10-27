package zedhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	wasmtime "github.com/ironpark/wasmtime-pure-go"
)

// hostFunc implements one import: it takes the call's arguments and
// returns its results. An error traps the guest; an import that fails as
// WIT says, with result<_, string>, returns Err instead.
type hostFunc func(e *Extension, args []Val) ([]Val, error)

// importSet is the functions and resources of one instance a component
// imports, or of the root.
type importSet struct {
	funcs     map[string]hostFunc
	resources map[string]uint32
}

// imports are the host's imports, by instance ("" for the world's own).
func imports() map[string]importSet {
	return map[string]importSet{
		"": {
			funcs: map[string]hostFunc{
				"get-settings":         (*Extension).getSettings,
				"download-file":        (*Extension).downloadFile,
				"make-file-executable": (*Extension).makeFileExecutable,
				"set-language-server-installation-status": (*Extension).setInstallationStatus,
				"[method]project.worktree-ids":            (*Extension).worktreeIDs,
			},
			resources: map[string]uint32{"worktree": resWorktree, "project": resProject, "key-value-store": resKeyValueStore},
		},
		"zed:extension/github": {funcs: map[string]hostFunc{
			"latest-github-release":      (*Extension).latestGitHubRelease,
			"github-release-by-tag-name": (*Extension).gitHubReleaseByTag,
		}},
		"zed:extension/platform": {funcs: map[string]hostFunc{
			"current-platform": (*Extension).currentPlatform,
		}},
		"zed:extension/nodejs": {funcs: map[string]hostFunc{
			"node-binary-path":              (*Extension).nodeBinaryPath,
			"npm-package-latest-version":    (*Extension).npmLatestVersion,
			"npm-package-installed-version": (*Extension).npmInstalledVersion,
			"npm-install-package":           (*Extension).npmInstall,
		}},
		"zed:extension/process": {funcs: map[string]hostFunc{
			"run-command": (*Extension).runCommand,
		}},
		"zed:extension/http-client": {funcs: map[string]hostFunc{
			"fetch": (*Extension).fetch,
		}},
	}
}

// componentImports lists the functions a component imports by instance,
// "" for its own, leaving out WASI's.
func componentImports(comp *wasmtime.Component) map[string][]string {
	out := map[string][]string{}
	ty := comp.Type()
	defer ty.Close()
	for i := range ty.ImportCount() {
		iname, item := ty.ImportNth(i)
		if item == nil {
			continue
		}
		switch item.Kind() {
		case wasmtime.ComponentItemKindComponentFunc:
			out[""] = append(out[""], iname)
		case wasmtime.ComponentItemKindComponentInstance:
			if strings.HasPrefix(iname, "wasi:") {
				break
			}
			it := item.InstanceType()
			var funcs []string
			for j := range it.ExportCount() {
				ename, ei := it.ExportNth(j)
				if ei == nil {
					continue
				}
				if ei.Kind() == wasmtime.ComponentItemKindComponentFunc {
					funcs = append(funcs, ename)
				}
				ei.Close()
			}
			it.Close()
			out[iname] = funcs
		}
		item.Close()
	}
	return out
}

// unsupported traps: an import the host does not implement, which a
// context server should never reach.
func unsupported(name string) hostFunc {
	return func(*Extension, []Val) ([]Val, error) {
		return nil, fmt.Errorf("mako does not support %s", name)
	}
}

// defineImports defines the host's imports, and for every other function
// the component imports outside WASI one that traps, so that it still
// instantiates. (wasmtime's define-unknown-imports-as-traps would shadow
// the WASI it already has under another patch version.)
func (e *Extension) defineImports() error {
	sets := imports()
	for inst, funcs := range componentImports(e.comp) {
		set, ok := sets[inst]
		if !ok {
			set = importSet{funcs: map[string]hostFunc{}}
			sets[inst] = set
		}
		for _, f := range funcs {
			if _, ok := set.funcs[f]; !ok {
				set.funcs[f] = unsupported(strings.TrimPrefix(inst+"#"+f, "#"))
			}
		}
	}
	root := e.linker.Root()
	defer root.Close()
	for name, set := range sets {
		li := root
		if name != "" {
			var err error
			if li, err = root.AddInstance(name); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		err := e.defineSet(li, set)
		if name != "" {
			li.Close()
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (e *Extension) defineSet(li *wasmtime.ComponentLinkerInstance, set importSet) error {
	for name, ty := range set.resources {
		if err := li.AddResource(name, ty); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, fn := range set.funcs {
		err := li.AddFunc(name, func(_ *wasmtime.ComponentCaller, args []Val) ([]Val, error) {
			res, err := fn(e, args)
			if err != nil {
				e.log.Debug("import failed", "import", name, "err", err)
			}
			return res, err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func arg(args []Val, i int) Val {
	if i < len(args) {
		return args[i]
	}
	return Val{}
}

func (e *Extension) context() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

// Settings ---------------------------------------------------------------

func (e *Extension) getSettings(args []Val) ([]Val, error) {
	category := arg(args, 1).Str
	var key string
	if k := arg(args, 2); k.Payload != nil {
		key = k.Payload.Str
	}
	if e.host.Settings != nil {
		s, err := e.host.Settings(category, key)
		if err != nil {
			return []Val{Err(err.Error())}, nil
		}
		if s != "" {
			return []Val{Ok(String(s))}, nil
		}
	}
	switch category {
	case "language":
		return []Val{Ok(String(`{"tab_size":4,"hard_tabs":false,"preferred_line_length":80}`))}, nil
	case "lsp", "context_servers":
		return []Val{Ok(String(`{}`))}, nil
	}
	return []Val{Errf("unknown settings category %q", category)}, nil
}

func (e *Extension) setInstallationStatus(args []Val) ([]Val, error) {
	status := arg(args, 1)
	attrs := []any{"server", arg(args, 0).Str, "status", status.Str}
	if status.Payload != nil {
		attrs = append(attrs, "detail", status.Payload.Str)
	}
	e.log.Debug("installation status", attrs...)
	return nil, nil
}

// worktreeIDs lists no worktrees: settings come from the node's
// configuration alone, never from a project's.
func (e *Extension) worktreeIDs(args []Val) ([]Val, error) {
	return []Val{List()}, nil
}

// Files ------------------------------------------------------------------

// path is a path the guest names, in its work directory, where its "."
// is. An absolute one must be inside it already.
func (e *Extension) path(p string) (string, error) {
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(e.work, p)
		if err != nil || !filepath.IsLocal(rel) && rel != "." {
			return "", fmt.Errorf("%s is outside the extension's work directory", p)
		}
		return p, nil
	}
	p = filepath.Clean(filepath.FromSlash(p))
	if p != "." && !filepath.IsLocal(p) {
		return "", fmt.Errorf("%s is outside the extension's work directory", p)
	}
	return filepath.Join(e.work, p), nil
}

func (e *Extension) makeFileExecutable(args []Val) ([]Val, error) {
	p, err := e.path(arg(args, 0).Str)
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	if runtime.GOOS == "windows" {
		return []Val{OkUnit()}, nil
	}
	fi, err := os.Stat(p)
	if err == nil {
		err = os.Chmod(p, fi.Mode().Perm()|0o755)
	}
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	return []Val{OkUnit()}, nil
}

func (e *Extension) downloadFile(args []Val) ([]Val, error) {
	url, kind := arg(args, 0).Str, arg(args, 2).Str
	dst, err := e.path(arg(args, 1).Str)
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	e.log.Info("downloading", "url", url, "to", dst)
	if err := download(e.context(), url, dst, kind); err != nil {
		return []Val{Errf("downloading %s: %v", url, err)}, nil
	}
	return []Val{OkUnit()}, nil
}

// Platform ---------------------------------------------------------------

func (e *Extension) currentPlatform(args []Val) ([]Val, error) {
	os := map[string]string{"darwin": "mac", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	arch := map[string]string{"arm64": "aarch64", "amd64": "x8664", "386": "x86"}[runtime.GOARCH]
	if os == "" || arch == "" {
		return nil, fmt.Errorf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return []Val{Tuple(Enum(os), Enum(arch))}, nil
}

// Node.js ----------------------------------------------------------------

func (e *Extension) nodeBinaryPath(args []Val) ([]Val, error) {
	p, err := exec.LookPath("node")
	if err != nil {
		return []Val{Err("Node.js is not installed: no node on the PATH")}, nil
	}
	return []Val{Ok(String(p))}, nil
}

// npm runs npm in the work directory and returns what it printed.
func (e *Extension) npm(args ...string) ([]byte, error) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return nil, errors.New("npm is not installed: no npm on the PATH")
	}
	cmd := exec.CommandContext(e.context(), npm, args...)
	cmd.Dir = e.work
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("npm %s: %v: %s", args[0], err, msg)
		}
		return nil, fmt.Errorf("npm %s: %v", args[0], err)
	}
	return out, nil
}

func (e *Extension) npmLatestVersion(args []Val) ([]Val, error) {
	out, err := e.npm("view", arg(args, 0).Str, "version", "--json")
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	var v string
	if err := json.Unmarshal(out, &v); err != nil || v == "" {
		return []Val{Errf("npm view %s: no version", arg(args, 0).Str)}, nil
	}
	return []Val{Ok(String(v))}, nil
}

func (e *Extension) npmInstalledVersion(args []Val) ([]Val, error) {
	name := arg(args, 0).Str
	p, err := e.path(filepath.Join("node_modules", filepath.FromSlash(name), "package.json"))
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return []Val{Ok(None())}, nil
	}
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil || pkg.Version == "" {
		return []Val{Ok(None())}, nil
	}
	return []Val{Ok(Some(String(pkg.Version)))}, nil
}

func (e *Extension) npmInstall(args []Val) ([]Val, error) {
	spec := arg(args, 0).Str + "@" + arg(args, 1).Str
	e.log.Info("npm install", "package", spec)
	if _, err := e.npm("install", "--prefix", e.work, "--save-exact", "--no-audit", "--no-fund",
		"--fetch-retry-mintimeout", "2000", "--fetch-retry-maxtimeout", "5000", spec); err != nil {
		return []Val{Err(err.Error())}, nil
	}
	return []Val{OkUnit()}, nil
}

// Processes --------------------------------------------------------------

func (e *Extension) runCommand(args []Val) ([]Val, error) {
	c := arg(args, 0)
	name, _ := field(c, "command")
	argv, _ := field(c, "args")
	env, _ := field(c, "env")
	if !e.Manifest.AllowsExec(name.Str, strs(argv)) {
		return []Val{Errf("the extension may not run %s: its manifest does not ask to", name.Str)}, nil
	}
	cmd := exec.CommandContext(e.context(), name.Str, strs(argv)...)
	cmd.Dir = e.work
	cmd.Env = os.Environ()
	for _, kv := range pairsOf(env) {
		cmd.Env = append(cmd.Env, kv[0]+"="+kv[1])
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	status := None()
	var exit *exec.ExitError
	switch {
	case err == nil:
		status = Some(S32(0))
	case errors.As(err, &exit):
		if code := exit.ExitCode(); code >= 0 {
			status = Some(S32(int32(code)))
		}
	default:
		return []Val{Err(err.Error())}, nil
	}
	return []Val{Ok(Record(
		Field{"status", status},
		Field{"stdout", Bytes(stdout.Bytes())},
		Field{"stderr", Bytes(stderr.Bytes())},
	))}, nil
}

// HTTP -------------------------------------------------------------------

// maxFetch caps a response read whole for the guest.
const maxFetch = 64 << 20

func (e *Extension) fetch(args []Val) ([]Val, error) {
	req := arg(args, 0)
	method, _ := field(req, "method")
	url, _ := field(req, "url")
	headers, _ := field(req, "headers")
	body, _ := field(req, "body")
	policy, _ := field(req, "redirect-policy")

	var rb io.Reader
	if body.Payload != nil {
		rb = bytes.NewReader(byteSlice(*body.Payload))
	}
	r, err := http.NewRequestWithContext(e.context(), strings.ToUpper(method.Str), url.Str, rb)
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	for _, kv := range pairsOf(headers) {
		r.Header.Add(kv[0], kv[1])
	}
	client := &http.Client{}
	switch policy.Str {
	case "no-follow":
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	case "follow-limit":
		limit := 0
		if policy.Payload != nil {
			limit = int(policy.Payload.Int)
		}
		client.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
			if len(via) > limit {
				return fmt.Errorf("more than %d redirects", limit)
			}
			return nil
		}
	}
	resp, err := client.Do(r)
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetch))
	if err != nil {
		return []Val{Err(err.Error())}, nil
	}
	var hs [][2]string
	for k, vs := range resp.Header {
		for _, v := range vs {
			hs = append(hs, [2]string{strings.ToLower(k), v})
		}
	}
	return []Val{Ok(Record(Field{"headers", Pairs(hs)}, Field{"body", Bytes(b)}))}, nil
}
