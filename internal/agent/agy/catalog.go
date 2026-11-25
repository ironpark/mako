package agy

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/safeexec"
)

const (
	catalogCacheTTL = 5 * time.Minute
	catalogTimeout  = 10 * time.Second
)

var agyEfforts = []string{"low", "medium", "high", "xhigh", "max"}

type catalogEntry struct {
	models []agent.ModelInfo
	at     time.Time
}

var (
	catalogMu    sync.Mutex
	catalogCache = map[string]catalogEntry{}
)

// Catalog discovers agy's available models by running `agy models`, cached for
// catalogCacheTTL per HOME directory unless refresh is true.
func (a *Agent) Catalog(ctx context.Context, env map[string]string, refresh bool) ([]agent.ModelInfo, error) {
	key := env["HOME"]
	if !refresh {
		catalogMu.Lock()
		entry, ok := catalogCache[key]
		catalogMu.Unlock()
		if ok && time.Since(entry.at) < catalogCacheTTL {
			return slices.Clone(entry.models), nil
		}
	}

	bin := "agy"
	if p, err := agent.Find(bin); err == nil {
		bin = p
	}
	tctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, "models")
	if len(env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), env)
	}
	out, err := safeexec.Output(cmd)
	if err != nil {
		return nil, err
	}
	models := parseModelsOutput(string(out))
	if len(models) > 0 {
		catalogMu.Lock()
		catalogCache[key] = catalogEntry{models: slices.Clone(models), at: time.Now()}
		catalogMu.Unlock()
	}
	return models, nil
}

// SplitModelEffort splits an agy model variant ID like "gemini-4-neon-high"
// into its base model ID ("gemini-4-neon") and effort tier ("high").
func SplitModelEffort(id string) (base, effort string, ok bool) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 || i+1 >= len(id) {
		return "", "", false
	}
	tier := strings.ToLower(id[i+1:])
	if !slices.Contains(agyEfforts, tier) {
		return "", "", false
	}
	return id[:i], tier, true
}

// NormalizeModelEffort prevents agy CLI conflicts when a legacy profile or
// caller passes both a variant model ID (e.g. "gemini-4-neon-high") and an
// --effort flag.
func (*Agent) NormalizeModelEffort(model, effort string) (string, string) {
	if model == "" || effort == "" {
		return model, effort
	}
	if base, tier, ok := SplitModelEffort(model); ok {
		if strings.EqualFold(tier, effort) {
			return model, ""
		}
		return base, effort
	}
	return model, effort
}

func stripEffortNameSuffix(name, tier string) string {
	trimmed := strings.TrimSpace(name)
	if !strings.HasSuffix(trimmed, ")") {
		return trimmed
	}
	open := strings.LastIndexByte(trimmed, '(')
	if open <= 0 {
		return trimmed
	}
	inside := strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
	if !strings.EqualFold(inside, tier) {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:open])
}

func parseModelsOutput(out string) []agent.ModelInfo {
	type rawEntry struct {
		id, name string
		baseID   string
		baseName string
		tier     string
		hasTier  bool
	}
	type group struct {
		baseID   string
		baseName string
		entries  []rawEntry
		tiers    map[string]bool
	}
	groups := map[string]*group{}

	// Preserve exact output order across both grouped base models and standalone models.
	type slot struct {
		isGroup bool
		key     string
		entry   rawEntry
	}
	var slots []slot

	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		id, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		baseID, tier, hasTier := SplitModelEffort(id)
		if !hasTier {
			re := rawEntry{id: id, name: name}
			slots = append(slots, slot{entry: re})
			continue
		}
		baseName := stripEffortNameSuffix(name, tier)
		re := rawEntry{id: id, name: name, baseID: baseID, baseName: baseName, tier: tier, hasTier: true}
		g := groups[baseID]
		if g == nil {
			g = &group{baseID: baseID, baseName: baseName, tiers: map[string]bool{}}
			groups[baseID] = g
			slots = append(slots, slot{isGroup: true, key: baseID})
		}
		g.entries = append(g.entries, re)
		g.tiers[tier] = true
	}

	var models []agent.ModelInfo
	for _, s := range slots {
		if !s.isGroup {
			models = append(models, agent.ModelInfo{
				ID:   s.entry.id,
				Name: s.entry.name,
			})
			continue
		}
		g := groups[s.key]
		if len(g.tiers) < 2 {
			// Single-tier fixed model (e.g. gpt-oss-120b-medium): keep exact variant ID with no effort selector.
			e := g.entries[0]
			models = append(models, agent.ModelInfo{
				ID:   e.id,
				Name: e.name,
			})
			continue
		}
		var efforts []string
		for _, t := range agyEfforts {
			if g.tiers[t] {
				efforts = append(efforts, t)
			}
		}
		models = append(models, agent.ModelInfo{
			ID:      g.baseID,
			Name:    g.baseName,
			Efforts: efforts,
		})
	}
	return models
}

func mergeEnv(base []string, overrides map[string]string) []string {
	m := make(map[string]string, len(base)+len(overrides))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	maps.Copy(m, overrides)
	out := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, k+"="+m[k])
	}
	return out
}
