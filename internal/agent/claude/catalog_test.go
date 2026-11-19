package claude

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCatalogReadsNewestCCFileAndFallsBack(t *testing.T) {
	home := t.TempDir()
	a := New()

	// Without cache, falls back to built-in aliases.
	got, err := a.Catalog(t.Context(), map[string]string{"HOME": home}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(a.Models()) || got[0].ID != a.Models()[0] || len(got[0].Efforts) == 0 {
		t.Fatalf("fallback catalog = %+v", got)
	}

	// Write an older and newer *-cc.json cache in default home.
	catDir := filepath.Join(home, ".claude", "cache", "model-catalog")
	if err := os.MkdirAll(catDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(catDir, "old-cc.json")
	newPath := filepath.Join(catDir, "new-cc.json")
	oldBody := `{"catalog":{"config":{"models":[{"id":"old-model","name":"Old","section":"main"}]}}}`
	newBody := `{
	  "version": 2,
	  "catalog": {
	    "surface": "cc",
	    "config": {
	      "models": [
	        {"id": "claude-opus-5-5", "name": "Opus 5.5", "section": "main", "thinking": {"effort_options": [{"id": "low"}, {"id": "high"}]}},
	        {"id": "claude-haiku-4-5", "name": "Haiku 4.5", "section": "overflow"}
	      ]
	    }
	  }
	}`
	if err := os.WriteFile(oldPath, []byte(oldBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte(newBody), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(oldPath, past, past)

	// Isolated CLAUDE_CONFIG_DIR without cache yet falls back to HOME's cache.
	iso := t.TempDir()
	got, err = a.Catalog(t.Context(), map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": iso}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].ID != "claude-opus-5-5" || got[0].Name != "Opus 5.5" || got[0].Overflow || !slices.Equal(got[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("first model = %+v", got[0])
	}
	if got[1].ID != "claude-haiku-4-5" || !got[1].Overflow || len(got[1].Efforts) != 0 {
		t.Fatalf("second model = %+v", got[1])
	}
}
