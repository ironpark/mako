package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/agent"
)

const catalogQueryTimeout = 5 * time.Second

// queryCatalogModels queries live models via `codex app-server --stdio`;
// overridden in tests.
var queryCatalogModels = func(ctx context.Context, env map[string]string) ([]agent.ModelInfo, error) {
	program, err := agent.Find("codex", New().Info().Dirs...)
	if err != nil {
		return nil, err
	}
	return QueryModelsStdio(ctx, program, env)
}

// Catalog discovers Codex's available models and reasoning effort levels.
// When refresh is false, models_cache.json under CODEX_HOME / ~/.codex is used
// as a fast path. When refresh is true, or when the cache is missing/empty,
// Catalog queries a short-lived `codex app-server --stdio` via `model/list`,
// falling back to models_cache.json if the live query fails.
func (*Agent) Catalog(ctx context.Context, env map[string]string, refresh bool) ([]agent.ModelInfo, error) {
	cached := readCatalogCache(env)
	if !refresh && len(cached) > 0 {
		return cached, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	qctx, cancel := context.WithTimeout(ctx, catalogQueryTimeout)
	defer cancel()
	if live, err := queryCatalogModels(qctx, env); err == nil && len(live) > 0 {
		return live, nil
	}
	return cached, nil
}

func readCatalogCache(env map[string]string) []agent.ModelInfo {
	roots := []string{place.Root(env)}
	defaultRoot := place.Root(map[string]string{"HOME": env["HOME"]})
	if defaultRoot != roots[0] {
		roots = append(roots, defaultRoot)
	}
	for _, root := range roots {
		if models := readModelsCache(filepath.Join(root, "models_cache.json")); len(models) > 0 {
			return models
		}
	}
	return nil
}

func readModelsCache(path string) []agent.ModelInfo {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Models []struct {
			Slug                    string `json:"slug"`
			DisplayName             string `json:"display_name"`
			Visibility              string `json:"visibility"`
			SupportedReasoningLevel []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := make([]agent.ModelInfo, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		efforts := make([]string, 0, len(m.SupportedReasoningLevel))
		for _, r := range m.SupportedReasoningLevel {
			if r.Effort != "" {
				efforts = append(efforts, r.Effort)
			}
		}
		out = append(out, agent.ModelInfo{
			ID:      m.Slug,
			Name:    cmp.Or(m.DisplayName, m.Slug),
			Efforts: efforts,
		})
	}
	return out
}
