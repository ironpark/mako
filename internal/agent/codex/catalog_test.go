package codex

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
