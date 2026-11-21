package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// ScopeContext describes the active workspace scope passed into plugin blocks,
// commands, and scoped MCP tools.
type ScopeContext struct {
	Scope       string   `json:"scope"`
	ScopeKey    string   `json:"scopeKey"`
	SpaceID     string   `json:"spaceId"`
	SpaceName   string   `json:"spaceName"`
	ProjectID   string   `json:"projectId"`
	ProjectName string   `json:"projectName"`
	SessionID   string   `json:"sessionId"`
	Roots       []string `json:"roots"`
}

// allowedScopes are the manifest-level scope declarations.
var allowedScopes = []string{
	protocol.ScopeGlobal,
	protocol.ScopeSpace,
	protocol.ScopeProject,
	protocol.ScopeSession,
}

// scopeIDRe bounds a canonical scope key's <id> part: safe ASCII identifier
// characters (including `:` and `/` used by Mako's repo:<host>/<owner>/<repo>
// project IDs), 1 to 256 bytes, no `@`, whitespace, or control characters.
var scopeIDRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._:/+-]{0,255}$`)

// normalizeScope checks a manifest's declared scope, normalizing "" to "global".
func normalizeScope(raw string) (string, error) {
	if raw == "" {
		return protocol.ScopeGlobal, nil
	}
	if !slices.Contains(allowedScopes, raw) {
		return "", fmt.Errorf("scope %q is not one of %s", raw, strings.Join(allowedScopes, ", "))
	}
	return raw, nil
}

// validateScopeID checks the <id> component of a canonical scope key.
func validateScopeID(id string) error {
	if !scopeIDRe.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("scope id %q is not a safe identifier of 1 to 256 characters", id)
	}
	return nil
}

// parseScopeKey validates a canonical scope key: "" (global), "space:<id>",
// "project:<id>", or "session:<id>".
func parseScopeKey(scopeKey string) (kind, id string, err error) {
	if scopeKey == "" {
		return protocol.ScopeGlobal, "", nil
	}
	kind, id, ok := strings.Cut(scopeKey, ":")
	if !ok {
		return "", "", fmt.Errorf("scope %q must be empty or <space|project|session>:<id>", scopeKey)
	}
	switch kind {
	case protocol.ScopeSpace, protocol.ScopeProject, protocol.ScopeSession:
	default:
		return "", "", fmt.Errorf("scope %q has unknown kind %q (want space, project, or session)", scopeKey, kind)
	}
	if err := validateScopeID(id); err != nil {
		return "", "", fmt.Errorf("scope %q: %w", scopeKey, err)
	}
	return kind, id, nil
}

// instanceKey builds the unique map key for a (blockID, scopeKey) runtime
// instance. Global instances (scopeKey == "") keep the bare blockID.
func instanceKey(blockID, scopeKey string) string {
	if scopeKey == "" {
		return blockID
	}
	return blockID + "@" + scopeKey
}

// splitInstanceKey splits instanceKey(blockID, scopeKey) back into its parts.
func splitInstanceKey(key string) (blockID, scopeKey string) {
	blockID, scopeKey, _ = strings.Cut(key, "@")
	return blockID, scopeKey
}

// resolveScope builds a ScopeContext for a validated canonical scopeKey,
// enriching it via Config.ResolveScope when configured.
func (m *Manager) resolveScope(scopeKey string) ScopeContext {
	if scopeKey == "" {
		return ScopeContext{
			Scope:    protocol.ScopeGlobal,
			ScopeKey: "",
			Roots:    []string{},
		}
	}
	kind, id, err := parseScopeKey(scopeKey)
	if err != nil {
		return ScopeContext{
			Scope:    protocol.ScopeGlobal,
			ScopeKey: "",
			Roots:    []string{},
		}
	}
	sc := ScopeContext{
		Scope:    kind,
		ScopeKey: scopeKey,
		Roots:    []string{},
	}
	switch kind {
	case protocol.ScopeSpace:
		sc.SpaceID = id
	case protocol.ScopeProject:
		sc.ProjectID = id
	case protocol.ScopeSession:
		sc.SessionID = id
	}
	if m.cfg.ResolveScope != nil {
		r := m.cfg.ResolveScope(scopeKey)
		if r.SpaceID != "" {
			sc.SpaceID = r.SpaceID
		}
		if r.SpaceName != "" {
			sc.SpaceName = r.SpaceName
		}
		if r.ProjectID != "" {
			sc.ProjectID = r.ProjectID
		}
		if r.ProjectName != "" {
			sc.ProjectName = r.ProjectName
		}
		if r.SessionID != "" {
			sc.SessionID = r.SessionID
		}
		if len(r.Roots) > 0 {
			sc.Roots = slices.Clone(r.Roots)
		}
		// Keep canonical scope kind and primary ID intact.
		sc.Scope = kind
		sc.ScopeKey = scopeKey
		switch kind {
		case protocol.ScopeSpace:
			if sc.SpaceID == "" {
				sc.SpaceID = id
			}
		case protocol.ScopeProject:
			if sc.ProjectID == "" {
				sc.ProjectID = id
			}
		case protocol.ScopeSession:
			if sc.SessionID == "" {
				sc.SessionID = id
			}
		}
	}
	if sc.Roots == nil {
		sc.Roots = []string{}
	}
	return sc
}

// resolveToolScope validates the caller-provided ScopeContext against a tool's
// declared scope and returns the canonical ScopeContext to pass into JS.
func (m *Manager) resolveToolScope(declaredScope string, provided ScopeContext) (ScopeContext, error) {
	decl, err := normalizeScope(declaredScope)
	if err != nil {
		return ScopeContext{}, err
	}
	var scopeKey string
	switch decl {
	case protocol.ScopeGlobal:
		return ScopeContext{
			Scope:    protocol.ScopeGlobal,
			ScopeKey: "",
			Roots:    []string{},
		}, nil
	case protocol.ScopeSpace:
		if provided.SpaceID == "" {
			return ScopeContext{}, fmt.Errorf("tool requires space scope (missing space ID)")
		}
		if err := validateScopeID(provided.SpaceID); err != nil {
			return ScopeContext{}, fmt.Errorf("tool space scope: %w", err)
		}
		scopeKey = protocol.ScopeSpace + ":" + provided.SpaceID
	case protocol.ScopeProject:
		if provided.ProjectID == "" {
			return ScopeContext{}, fmt.Errorf("tool requires project scope (missing project ID)")
		}
		if err := validateScopeID(provided.ProjectID); err != nil {
			return ScopeContext{}, fmt.Errorf("tool project scope: %w", err)
		}
		scopeKey = protocol.ScopeProject + ":" + provided.ProjectID
	case protocol.ScopeSession:
		if provided.SessionID == "" {
			return ScopeContext{}, fmt.Errorf("tool requires session scope (missing session ID)")
		}
		if err := validateScopeID(provided.SessionID); err != nil {
			return ScopeContext{}, fmt.Errorf("tool session scope: %w", err)
		}
		scopeKey = protocol.ScopeSession + ":" + provided.SessionID
	}

	sc := m.resolveScope(scopeKey)
	if provided.SpaceID != "" {
		sc.SpaceID = provided.SpaceID
	}
	if provided.SpaceName != "" {
		sc.SpaceName = provided.SpaceName
	}
	if provided.ProjectID != "" {
		sc.ProjectID = provided.ProjectID
	}
	if provided.ProjectName != "" {
		sc.ProjectName = provided.ProjectName
	}
	if provided.SessionID != "" {
		sc.SessionID = provided.SessionID
	}
	if len(provided.Roots) > 0 {
		sc.Roots = slices.Clone(provided.Roots)
	}
	if sc.Roots == nil {
		sc.Roots = []string{}
	}
	return sc, nil
}

func marshalScopeContext(sc ScopeContext) string {
	if sc.Scope == "" {
		sc.Scope = protocol.ScopeGlobal
	}
	if sc.Roots == nil {
		sc.Roots = []string{}
	}
	b, _ := json.Marshal(sc)
	return string(b)
}
