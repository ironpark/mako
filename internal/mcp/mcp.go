// Package mcp gives the agents a node launches the MCP servers (Zed's
// context servers) it has: those of the Zed extensions installed, whose
// wasm says how to start them, and any configured by hand. Claude Code
// and Codex take them on their command lines.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/sanbo/internal/zed"
	"github.com/ironpark/sanbo/internal/zedhost"
)

// Server configures one MCP server, by its ID: a context server of a Zed
// extension installed, or with Command one of the user's own. It is the
// shape of Zed's own context_servers settings.
type Server struct {
	// Enabled turns the server off with false; unset is on, so a server
	// comes with the extension that has it.
	Enabled *bool `json:"enabled,omitempty"`
	// Settings are the extension's settings for the server, as its
	// configuration describes them: keys, URLs and the like.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Command starts the server instead of what its extension says.
	Command *Command `json:"command,omitempty"`
}

// On reports whether the server is enabled.
func (s Server) On() bool { return s.Enabled == nil || *s.Enabled }

// Command is how to start a server: its program, flags and environment.
type Command struct {
	Path string            `json:"path"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// Launch is a server resolved to the command that starts it.
type Launch struct {
	ID      string
	Command string
	Args    []string
	Env     map[string]string
}

// Resolver turns the servers configured into commands, running the wasm
// of the extensions that have them. What an extension answered is kept
// for a while, since asking can mean looking up and installing packages.
type Resolver struct {
	// Extensions is where the Zed extensions are installed; Work holds
	// their work directories, where they install their servers.
	Extensions, Work string
	Log              *slog.Logger

	once  sync.Once
	turn  chan struct{} // held by the Resolve running
	mu    sync.Mutex    // guards cache
	cache map[string]cached
}

type cached struct {
	key    string // the extension's version and the server's settings
	launch Launch
	at     time.Time
}

// cacheFor is how long an extension's answer is used before asking again,
// which updates a server the extension installs from its latest release.
const cacheFor = 6 * time.Hour

func (r *Resolver) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// Resolve lists the enabled servers as commands, sorted by ID: those
// configured with a command, and those of the extensions installed. A
// server that cannot be resolved, as one lacking its settings, is logged
// and left out.
//
// One Resolve runs at a time. One that waits for another past ctx gives
// the servers resolved before instead, without asking their extensions.
func (r *Resolver) Resolve(ctx context.Context, servers map[string]Server) []Launch {
	return r.resolveAll(ctx, servers, func(_ string, s Server) bool { return s.On() })
}

// ResolveOnly is Resolve for the servers ids alone, as a profile picks
// them, whether or not they are enabled.
func (r *Resolver) ResolveOnly(ctx context.Context, servers map[string]Server, ids []string) []Launch {
	return r.resolveAll(ctx, servers, func(id string, _ Server) bool { return slices.Contains(ids, id) })
}

func (r *Resolver) resolveAll(ctx context.Context, servers map[string]Server, on func(string, Server) bool) []Launch {
	r.once.Do(func() { r.turn = make(chan struct{}, 1) })
	cachedOnly := false
	select {
	case r.turn <- struct{}{}:
		defer func() { <-r.turn }()
	case <-ctx.Done():
		cachedOnly = true
	}
	byServer := map[string]zed.Extension{}
	if r.Extensions != "" {
		exts, err := (&zed.Store{Dir: r.Extensions}).Installed()
		if err != nil {
			r.log().Warn("mcp: reading the extensions", "err", err)
		}
		for _, e := range exts {
			for _, id := range e.ContextServerIDs() {
				byServer[id] = e
			}
		}
	}
	ids := slices.Sorted(maps.Keys(servers))
	for id := range byServer {
		if _, ok := servers[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	var out []Launch
	loaded := map[string]*zedhost.Extension{}
	defer func() {
		for _, e := range loaded {
			e.Close()
		}
	}()
	for _, id := range ids {
		s := servers[id]
		if !on(id, s) {
			continue
		}
		if s.Command != nil && s.Command.Path != "" {
			out = append(out, Launch{ID: id, Command: ExpandHome(s.Command.Path), Args: s.Command.Args, Env: s.Command.Env})
			continue
		}
		ext, ok := byServer[id]
		if !ok {
			continue // configured for an extension since removed
		}
		if cachedOnly {
			if l, ok := r.cached(id, s, ext); ok {
				out = append(out, l)
			}
			continue
		}
		l, err := r.resolve(ctx, id, servers, ext, loaded)
		if err != nil {
			r.log().Warn("mcp: server left out", "server", id, "extension", ext.ID, "err", err)
			continue
		}
		out = append(out, l)
	}
	return out
}

func (r *Resolver) resolve(ctx context.Context, id string, servers map[string]Server, ext zed.Extension, loaded map[string]*zedhost.Extension) (Launch, error) {
	s := servers[id]
	if l, ok := r.cached(id, s, ext); ok {
		return l, nil
	}
	e := loaded[ext.ID]
	if e == nil {
		host := &zedhost.Host{
			WorkDir:  r.Work,
			Settings: func(category, key string) (string, error) { return settingsFor(category, servers[key]) },
			Log:      r.log(),
		}
		var err error
		if e, err = host.Load(ctx, ext); err != nil {
			return Launch{}, err
		}
		loaded[ext.ID] = e
	}
	start := time.Now()
	cmd, err := e.ContextServerCommand(ctx, id)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{ID: id, Command: stable(cmd.Command), Args: cmd.Args, Env: map[string]string{}}
	for _, kv := range cmd.Env {
		l.Env[kv[0]] = kv[1]
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[id] = cached{key: cacheKey(s, ext), launch: l, at: time.Now()}
	r.mu.Unlock()
	r.log().Info("mcp: server resolved", "server", id, "command", l.Command, "took", time.Since(start).Round(time.Millisecond))
	return l, nil
}

func cacheKey(s Server, ext zed.Extension) string {
	return ext.ID + "@" + ext.Version + "\x00" + string(s.Settings)
}

// cached is what the extension answered for the server before, while it
// still holds: for the same version and settings, and not too long ago.
func (r *Resolver) cached(id string, s Server, ext zed.Extension) (Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[id]
	if !ok || c.key != cacheKey(s, ext) || time.Since(c.at) >= cacheFor || !runnable(c.launch.Command) {
		return Launch{}, false
	}
	return c.launch, true
}

// settingsFor answers an extension's get-settings for a server, the key
// of category context_servers: its settings, as Zed's
// ContextServerSettings reads them.
func settingsFor(category string, s Server) (string, error) {
	if category != "context_servers" {
		return "", nil
	}
	settings := json.RawMessage("null")
	if len(s.Settings) > 0 {
		settings = s.Settings
	}
	b, err := json.Marshal(struct {
		Settings json.RawMessage `json:"settings"`
	}{settings})
	return string(b), err
}

// Configuration asks the extension that has the server how to set it up,
// for the settings page: its instructions and default settings.
func Configuration(ctx context.Context, ext zed.Extension, work, id string) (*zedhost.ContextServerConfiguration, error) {
	e, err := (&zedhost.Host{WorkDir: work}).Load(ctx, ext)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	return e.ContextServerConfiguration(ctx, id)
}

// runnable reports whether a resolved command can still start, its
// program not removed since.
func runnable(command string) bool {
	if !filepath.IsAbs(command) {
		return true
	}
	_, err := os.Stat(command)
	return err == nil
}

// stable resolves a program reached through symlinks, as version managers
// put node behind a per-shell link that goes when the shell does.
func stable(command string) string {
	if !filepath.IsAbs(command) {
		return command
	}
	if p, err := filepath.EvalSymlinks(command); err == nil {
		return p
	}
	return command
}

// ExpandHome is p with a leading ~ in the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// Agents ------------------------------------------------------------------

// Takes reports whether the program command[0] is an agent that takes MCP
// servers on its command line.
func Takes(command []string) bool {
	return len(command) > 0 && Kind(command[0]) != ""
}

// Kind names the agent program a command runs: claude, codex, or "" for
// another program.
func Kind(program string) string {
	switch strings.TrimSuffix(filepath.Base(program), ".exe") {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	}
	return ""
}

// Apply gives the agent command starts the servers: claude a config file
// written in dir, named for the session, which cleanup removes; codex its
// -c overrides. A command for another program comes back as it is.
func Apply(command []string, servers []Launch, dir, session string) (out []string, cleanup func(), err error) {
	cleanup = func() {}
	if len(servers) == 0 || len(command) == 0 {
		return command, cleanup, nil
	}
	var flags []string
	switch Kind(command[0]) {
	case "claude":
		path, err := writeClaudeConfig(servers, dir, session)
		if err != nil {
			return command, cleanup, err
		}
		cleanup = func() { os.Remove(path) }
		// The = form, as the flag takes any number of values otherwise and
		// would swallow the arguments after it.
		flags = []string{"--mcp-config=" + path}
	case "codex":
		flags = codexFlags(servers)
	default:
		return command, cleanup, nil
	}
	return slices.Concat(command[:1], flags, command[1:]), cleanup, nil
}

// writeClaudeConfig writes the servers as Claude Code's .mcp.json reads
// them. It holds their keys, so only the user reads it.
func writeClaudeConfig(servers []Launch, dir, session string) (string, error) {
	type server struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}
	cfg := struct {
		Servers map[string]server `json:"mcpServers"`
	}{map[string]server{}}
	for _, s := range servers {
		cfg.Servers[s.ID] = server{Type: "stdio", Command: s.Command, Args: nonNil(s.Args), Env: s.Env}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp-"+session+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// codexFlags are -c overrides of Codex's mcp_servers table, each value
// TOML, which a JSON string or array of strings also is.
func codexFlags(servers []Launch) []string {
	var out []string
	for _, s := range servers {
		key := "mcp_servers." + tomlKey(s.ID)
		out = append(out, "-c", key+".command="+jsonString(s.Command))
		out = append(out, "-c", key+".args="+jsonStrings(nonNil(s.Args)))
		if len(s.Env) > 0 {
			var kv []string
			for _, k := range slices.Sorted(maps.Keys(s.Env)) {
				kv = append(kv, jsonString(k)+"="+jsonString(s.Env[k]))
			}
			out = append(out, "-c", key+".env={"+strings.Join(kv, ",")+"}")
		}
	}
	return out
}

// tomlKey is id as a TOML key, bare when it can be.
func tomlKey(id string) string {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return jsonString(id)
		}
	}
	return id
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func jsonStrings(s []string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ErrNoServer is returned for a server no extension installed has.
var ErrNoServer = errors.New("mcp: no extension installed has the server")

// Find is the installed extension that has the server id.
func Find(extensions, id string) (zed.Extension, error) {
	exts, err := (&zed.Store{Dir: extensions}).Installed()
	if err != nil {
		return zed.Extension{}, err
	}
	for _, e := range exts {
		if _, ok := e.ContextServers[id]; ok {
			return e, nil
		}
	}
	return zed.Extension{}, fmt.Errorf("%w: %s", ErrNoServer, id)
}
