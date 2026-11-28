package policy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

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

// Validate checks that r is well-formed.
func (r Rule) Validate() error {
	if r.Action != ActionAllow && r.Action != ActionDeny {
		return fmt.Errorf("policy action must be %q or %q, got %q", ActionAllow, ActionDeny, r.Action)
	}
	if strings.TrimSpace(r.Tool) == "" {
		return errors.New("policy tool pattern cannot be empty (use '*' for any tool)")
	}
	return nil
}

// Normalized returns r with trimmed fields and a generated ID if missing.
// An empty Tool stays empty, for Validate to refuse: it never widens to
// every tool.
func (r Rule) Normalized() Rule {
	r.ID = strings.TrimSpace(r.ID)
	if r.ID == "" {
		r.ID = NewID("pol")
	}
	r.Name = strings.TrimSpace(r.Name)
	r.Tool = strings.TrimSpace(r.Tool)
	r.Pattern = strings.TrimSpace(r.Pattern)
	r.Message = strings.TrimSpace(r.Message)
	return r
}

// LoadRules reads global permission policy rules from configDir/policies.json.
func LoadRules(configDir string) ([]Rule, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(configDir, policiesFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Rule
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = list[i].Normalized()
	}
	return list, nil
}

// SaveRules atomically writes global permission policy rules to configDir/policies.json.
func SaveRules(configDir string, rules []Rule) error {
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

// LoadProjectDenyRules loads optional deny-only rules from <project>/.mako/policy.toml.
// For security, repository-level policy files may only deny tool calls and can
// never grant auto-allow permissions.
func LoadProjectDenyRules(projectDir string) []Rule {
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(projectPolicyFile)))
	if err != nil {
		return nil
	}
	var doc projectPolicyDoc
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil
	}
	var out []Rule
	for _, r := range doc.Rules {
		r = r.Normalized()
		if r.Action != ActionDeny {
			continue
		}
		if r.Validate() == nil {
			out = append(out, r)
		}
	}
	return out
}

// Evaluate checks global rules (from configDir) and project deny rules (from
// projectDir) against an incoming tool approval request.
//
// Interactive approvals (questions, plans, layout changes, templates) are never
// auto-approved. Deny rules always take precedence over allow rules.
func Evaluate(configDir, projectDir, agentName, kind, tool, detail string) Decision {
	switch kind {
	case protocol.ToolQuestion, protocol.ToolExitPlan, protocol.ToolLayout, protocol.ToolTemplate:
		return Decision{}
	}
	globalRules, _ := LoadRules(configDir)
	projectRules := LoadProjectDenyRules(projectDir)
	return EvaluateRules(slices.Concat(globalRules, projectRules), agentName, tool, detail)
}

// EvaluateRules evaluates an explicit rule slice with deny-first precedence.
func EvaluateRules(rules []Rule, agentName, tool, detail string) Decision {
	var allowMatch *Rule
	for i := range rules {
		r := rules[i]
		// A rule edited by hand into one Validate refuses, an empty
		// tool among them, matches nothing.
		if !r.Enabled || r.Validate() != nil || !ruleMatches(r, agentName, tool, detail) {
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

func ruleMatches(r Rule, agentName, tool, detail string) bool {
	if len(r.Agents) > 0 {
		matchedAgent := false
		for _, a := range r.Agents {
			if a == "*" || strings.EqualFold(strings.TrimSpace(a), agentName) {
				matchedAgent = true
				break
			}
		}
		if !matchedAgent {
			return false
		}
	}
	if !matchGlobFold(r.Tool, tool) {
		return false
	}
	if r.Pattern != "" {
		return matchGlobFold(r.Pattern, detail)
	}
	return true
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
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			starIdx = pi
			matchIdx = si
			pi++
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
	data, err := os.ReadFile(filepath.Join(configDir, auditFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []AuditEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AppendAudit records one policy verdict into configDir/policies-audit.json.
func AppendAudit(configDir string, entry AuditEntry) error {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	if entry.Time == 0 {
		entry.Time = time.Now().Unix()
	}
	list, _ := LoadAudit(configDir)
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
