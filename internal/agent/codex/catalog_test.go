package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestEffortArgsAndCatalog(t *testing.T) {
	a := New()
	if got := a.EffortArgs("xhigh"); !slices.Equal(got, []string{"-c", `model_reasoning_effort="xhigh"`}) {
		t.Fatalf("EffortArgs = %q", got)
	}

	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "models": [
	    {
	      "slug": "gpt-6.2-sol",
	      "display_name": "GPT-6.2-Sol",
	      "visibility": "list",
	      "supported_reasoning_levels": [{"effort": "low"}, {"effort": "high"}, {"effort": "ultra"}]
	    },
	    {
	      "slug": "gpt-hidden",
	      "display_name": "Hidden",
	      "visibility": "hide",
	      "supported_reasoning_levels": [{"effort": "low"}]
	    }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(codexDir, "models_cache.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Falls back to HOME's ~/.codex when CODEX_HOME is empty.
	got, err := a.Catalog(t.Context(), map[string]string{"HOME": home, "CODEX_HOME": t.TempDir()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].ID != "gpt-6.2-sol" || got[0].Name != "GPT-6.2-Sol" || !slices.Equal(got[0].Efforts, []string{"low", "high", "ultra"}) {
		t.Fatalf("model = %+v", got[0])
	}
}

func TestCatalogRefreshUsesAppServerAndFallsBackToCache(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheBody := `{"models":[{"slug":"cached-model","display_name":"Cached Model","visibility":"list","supported_reasoning_levels":[{"effort":"low"}]}]}`
	if err := os.WriteFile(filepath.Join(codexDir, "models_cache.json"), []byte(cacheBody), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := queryCatalogModels
	t.Cleanup(func() { queryCatalogModels = orig })

	calls := 0
	var retModels []agent.ModelInfo
	var retErr error
	queryCatalogModels = func(_ context.Context, _ map[string]string) ([]agent.ModelInfo, error) {
		calls++
		return retModels, retErr
	}

	a := New()
	env := map[string]string{"HOME": home}

	// Fast path: refresh=false uses cache without invoking app-server.
	got, err := a.Catalog(t.Context(), env, false)
	if err != nil || len(got) != 1 || got[0].ID != "cached-model" || calls != 0 {
		t.Fatalf("fast path got=%+v err=%v calls=%d", got, err, calls)
	}

	// refresh=true queries app-server and returns live models.
	retModels = []agent.ModelInfo{{ID: "live-model", Name: "Live Model", Efforts: []string{"medium", "high"}}}
	got, err = a.Catalog(t.Context(), env, true)
	if err != nil || len(got) != 1 || got[0].ID != "live-model" || calls != 1 {
		t.Fatalf("refresh live got=%+v err=%v calls=%d", got, err, calls)
	}

	// refresh=true falls back to cache when app-server query fails.
	retModels, retErr = nil, errors.New("boom")
	got, err = a.Catalog(t.Context(), env, true)
	if err != nil || len(got) != 1 || got[0].ID != "cached-model" || calls != 2 {
		t.Fatalf("refresh fallback got=%+v err=%v calls=%d", got, err, calls)
	}

	// Missing cache with refresh=false queries app-server.
	retModels, retErr = []agent.ModelInfo{{ID: "fresh-model", Name: "Fresh"}}, nil
	got, err = a.Catalog(t.Context(), map[string]string{"HOME": t.TempDir()}, false)
	if err != nil || len(got) != 1 || got[0].ID != "fresh-model" || calls != 3 {
		t.Fatalf("missing cache live got=%+v err=%v calls=%d", got, err, calls)
	}
}
