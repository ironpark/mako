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
	out, err := cmd.Output()
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

func parseModelsOutput(out string) []agent.ModelInfo {
	var models []agent.ModelInfo
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
		models = append(models, agent.ModelInfo{
			ID:      id,
			Name:    name,
			Efforts: slices.Clone(agyEfforts),
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
