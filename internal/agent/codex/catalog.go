package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
)

// Catalog discovers Codex's available models and reasoning effort levels from
// models_cache.json under CODEX_HOME / ~/.codex.
func (*Agent) Catalog(_ context.Context, env map[string]string, _ bool) ([]agent.ModelInfo, error) {
	roots := []string{place.Root(env)}
	defaultRoot := place.Root(map[string]string{"HOME": env["HOME"]})
	if defaultRoot != roots[0] {
		roots = append(roots, defaultRoot)
	}
	for _, root := range roots {
		if models := readModelsCache(filepath.Join(root, "models_cache.json")); len(models) > 0 {
			return models, nil
		}
	}
	return nil, nil
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
