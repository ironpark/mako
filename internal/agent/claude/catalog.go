package claude

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/ironpark/mako/internal/agent"
)

// Catalog discovers Claude Code's available models and effort levels from its
// model-catalog cache under CLAUDE_CONFIG_DIR / ~/.claude, falling back to
// its built-in aliases when no cache is present.
func (a *Agent) Catalog(_ context.Context, env map[string]string, _ bool) ([]agent.ModelInfo, error) {
	roots := []string{place.Root(env)}
	defaultRoot := place.Root(map[string]string{"HOME": env["HOME"]})
	if defaultRoot != roots[0] {
		roots = append(roots, defaultRoot)
	}
	for _, root := range roots {
		if models := readModelCatalog(root); len(models) > 0 {
			return models, nil
		}
	}
	return a.fallbackCatalog(), nil
}

func (a *Agent) fallbackCatalog() []agent.ModelInfo {
	aliases := a.Models()
	efforts := a.Efforts()
	out := make([]agent.ModelInfo, len(aliases))
	for i, id := range aliases {
		out[i] = agent.ModelInfo{ID: id, Name: id, Efforts: efforts}
	}
	return out
}

func readModelCatalog(root string) []agent.ModelInfo {
	if root == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "cache", "model-catalog", "*-cc.json"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	type candidate struct {
		path    string
		modTime int64
	}
	cands := make([]candidate, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || info.IsDir() {
			continue
		}
		cands = append(cands, candidate{path: m, modTime: info.ModTime().UnixNano()})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].modTime > cands[j].modTime })
	for _, c := range cands {
		if models := parseCatalogFile(c.path); len(models) > 0 {
			return models
		}
	}
	return nil
}

func parseCatalogFile(path string) []agent.ModelInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Catalog struct {
			Config struct {
				Models []struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					Section  string `json:"section"`
					Thinking *struct {
						EffortOptions []struct {
							ID string `json:"id"`
						} `json:"effort_options"`
					} `json:"thinking"`
				} `json:"models"`
			} `json:"config"`
		} `json:"catalog"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	models := doc.Catalog.Config.Models
	out := make([]agent.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		var efforts []string
		if m.Thinking != nil {
			for _, e := range m.Thinking.EffortOptions {
				if e.ID != "" {
					efforts = append(efforts, e.ID)
				}
			}
		}
		out = append(out, agent.ModelInfo{
			ID:       m.ID,
			Name:     cmp.Or(m.Name, m.ID),
			Efforts:  efforts,
			Overflow: m.Section == "overflow",
		})
	}
	return out
}
