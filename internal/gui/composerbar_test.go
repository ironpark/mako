package gui

import "testing"

func TestModelsAreNamedAsPeopleSayThem(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":            "Opus 5.5",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-fable-5-1":           "Fable 5.1",
		"claude-haiku":               "Haiku",
		"gpt-5.1-codex":              "gpt-5.1-codex",
		"claude-":                    "claude-",
	} {
		if got := modelName(id); got != want {
			t.Errorf("modelName(%q) = %q, want %q", id, got, want)
		}
	}
}
