// Package syncstate implements Pure-Go HLC CRDT merge, digest, healing, and
// secret-stripping sanitization for P2P plugin config and agent config sync
// (protocol revision 47).
package syncstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/crdt"
)

// HLC conversions -------------------------------------------------------------

func ToHLC(p *protocol.HLC) crdt.HLC {
	if p == nil {
		return crdt.HLC{}
	}
	return crdt.HLC{WallTime: p.GetWallTime(), Logical: p.GetLogical(), NodeID: p.GetNodeId()}
}

func FromHLC(h crdt.HLC) *protocol.HLC {
	if h.IsZero() {
		return nil
	}
	return &protocol.HLC{WallTime: h.WallTime, Logical: h.Logical, NodeId: h.NodeID}
}

func takeClock(h crdt.HLC, clock *crdt.Clock, physNow int64) bool {
	if h.IsZero() || crdt.Quarantined(h, physNow) {
		return false
	}
	if clock != nil {
		clock.Observe(h, physNow)
	}
	return true
}

func mergeBoolReg(dst **protocol.BoolReg, src *protocol.BoolReg, key string, clock *crdt.Clock, physNow int64) bool {
	if src == nil {
		return false
	}
	sh := ToHLC(src.GetClock())
	if !takeClock(sh, clock, physNow) {
		return false
	}
	dh := ToHLC((*dst).GetClock())
	if !sh.After(dh, key) {
		return false
	}
	*dst = &protocol.BoolReg{Value: src.GetValue(), Clock: FromHLC(sh)}
	return true
}

func mergeBytesReg(dst **protocol.BytesReg, src *protocol.BytesReg, key string, clock *crdt.Clock, physNow int64) bool {
	if src == nil {
		return false
	}
	sh := ToHLC(src.GetClock())
	if !takeClock(sh, clock, physNow) {
		return false
	}
	dh := ToHLC((*dst).GetClock())
	if !sh.After(dh, key) {
		return false
	}
	*dst = &protocol.BytesReg{
		Value:   bytes.Clone(src.GetValue()),
		Deleted: src.GetDeleted(),
		Clock:   FromHLC(sh),
	}
	return true
}

func mergeElementMeta(dst **protocol.ElementMeta, src *protocol.ElementMeta, key string, clock *crdt.Clock, physNow int64) bool {
	if src == nil {
		return false
	}
	if *dst == nil {
		*dst = &protocol.ElementMeta{}
	}
	d := *dst
	changed := false
	if sa := ToHLC(src.GetAddedAt()); takeClock(sa, clock, physNow) && sa.After(ToHLC(d.GetAddedAt()), key+":add") {
		d.AddedAt = FromHLC(sa)
		changed = true
	}
	if sr := ToHLC(src.GetRemovedAt()); takeClock(sr, clock, physNow) && sr.After(ToHLC(d.GetRemovedAt()), key+":rem") {
		d.RemovedAt = FromHLC(sr)
		changed = true
	}
	if !changed && d.GetAddedAt() == nil && d.GetRemovedAt() == nil {
		*dst = nil
	}
	return changed
}

func mergeInstallEntry(dst **protocol.PluginInstallSyncEntry, src *protocol.PluginInstallSyncEntry, key string, clock *crdt.Clock, physNow int64) bool {
	if src == nil {
		return false
	}
	sh := ToHLC(src.GetClock())
	if !takeClock(sh, clock, physNow) {
		return false
	}
	dh := ToHLC((*dst).GetClock())
	if !sh.After(dh, key) {
		return false
	}
	*dst = &protocol.PluginInstallSyncEntry{
		Installed: src.GetInstalled(),
		Registry:  src.GetRegistry(),
		Key:       src.GetKey(),
		Version:   src.GetVersion(),
		Clock:     FromHLC(sh),
	}
	return true
}

// ElementLive reports whether an ElementMeta set member is currently present.
func ElementLive(m *protocol.ElementMeta, key string) bool {
	if m == nil {
		return false
	}
	add := ToHLC(m.GetAddedAt())
	if add.IsZero() {
		return false
	}
	rem := ToHLC(m.GetRemovedAt())
	if rem.IsZero() {
		return true
	}
	// Add-wins bias on equal clock, otherwise strictly newer clock wins.
	return crdt.Compare(add, rem, key) >= 0
}

// PluginConfigSyncState CRDT --------------------------------------------------

// EmptyPluginConfig reports whether st holds no replicated entries.
func EmptyPluginConfig(st *protocol.PluginConfigSyncState) bool {
	if st == nil {
		return true
	}
	return len(st.GetRegistries()) == 0 &&
		st.GetNoDefaultRegistry() == nil &&
		len(st.GetInstalls()) == 0 &&
		len(st.GetDisabled()) == 0 &&
		len(st.GetSettings()) == 0
}

// DigestPluginConfig returns a deterministic 32-byte SHA-256 hash of st, or nil
// when st is empty.
func DigestPluginConfig(st *protocol.PluginConfigSyncState) []byte {
	if EmptyPluginConfig(st) {
		return nil
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(st)
	if err != nil {
		data = []byte(err.Error())
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

// ObservePluginConfig advances clock with every non-quarantined stamp in st.
func ObservePluginConfig(st *protocol.PluginConfigSyncState, clock *crdt.Clock, physNow int64) {
	if st == nil || clock == nil {
		return
	}
	for _, m := range st.GetRegistries() {
		takeClock(ToHLC(m.GetAddedAt()), clock, physNow)
		takeClock(ToHLC(m.GetRemovedAt()), clock, physNow)
	}
	takeClock(ToHLC(st.GetNoDefaultRegistry().GetClock()), clock, physNow)
	for _, e := range st.GetInstalls() {
		takeClock(ToHLC(e.GetClock()), clock, physNow)
	}
	for _, b := range st.GetDisabled() {
		takeClock(ToHLC(b.GetClock()), clock, physNow)
	}
	for _, r := range st.GetSettings() {
		takeClock(ToHLC(r.GetClock()), clock, physNow)
	}
}

// HealPluginConfig re-stamps any entries authored by clock.NodeID() that were
// stamped ahead of physNow + MaxClockDrift (clock jump recovery).
func HealPluginConfig(st *protocol.PluginConfigSyncState, clock *crdt.Clock, physNow int64) bool {
	if st == nil || clock == nil {
		return false
	}
	self := clock.NodeID()
	changed := false
	heal := func(p **protocol.HLC) {
		h := ToHLC(*p)
		if h.NodeID == self && crdt.Quarantined(h, physNow) {
			*p = FromHLC(clock.Now(physNow))
			changed = true
		}
	}
	for _, m := range st.GetRegistries() {
		heal(&m.AddedAt)
		heal(&m.RemovedAt)
	}
	if r := st.GetNoDefaultRegistry(); r != nil {
		heal(&r.Clock)
	}
	for _, e := range st.GetInstalls() {
		heal(&e.Clock)
	}
	for _, b := range st.GetDisabled() {
		heal(&b.Clock)
	}
	for _, r := range st.GetSettings() {
		heal(&r.Clock)
	}
	return changed
}

// MergePluginConfig merges src into dst field by field and returns whether dst
// changed.
func MergePluginConfig(dst, src *protocol.PluginConfigSyncState, clock *crdt.Clock, physNow int64) bool {
	if dst == nil || src == nil {
		return false
	}
	changed := false
	if mergeBoolReg(&dst.NoDefaultRegistry, src.GetNoDefaultRegistry(), "plugin:no_default_registry", clock, physNow) {
		changed = true
	}
	for url, sm := range src.GetRegistries() {
		if url == "" {
			continue
		}
		if dst.Registries == nil {
			dst.Registries = map[string]*protocol.ElementMeta{}
		}
		cur := dst.Registries[url]
		if mergeElementMeta(&cur, sm, "plugin:reg:"+url, clock, physNow) {
			dst.Registries[url] = cur
			changed = true
		}
	}
	for id, se := range src.GetInstalls() {
		if id == "" {
			continue
		}
		if dst.Installs == nil {
			dst.Installs = map[string]*protocol.PluginInstallSyncEntry{}
		}
		cur := dst.Installs[id]
		if mergeInstallEntry(&cur, se, "plugin:inst:"+id, clock, physNow) {
			dst.Installs[id] = cur
			changed = true
		}
	}
	for id, sb := range src.GetDisabled() {
		if id == "" {
			continue
		}
		if dst.Disabled == nil {
			dst.Disabled = map[string]*protocol.BoolReg{}
		}
		cur := dst.Disabled[id]
		if mergeBoolReg(&cur, sb, "plugin:dis:"+id, clock, physNow) {
			dst.Disabled[id] = cur
			changed = true
		}
	}
	for k, sr := range src.GetSettings() {
		if _, _, _, ok := ParseSettingSyncKey(k); !ok {
			continue
		}
		if dst.Settings == nil {
			dst.Settings = map[string]*protocol.BytesReg{}
		}
		cur := dst.Settings[k]
		if mergeBytesReg(&cur, sr, "plugin:set:"+k, clock, physNow) {
			dst.Settings[k] = cur
			changed = true
		}
	}
	return changed
}

// LiveRegistries returns the sorted custom registry URLs currently active in st.
func LiveRegistries(st *protocol.PluginConfigSyncState) []string {
	var out []string
	for url, m := range st.GetRegistries() {
		if ElementLive(m, "plugin:reg:"+url) {
			out = append(out, url)
		}
	}
	slices.Sort(out)
	return out
}

// SettingSyncKey formats the CRDT map key for one non-secret plugin setting.
func SettingSyncKey(pluginID, scopeKey, settingKey string) string {
	return pluginID + "\x00" + scopeKey + "\x00" + settingKey
}

// ParseSettingSyncKey splits a plugin setting CRDT key into its parts.
func ParseSettingSyncKey(k string) (pluginID, scopeKey, settingKey string, ok bool) {
	parts := strings.Split(k, "\x00")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// AgentSyncState CRDT ---------------------------------------------------------

// EmptyAgentConfig reports whether st holds no replicated entries.
func EmptyAgentConfig(st *protocol.AgentSyncState) bool {
	if st == nil {
		return true
	}
	return len(st.GetAgents()) == 0 &&
		len(st.GetContextServers()) == 0 &&
		len(st.GetSkills()) == 0 &&
		len(st.GetTemplates()) == 0
}

// DigestAgentConfig returns a deterministic 32-byte SHA-256 hash of st, or nil
// when st is empty.
func DigestAgentConfig(st *protocol.AgentSyncState) []byte {
	if EmptyAgentConfig(st) {
		return nil
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(st)
	if err != nil {
		data = []byte(err.Error())
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

// ObserveAgentConfig advances clock with every non-quarantined stamp in st.
func ObserveAgentConfig(st *protocol.AgentSyncState, clock *crdt.Clock, physNow int64) {
	if st == nil || clock == nil {
		return
	}
	for _, r := range st.GetAgents() {
		takeClock(ToHLC(r.GetClock()), clock, physNow)
	}
	for _, r := range st.GetContextServers() {
		takeClock(ToHLC(r.GetClock()), clock, physNow)
	}
	for _, r := range st.GetSkills() {
		takeClock(ToHLC(r.GetClock()), clock, physNow)
	}
	for _, r := range st.GetTemplates() {
		takeClock(ToHLC(r.GetClock()), clock, physNow)
	}
}

// HealAgentConfig re-stamps any entries authored by clock.NodeID() that were
// stamped ahead of physNow + MaxClockDrift.
func HealAgentConfig(st *protocol.AgentSyncState, clock *crdt.Clock, physNow int64) bool {
	if st == nil || clock == nil {
		return false
	}
	self := clock.NodeID()
	changed := false
	heal := func(p **protocol.HLC) {
		h := ToHLC(*p)
		if h.NodeID == self && crdt.Quarantined(h, physNow) {
			*p = FromHLC(clock.Now(physNow))
			changed = true
		}
	}
	for _, r := range st.GetAgents() {
		heal(&r.Clock)
	}
	for _, r := range st.GetContextServers() {
		heal(&r.Clock)
	}
	for _, r := range st.GetSkills() {
		heal(&r.Clock)
	}
	for _, r := range st.GetTemplates() {
		heal(&r.Clock)
	}
	return changed
}

// MergeAgentConfig merges src into dst field by field and returns whether dst
// changed.
func MergeAgentConfig(dst, src *protocol.AgentSyncState, clock *crdt.Clock, physNow int64) bool {
	if dst == nil || src == nil {
		return false
	}
	changed := false
	for name, sr := range src.GetAgents() {
		if name == "" || name == agentspec.ShellName || !agentspec.ValidName(name) {
			continue
		}
		if dst.Agents == nil {
			dst.Agents = map[string]*protocol.BytesReg{}
		}
		cur := dst.Agents[name]
		if mergeBytesReg(&cur, sr, "agent:def:"+name, clock, physNow) {
			dst.Agents[name] = cur
			changed = true
		}
	}
	for id, sr := range src.GetContextServers() {
		if id == "" {
			continue
		}
		if dst.ContextServers == nil {
			dst.ContextServers = map[string]*protocol.BytesReg{}
		}
		cur := dst.ContextServers[id]
		if mergeBytesReg(&cur, sr, "agent:mcp:"+id, clock, physNow) {
			dst.ContextServers[id] = cur
			changed = true
		}
	}
	for name, sr := range src.GetSkills() {
		if !ValidSkillKey(name) {
			continue
		}
		if dst.Skills == nil {
			dst.Skills = map[string]*protocol.BytesReg{}
		}
		cur := dst.Skills[name]
		if mergeBytesReg(&cur, sr, "agent:skill:"+name, clock, physNow) {
			dst.Skills[name] = cur
			changed = true
		}
	}
	for file, sr := range src.GetTemplates() {
		if !ValidTemplateFilename(file) {
			continue
		}
		if dst.Templates == nil {
			dst.Templates = map[string]*protocol.BytesReg{}
		}
		cur := dst.Templates[file]
		if mergeBytesReg(&cur, sr, "agent:tmpl:"+file, clock, physNow) {
			dst.Templates[file] = cur
			changed = true
		}
	}
	return changed
}

// Sanitization & local-env preservation ---------------------------------------

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneStringSlicePtr(p *[]string) *[]string {
	if p == nil {
		return nil
	}
	cp := slices.Clone(*p)
	if cp == nil {
		cp = []string{}
	}
	return &cp
}

// SanitizeAgent returns a deep copy of a with agent and profile environment
// variables stripped so node-local secrets/tokens are never replicated.
func SanitizeAgent(a agentspec.Agent) agentspec.Agent {
	out := agentspec.Agent{
		Command:       slices.Clone(a.Command),
		Args:          slices.Clone(a.Args),
		Yolo:          a.Yolo,
		Enabled:       cloneBoolPtr(a.Enabled),
		AllowAgent:    cloneBoolPtr(a.AllowAgent),
		AllowTemplate: cloneBoolPtr(a.AllowTemplate),
	}
	if len(out.Command) == 0 {
		out.Command = nil
	}
	if len(out.Args) == 0 {
		out.Args = nil
	}
	if len(a.Profiles) > 0 {
		out.Profiles = make([]agentspec.Profile, len(a.Profiles))
		for i, p := range a.Profiles {
			np := agentspec.Profile{
				Name:           p.Name,
				Description:    p.Description,
				Args:           slices.Clone(p.Args),
				Model:          p.Model,
				Effort:         p.Effort,
				Yolo:           cloneBoolPtr(p.Yolo),
				ContextServers: cloneStringSlicePtr(p.ContextServers),
				AllowAgent:     cloneBoolPtr(p.AllowAgent),
				AllowTemplate:  cloneBoolPtr(p.AllowTemplate),
			}
			if len(np.Args) == 0 {
				np.Args = nil
			}
			out.Profiles[i] = np
		}
	}
	return out
}

// MarshalAgent encodes a sanitized agentspec.Agent into canonical JSON bytes.
func MarshalAgent(a agentspec.Agent) ([]byte, error) {
	return json.Marshal(SanitizeAgent(a))
}

// IsDefaultAgent reports whether a (after stripping env) matches the built-in
// default for name so fresh nodes do not stamp untouched defaults into CRDT state.
func IsDefaultAgent(name string, a agentspec.Agent) bool {
	defCmd, ok := agentspec.Builtin[name]
	if !ok {
		return false
	}
	s := SanitizeAgent(a)
	if len(s.Args) > 0 || len(s.Profiles) > 0 || s.Yolo || s.Enabled != nil || s.AllowAgent != nil || s.AllowTemplate != nil {
		return false
	}
	return len(s.Command) == 0 || slices.Equal(s.Command, defCmd)
}

// MergeLocalAgentEnv applies synced on top of local while preserving local's
// agent-level and per-profile Env maps untouched.
func MergeLocalAgentEnv(synced, local agentspec.Agent) agentspec.Agent {
	out := SanitizeAgent(synced)
	if len(local.Env) > 0 {
		out.Env = maps.Clone(local.Env)
	}
	if len(out.Profiles) > 0 && len(local.Profiles) > 0 {
		localEnv := make(map[string]map[string]string, len(local.Profiles))
		for _, p := range local.Profiles {
			if len(p.Env) > 0 {
				localEnv[p.Name] = p.Env
			}
		}
		for i := range out.Profiles {
			if env := localEnv[out.Profiles[i].Name]; len(env) > 0 {
				out.Profiles[i].Env = maps.Clone(env)
			}
		}
	}
	return out
}

// SanitizeMCPServer returns a deep copy of s with Command.Env stripped and
// Settings canonicalized so node-local env secrets are never replicated.
func SanitizeMCPServer(s mcp.Server) mcp.Server {
	out := mcp.Server{
		Enabled: cloneBoolPtr(s.Enabled),
	}
	if len(bytes.TrimSpace(s.Settings)) > 0 {
		var v any
		if json.Unmarshal(s.Settings, &v) == nil {
			if canon, err := json.Marshal(v); err == nil {
				out.Settings = canon
			} else {
				out.Settings = bytes.Clone(s.Settings)
			}
		} else {
			out.Settings = bytes.Clone(s.Settings)
		}
	}
	if s.Command != nil {
		cmd := &mcp.Command{
			Path: s.Command.Path,
			Args: slices.Clone(s.Command.Args),
		}
		if len(cmd.Args) == 0 {
			cmd.Args = nil
		}
		out.Command = cmd
	}
	return out
}

// MarshalMCPServer encodes a sanitized mcp.Server into canonical JSON bytes.
func MarshalMCPServer(s mcp.Server) ([]byte, error) {
	return json.Marshal(SanitizeMCPServer(s))
}

// MergeLocalMCPEnv applies synced on top of local while preserving local's
// Command.Env untouched when both declare a Command.
func MergeLocalMCPEnv(synced, local mcp.Server) mcp.Server {
	out := SanitizeMCPServer(synced)
	if out.Command != nil && local.Command != nil && len(local.Command.Env) > 0 {
		out.Command.Env = maps.Clone(local.Command.Env)
	}
	return out
}

// SameAgent reports whether two agents are deeply equal.
func SameAgent(a, b agentspec.Agent) bool { return reflect.DeepEqual(a, b) }

// SameMCPServer reports whether two MCP servers are deeply equal.
func SameMCPServer(a, b mcp.Server) bool { return reflect.DeepEqual(a, b) }

// ValidSkillName rejects path separators, traversal segments, and hidden names.
func ValidSkillName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) || name == ".." {
		return false
	}
	return filepath.Base(name) == name
}

// ValidSkillKey validates a synced skill override key: either a bare skill name
// (for SKILL.md) or a slash-separated relative path "<skill>/<subdir>/<file>.md"
// with no empty, hidden, or traversal segments.
func ValidSkillKey(key string) bool {
	if key == "" || strings.Contains(key, `\`) || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return false
	}
	parts := strings.Split(key, "/")
	if !ValidSkillName(parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	for _, seg := range parts[1:] {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return strings.HasSuffix(parts[len(parts)-1], ".md")
}

// ValidTemplateFilename reports whether name is a safe base Markdown filename.
func ValidTemplateFilename(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") || strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name && name != ".md"
}
