package agy

import (
	"slices"
	"testing"
)

func TestParseModelsOutput(t *testing.T) {
	raw := "Fetching available models...\n" +
		"gemini-4-neon-high\tGemini 4 Neon (High)\n" +
		"gemini-4-carbon-medium\tGemini 4 Carbon (Medium)\n" +
		"blank-name\t\n" +
		"ignored line without tab\n"
	got := parseModelsOutput(raw)
	if len(got) != 3 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].ID != "gemini-4-neon-high" || got[0].Name != "Gemini 4 Neon (High)" || !slices.Equal(got[0].Efforts, agyEfforts) {
		t.Fatalf("first model = %+v", got[0])
	}
	if got[2].ID != "blank-name" || got[2].Name != "blank-name" {
		t.Fatalf("third model = %+v", got[2])
	}
}
