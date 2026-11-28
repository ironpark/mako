package policy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
)

// Action is the automated verdict of a permission policy rule.
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
)

const (
	policiesFileName  = "policies.json"
	auditFileName     = "policies-audit.json"
	projectPolicyFile = ".mako/policy.toml"
	maxAuditEntries   = 200
)

// Rule defines one cross-agent permission policy rule evaluated before an
// approval is surfaced to the user.
type Rule struct {
	ID      string   `json:"id" toml:"id"`
	Name    string   `json:"name,omitempty" toml:"name"`
	Enabled bool     `json:"enabled" toml:"enabled"`
	Action  Action   `json:"action" toml:"action"`
	Agents  []string `json:"agents,omitempty" toml:"agents"`
	Tool    string   `json:"tool" toml:"tool"`
	Pattern string   `json:"pattern,omitempty" toml:"pattern"`
	Message string   `json:"message,omitempty" toml:"message"`
}

// Decision is the result of evaluating policy rules against a tool call.
type Decision struct {
	Matched bool
	Action  Action
	Rule    Rule
	Reason  string
}

// AuditEntry logs one automated allow or deny verdict.
type AuditEntry struct {
	Time    int64  `json:"time"`
	Session string `json:"session"`
	Agent   string `json:"agent"`
	Tool    string `json:"tool"`
	Detail  string `json:"detail,omitempty"`
	Action  Action `json:"action"`
	RuleID  string `json:"rule_id"`
	Rule    string `json:"rule,omitempty"`
}

type projectPolicyDoc struct {
	Rules []Rule `toml:"rule"`
}

// NewID generates a short random identifier for a rule.
func NewID(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// What a rule may hold: rules are read for every approval, and a request
// to save them is a front-end's to make.
const (
	MaxRules      = 256  // rules in policies.json
	maxIDLen      = 100  // bytes of a rule's id
	maxNameLen    = 200  // of its name
	maxToolLen    = 200  // of its tool
	maxPatternLen = 4096 // of its pattern
	maxMessageLen = 2000 // of its message
	maxAgents     = 32   // names in its agents
	maxAgentLen   = 100  // bytes of each
)

// The ways a rule fails to validate, each wrapped by what Validate says.
var (
	ErrAction     = fmt.Errorf("policy action must be %q or %q", ActionAllow, ActionDeny)
	ErrNoTool     = errors.New("policy tool cannot be empty (use * for any tool)")
	ErrBlankAgent = errors.New("policy agents cannot hold a blank name")
	ErrTooLong    = errors.New("policy rule field is too long")
	ErrTooMany    = fmt.Errorf("a node holds at most %d policy rules", MaxRules)
)

// Validate checks that r is well-formed.
func (r Rule) Validate() error {
	if r.Action != ActionAllow && r.Action != ActionDeny {
		return fmt.Errorf("%w, got %q", ErrAction, r.Action)
	}
	if strings.TrimSpace(r.Tool) == "" {
		return ErrNoTool
	}
	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"id", r.ID, maxIDLen}, {"name", r.Name, maxNameLen}, {"tool", r.Tool, maxToolLen},
		{"pattern", r.Pattern, maxPatternLen}, {"message", r.Message, maxMessageLen},
	} {
		if len(f.value) > f.max {
			return fmt.Errorf("%w: its %s holds %d bytes, past %d", ErrTooLong, f.name, len(f.value), f.max)
		}
	}
	if len(r.Agents) > maxAgents {
		return fmt.Errorf("%w: it names %d agents, past %d", ErrTooLong, len(r.Agents), maxAgents)
	}
	for _, a := range r.Agents {
		if strings.TrimSpace(a) == "" {
			return ErrBlankAgent
		}
		if len(a) > maxAgentLen {
			return fmt.Errorf("%w: an agent's name holds %d bytes, past %d", ErrTooLong, len(a), maxAgentLen)
		}
	}
	return nil
}

// Normalized returns r with trimmed fields and a generated ID if missing.
// An empty Tool stays empty, for Validate to refuse: it never widens to
// every tool. Agents are trimmed, the blank among them dropped; a list of
// blank names alone keeps one, for Validate to refuse, as it never widens
// to every agent either.
func (r Rule) Normalized() Rule {
	r.ID = strings.TrimSpace(r.ID)
	if r.ID == "" {
		r.ID = NewID("pol")
	}
	r.Name = strings.TrimSpace(r.Name)
	// "Deny" is deny, not an action Validate refuses and a rule lost.
	r.Action = Action(strings.ToLower(strings.TrimSpace(string(r.Action))))
	r.Tool = strings.TrimSpace(r.Tool)
	r.Pattern = strings.TrimSpace(r.Pattern)
	r.Message = strings.TrimSpace(r.Message)
	if len(r.Agents) > 0 {
		agents := present(r.Agents...)
		if len(agents) == 0 {
			agents = []string{""}
		}
		r.Agents = agents
	}
	return r
}

// LoadRules reads global permission policy rules from configDir/policies.json.
func LoadRules(configDir string) ([]Rule, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, nil
	}
	path := filepath.Join(configDir, policiesFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var list []Rule
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	for i := range list {
		list[i] = list[i].Normalized()
	}
	return list, nil
}

// rulesMu serializes SaveRules, as front-ends save at once.
var rulesMu sync.Mutex

// SaveRules atomically writes global permission policy rules to configDir/policies.json.
func SaveRules(configDir string, rules []Rule) error {
	if strings.TrimSpace(configDir) == "" {
		return errors.New("policy: no configuration directory to save rules in")
	}
	if len(rules) > MaxRules {
		return ErrTooMany
	}
	rulesMu.Lock()
	defer rulesMu.Unlock()
	clean := make([]Rule, len(rules))
	for i, r := range rules {
		r = r.Normalized()
		if err := r.Validate(); err != nil {
			return err
		}
		clean[i] = r
	}
	raw, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.Write(filepath.Join(configDir, policiesFileName), raw)
}

// maxProjectPolicySize bounds what is read of a repository's policy
// file, which the repository, not the user, wrote.
const maxProjectPolicySize = 256 << 10

// LoadProjectDenyRules loads optional deny-only rules from .mako/policy.toml
// in the repository projectDir is in: at the top of its work tree, of the
// main work tree for a linked one, and in projectDir itself. For security,
// repository-level policy files may only deny tool calls and can never
// grant auto-allow permissions; their allow rules are left out. A file that
// cannot be read, or a deny rule in it that does not validate, is an error,
// with the deny rules read besides.
func LoadProjectDenyRules(projectDir string) ([]Rule, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, nil
	}
	var out []Rule
	var errs []error
	for _, dir := range projectDirs(projectDir) {
		path := filepath.Join(dir, filepath.FromSlash(projectPolicyFile))
		data, err := readBounded(path, maxProjectPolicySize)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("policy: read %s: %w", path, err))
			continue
		}
		var doc projectPolicyDoc
		if _, err := toml.Decode(string(data), &doc); err != nil {
			errs = append(errs, fmt.Errorf("policy: read %s: %w", path, err))
			continue
		}
		for i, r := range doc.Rules {
			r = r.Normalized()
			if r.Action == ActionAllow {
				continue
			}
			if err := r.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("policy: rule %d of %s: %w", i+1, path, err))
				continue
			}
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}

// projectDirs are the folders a repository's policy file is read in for a
// session in dir: dir, the top of its work tree, and the main work tree.
func projectDirs(dir string) []string {
	dir = filepath.Clean(dir)
	dirs := []string{dir}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			dirs = append(dirs, d)
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	if repo := gitstat.Repo(dir); repo != "" {
		dirs = append(dirs, repo)
	}
	// Each once, however a link names it.
	for i, d := range dirs {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			dirs[i] = real
		}
	}
	return unique(dirs)
}

// readBounded reads the regular file at path, of at most limit bytes,
// without waiting on a FIFO or a device to give it any.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

// Evaluate checks global rules (from configDir) and project deny rules (from
// projectDir) against an incoming tool approval request.
//
// Interactive approvals (questions, plans, layout changes, templates) are never
// auto-approved. Deny rules always take precedence over allow rules.
//
// Rules that cannot be read fail closed: a rules file that cannot be read,
// or an enabled rule in one that does not validate, might be a deny rule
// lost, so while err says one is, no call is allowed. The deny rules read
// still hold.
func Evaluate(configDir, projectDir string, call Call) (Decision, error) {
	if interactive(call.Kind) {
		return Decision{}, nil
	}
	globalRules, gerr := LoadRules(configDir)
	projectRules, perr := LoadProjectDenyRules(projectDir)
	errs := []error{gerr, perr}
	for _, r := range globalRules {
		if err := r.Validate(); err != nil && r.Enabled {
			errs = append(errs, fmt.Errorf("policy: rule %q of %s: %w", r.ID, policiesFileName, err))
		}
	}
	err := errors.Join(errs...)
	dec := EvaluateRules(slices.Concat(globalRules, projectRules), call)
	if err != nil && dec.Action == ActionAllow {
		return Decision{}, err
	}
	return dec, err
}

// EvaluateRules evaluates an explicit rule slice with deny-first precedence.
// Interactive approvals (questions, plans, layout changes, templates) are
// never decided by a rule.
func EvaluateRules(rules []Rule, call Call) Decision {
	if interactive(call.Kind) {
		return Decision{}
	}
	// What the call acts on is read once, for the first rule with a
	// pattern, as a command line is parsed to read it.
	var read *subjects
	matches := func(r Rule) bool {
		if !agentMatches(r, call) || !toolMatches(r.Tool, call) {
			return false
		}
		if r.Pattern == "" {
			return true
		}
		if read == nil {
			s := readCall(call)
			read = &s
		}
		return patternMatches(r, *read)
	}
	var allowMatch *Rule
	for i := range rules {
		r := rules[i]
		// A rule edited by hand into one Validate refuses, an empty
		// tool among them, matches nothing.
		if !r.Enabled || r.Validate() != nil || !matches(r) {
			continue
		}
		if r.Action == ActionDeny {
			reason := r.Message
			if reason == "" {
				label := r.Name
				if label == "" {
					label = r.ID
				}
				reason = fmt.Sprintf("Blocked by Mako policy %q", label)
			}
			return Decision{Matched: true, Action: ActionDeny, Rule: r, Reason: reason}
		}
		if r.Action == ActionAllow && allowMatch == nil {
			cp := r
			allowMatch = &cp
		}
	}
	if allowMatch != nil {
		return Decision{Matched: true, Action: ActionAllow, Rule: *allowMatch}
	}
	return Decision{}
}

// interactive says a call of kind asks the user something, which no rule
// answers for them.
func interactive(kind string) bool {
	switch kind {
	case protocol.ToolQuestion, protocol.ToolExitPlan, protocol.ToolLayout, protocol.ToolTemplate:
		return true
	}
	return false
}

// agentMatches says r holds for the agent that makes call.
func agentMatches(r Rule, call Call) bool {
	if len(r.Agents) == 0 {
		return true
	}
	return slices.ContainsFunc(r.Agents, func(a string) bool {
		a = strings.TrimSpace(a)
		return a == "*" || strings.EqualFold(a, call.Agent)
	})
}

// matchGlobFold performs case-insensitive wildcard matching supporting '*'
// (matches any sequence of runes, including slashes) and '?' (single rune).
func matchGlobFold(pattern, text string) bool {
	p := []rune(strings.ToLower(strings.TrimSpace(pattern)))
	s := []rune(strings.ToLower(strings.TrimSpace(text)))
	pi, si := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(s) {
		switch {
		// A star is a wildcard first, even against a star in text.
		case pi < len(p) && p[pi] == '*':
			starIdx = pi
			matchIdx = si
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case starIdx != -1:
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// LoadAudit reads recent automated policy decisions (newest first).
func LoadAudit(configDir string) ([]AuditEntry, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, nil
	}
	path := filepath.Join(configDir, auditFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var list []AuditEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	return list, nil
}

// auditMu serializes AppendAudit's read, prepend and write: approval hooks
// are served at once, and two that wrote over each other would lose one.
var auditMu sync.Mutex

// AppendAudit records one policy verdict into configDir/policies-audit.json.
func AppendAudit(configDir string, entry AuditEntry) error {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	if entry.Time == 0 {
		entry.Time = time.Now().Unix()
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	// A log that cannot be read is kept as it is, not written over with
	// this entry alone.
	list, err := LoadAudit(configDir)
	if err != nil {
		return err
	}
	next := append([]AuditEntry{entry}, list...)
	if len(next) > maxAuditEntries {
		next = slices.Clone(next[:maxAuditEntries])
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return fsutil.Write(filepath.Join(configDir, auditFileName), raw)
}
